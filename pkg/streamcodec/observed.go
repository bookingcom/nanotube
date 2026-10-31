package codec

// AddObserved is a counter-only adapter for the relay. It leaves the upstream
// encoder and wire format unchanged and avoids a second name dictionary/lookup.
func (e *Encoder) AddObserved(name []byte, value float64, ts int64) (defined, reset bool, err error) {
	before := len(e.names)
	var first string
	if before == 1 {
		first = e.names[0]
	}
	if err = e.Add(name, value, ts); err != nil {
		return false, false, err
	}
	after := len(e.names)
	reset = before > after || (before == 1 && after == 1 && first != string(name))
	return after > before || reset, reset, nil
}

// NameCount reports the current dictionary population for diagnostics.
func (e *Encoder) NameCount() int { return len(e.names) }
