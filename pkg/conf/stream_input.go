package conf

// StreamInputDictionaryLimit bounds the shared decoder's allocated dictionary.
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

// StreamInputTotalLimit bounds aggregate dictionary/route reservations. It does
// not allocate eagerly or include queues, frame buffers and compression contexts.
func (c Main) StreamInputTotalLimit() int {
	if c.StreamInputTotalBytes == 0 {
		return 3 << 30
	}
	return c.StreamInputTotalBytes
}
