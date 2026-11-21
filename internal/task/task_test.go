package task

import (
	"sync"
	"testing"
	"time"
)

func resetTaskScheduler(t *testing.T) {
	t.Helper()
	reset := func() {
		tasksMu.Lock()
		previous := tasks
		for _, entry := range tasks {
			select {
			case <-entry.stopCh:
			default:
				close(entry.stopCh)
			}
		}
		tasks = make(map[string]*taskEntry)
		tasksStarted = false
		tasksStopping = false
		stopDone = nil
		unregisterSettings = nil
		tasksMu.Unlock()
		for _, entry := range previous {
			if entry.loopDone != nil {
				<-entry.loopDone
			}
			entry.workers.Wait()
		}
	}
	reset()
	t.Cleanup(reset)
}

func awaitTask(t *testing.T, ran <-chan struct{}) {
	t.Helper()
	select {
	case <-ran:
	case <-time.After(2 * time.Second):
		t.Fatal("task did not run")
	}
}

func TestRegisterAfterStartAndReenable(t *testing.T) {
	resetTaskScheduler(t)
	startTasks()
	ran := make(chan struct{}, 100)
	Register("backup", 5*time.Millisecond, false, func() { ran <- struct{}{} })
	awaitTask(t, ran)
	Update("backup", 0)
	if err := Update("backup", 5*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	awaitTask(t, ran)
}

func TestIntervalUpdateBeforeStartAndWhileRunning(t *testing.T) {
	resetTaskScheduler(t)
	ran := make(chan struct{}, 100)
	Register("backup", time.Hour, false, func() { ran <- struct{}{} })
	Update("backup", 5*time.Millisecond)
	startTasks()
	awaitTask(t, ran)
	Update("backup", time.Hour)
	var writers sync.WaitGroup
	for i := 0; i < 20; i++ {
		writers.Add(1)
		go func() { defer writers.Done(); Update("backup", time.Hour) }()
	}
	writers.Wait()
	// Drain earlier ticks so only the final interval update can satisfy this.
	for len(ran) > 0 {
		<-ran
	}
	Update("backup", 5*time.Millisecond)
	awaitTask(t, ran)
}

func TestSchedulerStartsEachTaskOnce(t *testing.T) {
	resetTaskScheduler(t)
	ran := make(chan struct{}, 10)
	Register("once", time.Hour, true, func() { ran <- struct{}{} })
	startTasks()
	awaitTask(t, ran)
	startTasks()
	select {
	case <-ran:
		t.Fatal("scheduler started a duplicate loop")
	case <-time.After(20 * time.Millisecond):
	}
}

func TestTaskCanStartDisabledAndBeReenabled(t *testing.T) {
	resetTaskScheduler(t)
	ran := make(chan struct{}, 100)
	Register("disabled", 0, true, func() { ran <- struct{}{} })
	tasksMu.RLock()
	entry := tasks["disabled"]
	tasksMu.RUnlock()
	if entry == nil {
		t.Fatal("disabled task lost its definition")
	}
	startTasks()
	triggerTask(entry, "disabled")
	select {
	case <-ran:
		t.Fatal("disabled task ran")
	default:
	}
	if err := Update("disabled", 5*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	awaitTask(t, ran)
	if err := Update("disabled", 0); err != nil {
		t.Fatal(err)
	}
	entry.workers.Wait()
	for len(ran) > 0 {
		<-ran
	}
	triggerTask(entry, "disabled-again")
	select {
	case <-ran:
		t.Fatal("disabled task ran after its interval was cleared")
	default:
	}
	if err := Update("disabled", 5*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	awaitTask(t, ran)
}

func TestTaskReconfigurationKeepsInFlightGuard(t *testing.T) {
	resetTaskScheduler(t)
	started := make(chan struct{}, 10)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	Register("busy", time.Hour, true, func() { started <- struct{}{}; <-release })
	startTasks()
	awaitTask(t, started)
	Update("busy", 0)
	if err := Configure("busy", time.Hour, false, func() { started <- struct{}{} }); err != nil {
		t.Fatal(err)
	}
	tasksMu.RLock()
	entry := tasks["busy"]
	tasksMu.RUnlock()
	triggerTask(entry, "reconfigured")
	select {
	case <-started:
		t.Fatal("reconfiguration started a second in-flight call")
	default:
	}
	unblock()
	entry.workers.Wait()
	triggerTask(entry, "after-completion")
	awaitTask(t, started)
}
