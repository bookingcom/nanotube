package metrics

// ReserveStreamInput admits fixed connection costs and growth of decoder slots,
// shared names, indexes, routes and frame buffers before retaining them. Shared storage is
// charged once, independently of each sender's logical per-connection limits.
func (m *Prom) ReserveStreamInput(n, limit int64) bool {
	for {
		if n < 0 {
			return false
		}
		used := m.streamInputReserved.Load()
		if n > limit-used {
			return false
		}
		if m.streamInputReserved.CompareAndSwap(used, used+n) {
			m.StreamInputReservedBytes.Add(float64(n))
			return true
		}
	}
}

// ReleaseStreamInput returns storage released by RESET, close or handoff.
func (m *Prom) ReleaseStreamInput(n int64) {
	m.streamInputReserved.Add(-n)
	m.StreamInputReservedBytes.Sub(float64(n))
}
