// This example converts a request and a synthetic response without network I/O.
package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/suprelory/octopus-plus/polywire"
	"github.com/suprelory/octopus-plus/polywire/inbound"
	"github.com/suprelory/octopus-plus/polywire/outbound"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	ctx := context.Background()
	engine := polywire.New(polywire.Config{})
	client := engine.Inbound(inbound.InboundTypeOpenAIChat)
	request, err := client.TransformRequest(ctx, []byte(`{"model":"demo","messages":[{"role":"user","content":"Hello"}],"max_tokens":32}`))
	if err != nil {
		return err
	}
	target := outbound.OutboundTypeAnthropic
	plan, prepared := engine.PrepareRequestForModel(request, "claude-demo", target, false)
	if plan.Rejected() {
		return fmt.Errorf("conversion rejected: %s", plan.Summary())
	}
	// The caller chooses whether the reported losses are acceptable.
	fmt.Println("Plan:", plan.Status)
	provider := engine.Outbound(target)
	wire, report, err := prepared.Build(ctx, "https://example.invalid/v1", "")
	if err != nil {
		return err
	}
	defer wire.Body.Close()
	body, err := io.ReadAll(wire.Body)
	if err != nil {
		return err
	}
	fmt.Printf("Request: %s %s\n%s\nChanges: %d\n", wire.Method, wire.URL.Path, body, len(report))
	// In an application, the host executes wire with its own HTTP client and
	// passes the resulting response to this same provider adapter.
	response := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"id":"msg_demo","type":"message","role":"assistant","model":"claude-demo","content":[{"type":"text","text":"Hello from Polywire"}],"stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":4}}`))}
	defer response.Body.Close()
	canonical, err := provider.TransformResponse(ctx, response)
	if err != nil {
		return err
	}
	encoded, err := client.TransformResponse(ctx, canonical)
	if err != nil {
		return err
	}
	fmt.Printf("Response: %s\n", encoded)
	return nil
}
