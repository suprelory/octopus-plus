package task

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/suprelory/octopus-plus/internal/utils/log"
	"github.com/suprelory/octopus-plus/internal/utils/safe"
)

type taskEntry struct {
	name       string
	interval   time.Duration
	fn         func()
	runOnStart bool
	stopCh     chan struct{}
	updateCh   chan struct{}
	running    atomic.Bool
	loopDone   chan struct{}
	workers    sync.WaitGroup
}

var (
	tasks        = make(map[string]*taskEntry)
	tasksMu      sync.RWMutex
	tasksStarted bool
)

// Register 注册一个定时任务
// runOnStart: 是否在启动时立即执行一次
func Register(name string, interval time.Duration, runOnStart bool, fn func()) {
	if err := Configure(name, interval, runOnStart, fn); err != nil {
		log.Warnf("task %s registration failed: %v", name, err)
	}
}

// Configure creates or updates a task while preserving its execution guard.
// A zero interval disables scheduling but retains the function for reactivation.
func Configure(name string, interval time.Duration, runOnStart bool, fn func()) error {
	if name == "" || fn == nil || interval < 0 {
		return fmt.Errorf("task requires a name, function and non-negative interval")
	}
	tasksMu.Lock()
	defer tasksMu.Unlock()

	if entry, exists := tasks[name]; exists {
		entry.fn, entry.runOnStart = fn, runOnStart
		updateTaskLocked(entry, interval)
		return nil
	}

	tasks[name] = &taskEntry{
		name:       name,
		interval:   interval,
		fn:         fn,
		runOnStart: runOnStart,
		stopCh:     make(chan struct{}),
		updateCh:   make(chan struct{}, 1),
	}
	if tasksStarted {
		startTaskLocked(tasks[name])
	}
	log.Debugf("task %s registered with interval %v, runOnStart: %v", name, interval, runOnStart)
	return nil
}

// Update 更新任务的执行间隔
// 当 interval 为 0 时停用任务，保留执行函数以便重新启用。
func Update(name string, interval time.Duration) error {
	if interval < 0 {
		return fmt.Errorf("task interval must be non-negative")
	}
	tasksMu.Lock()
	defer tasksMu.Unlock()
	entry, exists := tasks[name]
	if !exists {
		return fmt.Errorf("task %s not found", name)
	}
	updateTaskLocked(entry, interval)
	return nil
}

func updateTaskLocked(entry *taskEntry, interval time.Duration) {
	if entry.interval == interval {
		return
	}
	entry.interval = interval
	select {
	case entry.updateCh <- struct{}{}:
		log.Infof("task %s interval updated to %v", entry.name, interval)
	default:
		// A wakeup is already queued; the loop reads the latest interval.
	}
}

// RUN 启动所有注册的任务
func RUN() {
	startTasks()
	// 阻塞主协程
	select {}
}

func startTasks() {
	tasksMu.Lock()
	defer tasksMu.Unlock()
	if tasksStarted {
		return
	}
	tasksStarted = true
	for _, entry := range tasks {
		startTaskLocked(entry)
	}
}

func startTaskLocked(entry *taskEntry) {
	entry.loopDone = make(chan struct{})
	go func() {
		defer close(entry.loopDone)
		safe.Run("task-loop:"+entry.name, func() { runTask(entry) })
	}()
}

func runTask(entry *taskEntry) {
	tasksMu.RLock()
	runOnStart := entry.runOnStart && entry.interval > 0
	tasksMu.RUnlock()
	// 根据配置决定是否在启动时立即执行
	if runOnStart {
		triggerTask(entry, "startup")
	}

	var ticker *time.Ticker
	var ticks <-chan time.Time
	resetTicker := func() {
		tasksMu.RLock()
		interval := entry.interval
		tasksMu.RUnlock()
		if ticker != nil {
			ticker.Stop()
		}
		ticks = nil
		if interval > 0 {
			if ticker == nil {
				ticker = time.NewTicker(interval)
			} else {
				ticker.Reset(interval)
			}
			ticks = ticker.C
		}
	}
	resetTicker()
	defer func() {
		if ticker != nil {
			ticker.Stop()
		}
	}()

	for {
		select {
		case <-ticks:
			triggerTask(entry, "ticker")
		case <-entry.updateCh:
			resetTicker()
		case <-entry.stopCh:
			return
		}
	}
}

func triggerTask(entry *taskEntry, trigger string) {
	if entry == nil {
		return
	}
	tasksMu.RLock()
	select {
	case <-entry.stopCh:
		tasksMu.RUnlock()
		return
	default:
	}
	if entry.interval <= 0 {
		tasksMu.RUnlock()
		return
	}
	if !entry.running.CompareAndSwap(false, true) {
		tasksMu.RUnlock()
		log.Warnf("task %s skipped: previous run still in progress (trigger=%s)", entry.name, trigger)
		return
	}
	fn := entry.fn
	entry.workers.Add(1)
	tasksMu.RUnlock()
	go func() {
		defer entry.workers.Done()
		defer entry.running.Store(false)
		safe.Run("task-exec:"+entry.name+":"+trigger, fn)
	}()
}
