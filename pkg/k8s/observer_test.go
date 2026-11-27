package k8s

import (
	"errors"
	"testing"
	"time"

	"go.uber.org/zap"
)

// startObserver runs runObserver with a tick channel that only the test drives,
// so the first tick never comes on its own. Each update call is reported on calls.
func startObserver(t *testing.T, fail func(n int) bool) (tick chan time.Time, calls chan int, stop chan struct{}, done chan struct{}) {
	t.Helper()
	tick = make(chan time.Time)
	calls = make(chan int, 16)
	stop = make(chan struct{})
	done = make(chan struct{})
	n := 0
	update := func() error {
		n++
		calls <- n
		if fail(n) {
			return errors.New("api not reachable")
		}
		return nil
	}
	noJitter := func() time.Duration { return 0 }
	go func() {
		defer close(done)
		runObserver(update, tick, noJitter, stop, zap.NewNop())
	}()
	return tick, calls, stop, done
}

func expectCall(t *testing.T, calls <-chan int, want int, why string) {
	t.Helper()
	select {
	case got := <-calls:
		if got != want {
			t.Fatalf("%s: got update call %d, want %d", why, got, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("%s: update call %d did not happen", why, want)
	}
}

func expectNoCall(t *testing.T, calls <-chan int, why string) {
	t.Helper()
	select {
	case got := <-calls:
		t.Fatalf("%s: unexpected update call %d", why, got)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestRunObserverFirstScanDoesNotWaitForTick(t *testing.T) {
	tick, calls, stop, done := startObserver(t, func(int) bool { return false })

	// No tick has been sent: the first scan must still happen.
	expectCall(t, calls, 1, "startup scan")
	expectNoCall(t, calls, "before the first tick")

	tick <- time.Now()
	expectCall(t, calls, 2, "first tick")
	tick <- time.Now()
	expectCall(t, calls, 3, "second tick")

	close(stop)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("runObserver did not return after stop")
	}
}

func TestRunObserverRetriesFailedFirstScanOnTick(t *testing.T) {
	tick, calls, stop, done := startObserver(t, func(n int) bool { return n == 1 })

	expectCall(t, calls, 1, "failing startup scan")
	expectNoCall(t, calls, "failed startup scan is not retried before the tick")

	tick <- time.Now()
	expectCall(t, calls, 2, "retry on the first tick")

	close(stop)
	<-done
}

func TestRunObserverStopsDuringJitter(t *testing.T) {
	tick := make(chan time.Time, 1)
	stop := make(chan struct{})
	done := make(chan struct{})
	calls := 0
	jitterStarted := make(chan struct{})
	jitter := func() time.Duration {
		close(jitterStarted)
		return time.Hour
	}
	go func() {
		defer close(done)
		runObserver(func() error { calls++; return nil }, tick, jitter, stop, zap.NewNop())
	}()

	tick <- time.Now()
	<-jitterStarted
	close(stop)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("runObserver did not return while waiting for the jitter")
	}
	if calls != 1 {
		t.Fatalf("got %d update calls, want only the startup scan", calls)
	}
}
