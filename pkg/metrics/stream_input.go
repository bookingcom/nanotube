package metrics

// ReserveStreamInput admits a connection only if both of its full dictionary
// and route allowances fit. Reservation happens before constructing a decoder.
func (m *Prom) ReserveStreamInput(n, limit int64) bool {
	for {
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

// ReleaseStreamInput releases a closed input's admission reservation.
func (m *Prom) ReleaseStreamInput(n int64) {
	m.streamInputReserved.Add(-n)
	m.StreamInputReservedBytes.Sub(float64(n))
}
