package sitesync

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/fastschema/qjs"
	"github.com/suprelory/octopus-plus/internal/model"
)

const (
	checkinRewardExtractorTimeout      = 250 * time.Millisecond
	checkinRewardExtractorQueueTimeout = 5 * time.Second
	// The first qjs.New compiles WASM; race instrumentation and slower CPUs can
	// push this past five seconds. User code still gets only the execution budget.
	checkinRewardExtractorInitTimeout = 30 * time.Second
)

var (
	checkinRewardExtractorSlots = make(chan struct{}, 2)
	errRewardExtractorFailed    = errors.New("reward extractor failed; check the JavaScript syntax, response fields and resource limits")
	errRewardExtractorTimeout   = errors.New("reward extractor exceeded its execution time limit")
	errRewardExtractorResult    = errors.New("reward extractor must return a finite non-negative number, a numeric string, or null")
)

// ExtractCheckinReward runs a synchronous function body with only a JSON response
// as input. Each call gets a fresh, memory-limited WASM runtime. No Go objects,
// credentials, HTTP clients, environment variables or host files are exposed.
func ExtractCheckinReward(ctx context.Context, code string, response json.RawMessage) (reward string, err error) {
	if err := model.ValidateCheckinRewardExtractor(code); err != nil {
		return "", err
	}
	if len(response) > siteCheckinResponseLimit || !json.Valid(response) {
		return "", errors.New("reward extractor response must be valid JSON no larger than 1 MiB")
	}
	if strings.TrimSpace(code) == "" {
		return "", nil
	}
	// Bound queueing independently so a longer cold start does not increase the
	// time requests can spend waiting for an execution slot.
	queueCtx, queueCancel := context.WithTimeout(ctx, checkinRewardExtractorQueueTimeout)
	defer queueCancel()
	select {
	case checkinRewardExtractorSlots <- struct{}{}:
		defer func() { <-checkinRewardExtractorSlots }()
	case <-queueCtx.Done():
		return "", errRewardExtractorTimeout
	}
	if queueCtx.Err() != nil {
		return "", errRewardExtractorTimeout
	}
	queueCancel()
	// Give compilation its own budget while preserving the caller's deadline.
	initCtx, initCancel := context.WithTimeout(ctx, checkinRewardExtractorInitTimeout)
	defer initCancel()
	if initCtx.Err() != nil {
		return "", errRewardExtractorTimeout
	}
	defer func() {
		// wazero closes timed-out modules; qjs reports calls on them as panics.
		// Never return its stack trace or a script's thrown response data.
		panicked := recover() != nil
		if initCtx.Err() != nil {
			reward, err = "", errRewardExtractorTimeout
		} else if panicked {
			reward, err = "", errRewardExtractorFailed
		}
	}()
	runtime, err := qjs.New(qjs.Option{
		Context: initCtx, CloseOnContextDone: true,
		MemoryLimit: 16 << 20, MaxStackSize: 256 << 10,
		// qjs always mounts CWD. A null device is not a directory: every
		// guest path lookup fails, without creating a writable host directory.
		CWD: os.DevNull, Stdout: io.Discard, Stderr: io.Discard,
	})
	if err != nil {
		return "", errRewardExtractorFailed
	}
	defer func() {
		defer func() { _ = recover() }() // Already closed on cancellation.
		runtime.Close()
	}()
	// qjs and wazero may read Context concurrently even between WASM calls.
	// Keep the installed context immutable for the runtime's whole lifetime;
	// tighten the execution budget by cancelling it rather than replacing it.
	execTimer := time.AfterFunc(checkinRewardExtractorTimeout, initCancel)
	defer execTimer.Stop()
	// Eval is used only to compile the body and remove qjs host helpers. The
	// body is a quoted string, so it cannot execute during this step.
	body, _ := json.Marshal("\"use strict\";\n" + code)
	function, err := runtime.Eval("checkin-reward.js", qjs.Code(`
		for (const key of ["std", "os", "bjson", "print", "console", "scriptArgs",
			"setTimeout", "setInterval", "clearTimeout", "clearInterval", "QJS_PROXY_VALUE"]) {
			delete globalThis[key];
		}
		new Function("response", `+string(body)+`);
	`))
	if err != nil {
		return "", errRewardExtractorFailed
	}
	defer function.Free()
	js := runtime.Context()
	input := js.ParseJSON(string(response))
	defer input.Free()
	if js.HasException() {
		return "", errRewardExtractorFailed
	}
	this := js.NewUndefined()
	defer this.Free()
	// Invoke calls JS_Call directly. Do not use Eval or pump pending jobs after
	// running user code: asynchronous imports/timers are outside this contract.
	value, err := js.Invoke(function, this, input)
	if err != nil {
		return "", errRewardExtractorFailed
	}
	defer value.Free()
	if value.IsNull() || value.IsUndefined() {
		return "", nil
	}
	var number float64
	switch {
	case value.IsNumber():
		number = value.Float64()
	case value.IsString():
		result := strings.TrimSpace(value.String())
		if len(result) > 128 {
			return "", errRewardExtractorResult
		}
		number, err = strconv.ParseFloat(result, 64)
		if err != nil {
			return "", errRewardExtractorResult
		}
	default:
		return "", errRewardExtractorResult
	}
	if math.IsNaN(number) || math.IsInf(number, 0) || number < 0 {
		return "", errRewardExtractorResult
	}
	reward = strings.TrimRight(strings.TrimRight(strconv.FormatFloat(number, 'f', 6, 64), "0"), ".")
	if reward == "-0" {
		reward = "0"
	}
	return reward, nil
}
