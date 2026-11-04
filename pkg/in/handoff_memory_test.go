package in

import "testing"

func TestHandoffMemoryBatchesReleasedStorage(t *testing.T) {
	collections := 0
	m := startHandoffMemory(100, func() { collections++ })
	// Serial sends synchronize with the single collector. An extra send
	// fences its processing of the preceding batch without timing sleeps.
	m.completed <- 60
	m.completed <- 39
	m.completed <- 2
	m.finish()
	<-m.done
	if collections != 1 || m.released != 0 {
		t.Fatalf("released batch not collected: calls=%d remaining=%d", collections, m.released)
	}

	collections = 0
	m = startHandoffMemory(100, func() { collections++ })
	m.completed <- 1
	m.finish()
	<-m.done
	if collections != 1 {
		t.Fatal("final partial batch was not collected")
	}
	var small *handoffMemory
	small.track(&inputConn{})
	small.finish()
}

func TestHandoffMemoryCapsCollections(t *testing.T) {
	collections := 0
	m := startHandoffMemory(100, func() { collections++ })
	for range 100 {
		m.completed <- 100
	}
	m.finish()
	<-m.done
	if collections != 6 || m.collections != 5 {
		t.Fatalf("want five batches plus final collection; got %d / %d", collections, m.collections)
	}
}

func TestHandoffMemoryObservesDelayedRelease(t *testing.T) {
	collections := 0
	m := startHandoffMemory(100, func() { collections++ })
	c := &inputConn{releaseBytes: 123, done: make(chan struct{})}
	m.track(c)
	m.track(&inputConn{done: make(chan struct{})}) // Zero-state reader never finishes.
	m.finish()
	select {
	case <-m.done:
		t.Fatal("collector did not wait for nonzero reader storage")
	default:
	}
	close(c.done)
	<-m.done
	if collections != 1 {
		t.Fatal("delayed storage was not collected")
	}
}
