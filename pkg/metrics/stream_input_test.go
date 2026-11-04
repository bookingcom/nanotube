package metrics

import (
	"sync"
	"testing"
)

func TestStreamInputReservations(t *testing.T) {
	m := New()
	var wg sync.WaitGroup
	for range 100 {
		wg.Go(func() {
			if m.ReserveStreamInput(80, 160) {
				m.ReleaseStreamInput(80)
			}
		})
	}
	wg.Wait()
	if !m.ReserveStreamInput(160, 160) || m.ReserveStreamInput(1, 160) {
		t.Fatal("global limit not enforced")
	}
	m.ReleaseStreamInput(160)
	if m.streamInputReserved.Load() != 0 {
		t.Fatal("reservation leak")
	}
}
