package target

import (
	"bytes"
	"io"
)

// recordBuffer owns complete serialized records until a socket write succeeds.
// On an errored/short write it retains the entire buffer for replay on a fresh
// connection. The peer may already have received the prefix: Graphite TCP has
// no acknowledgement, so replay can duplicate records but never starts with a
// record fragment. Its size is bounded by the configured buffer or one record.
type recordBuffer struct {
	data    []byte
	records int
	size    int
}

type tcpWriteResult struct {
	records          int
	bytes            int
	written          int
	uncertainRecords int
	err              error
}

func newRecordBuffer(size int) *recordBuffer {
	if size <= 0 {
		size = 4096 // same default as bufio.NewWriterSize
	}
	return &recordBuffer{size: size}
}

func (b *recordBuffer) Buffered() int { return len(b.data) }

func (b *recordBuffer) fits(n int) bool { return len(b.data)+n <= b.size }

func (b *recordBuffer) appendRecord(record []byte) {
	if b.data == nil {
		b.data = make([]byte, 0, max(b.size, len(record)))
	}
	b.data = append(b.data, record...)
	b.records++
}

func (b *recordBuffer) flush(w io.Writer) tcpWriteResult {
	if len(b.data) == 0 {
		return tcpWriteResult{}
	}
	n, err := w.Write(b.data)
	if n < 0 || n > len(b.data) {
		n, err = 0, io.ErrShortWrite
	}
	if n < len(b.data) && err == nil {
		err = io.ErrShortWrite
	}
	r := tcpWriteResult{written: n, err: err}
	if err != nil {
		// Only the prefix reported written might be duplicated by replay.
		r.uncertainRecords = bytes.Count(b.data[:n], []byte{'\n'})
		if n > 0 && b.data[n-1] != '\n' {
			r.uncertainRecords++
		}
		return r
	}
	r.records, r.bytes = b.records, len(b.data)
	b.data, b.records = b.data[:0], 0
	return r
}
