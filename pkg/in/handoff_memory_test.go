package in

import (
	"context"
	"errors"
	"testing"
)

func TestHandoffMemoryBatchesReleasedStorage(t *testing.T) {
	collections := 0
	m := &handoffMemory{threshold: 100, free: func() { collections++ }}
	m.release(60)
	m.release(39)
	if collections != 0 {
		t.Fatal("collected before a batch was released")
	}
	m.release(2)
	if collections != 1 || m.released != 0 {
		t.Fatal("released batch was not collected")
	}
	m.finish()
	if collections != 1 {
		t.Fatal("collected an empty batch")
	}
	m.release(1)
	m.finish()
	if collections != 2 {
		t.Fatal("final partial batch was not collected")
	}
	var small *handoffMemory
	small.release(100)
	small.finish()
}

func TestHandoffWaitsForReaderRelease(t *testing.T) {
	c := &inputConn{done: make(chan struct{}), moved: make(chan struct{})}
	close(c.moved) // Replay alone does not release the reader's dictionary.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitHandoffRelease(ctx, c); !errors.Is(err, context.Canceled) {
		t.Fatalf("returned before reader cleanup: %v", err)
	}
	c.releaseBytes = 123
	close(c.done)
	if err := waitHandoffRelease(context.Background(), c); err != nil || c.releaseBytes != 123 {
		t.Fatalf("reader cleanup was not observed: %v", err)
	}
}
