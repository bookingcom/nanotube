package target

import (
	"bufio"
	"bytes"
	"fmt"
	"testing"
)

func TestRecordBufferMatchesBufferedWriter(t *testing.T) {
	for _, size := range []int{0, 1, 4096, 65536} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			var want, got bytes.Buffer
			original := bufio.NewWriterSize(&want, size)
			recovered := newRecordBuffer(size)
			for i := 0; i < 1000; i++ {
				// Include records larger than either configured buffer.
				record := append(bytes.Repeat([]byte{'x'}, (i*7919)%70000), []byte(" 1 100\n")...)
				if _, err := original.Write(record); err != nil {
					t.Fatal(err)
				}
				if recovered.Buffered() > 0 && !recovered.fits(len(record)) {
					if r := recovered.flush(&got); r.err != nil {
						t.Fatal(r.err)
					}
				}
				recovered.appendRecord(record)
				if recovered.Buffered() >= recovered.size {
					if r := recovered.flush(&got); r.err != nil {
						t.Fatal(r.err)
					}
				}
			}
			if err := original.Flush(); err != nil {
				t.Fatal(err)
			}
			if r := recovered.flush(&got); r.err != nil {
				t.Fatal(r.err)
			}
			if !bytes.Equal(got.Bytes(), want.Bytes()) {
				t.Fatal("healthy wire output differs from bufio.Writer")
			}
		})
	}
}
