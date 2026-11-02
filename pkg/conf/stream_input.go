package conf

// StreamInputDictionaryLimit bounds decoder capacity plus a connection's logical
// charge for the shared names and index entries its ids can retain.
func (c Main) StreamInputDictionaryLimit() int {
	if c.StreamInputMaxDictionaryBytes == 0 {
		return 64 << 20
	}
	return c.StreamInputMaxDictionaryBytes
}

// StreamInputRouteLimit bounds cached plans independently of decoding.
func (c Main) StreamInputRouteLimit() int {
	if c.StreamInputMaxRouteBytes == 0 {
		return 128 << 20
	}
	return c.StreamInputMaxRouteBytes
}

// StreamInputTotalLimit bounds the fixed shared index and aggregate connection
// dictionary/route reservations. It does
// not allocate eagerly or include queues, frame buffers and compression contexts.
func (c Main) StreamInputTotalLimit() int {
	if c.StreamInputTotalBytes == 0 {
		return 3 << 30
	}
	return c.StreamInputTotalBytes
}
