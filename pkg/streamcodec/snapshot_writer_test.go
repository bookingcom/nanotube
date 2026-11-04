package codec

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strconv"
	"testing"
)

type snapshotWriter struct {
	bytes.Buffer
	maxWrite int
	short    bool
}

func BenchmarkSnapshotWriter(b *testing.B) {
	count, _ := strconv.Atoi(os.Getenv("STREAM_SNAPSHOT_NAMES"))
	if count == 0 {
		count = 65536
	}
	for _, flags := range []byte{0, FlagZstd} {
		e := encoder(b, Options{Flags: flags, DictionaryBytes: 128 << 20, PeerDictionaryBytes: 256 << 20})
		for i := range count {
			add(b, e, fmt.Sprintf("example.snapshot.%080d", i), float64(i), int64(i))
		}
		d := decoder(b, Options{Flags: flags, DictionaryBytes: 256 << 20})
		decodeAll(b, d, flush(b, e, nil), nil)
		for _, streamed := range []bool{false, true} {
			b.Run(fmt.Sprintf("flags%d/streamed%t", flags, streamed), func(b *testing.B) {
				b.ReportAllocs()
				for range b.N {
					if streamed {
						if err := d.WriteSnapshot(io.Discard); err != nil {
							b.Fatal(err)
						}
					} else {
						wire, err := d.Snapshot(nil)
						if err != nil {
							b.Fatal(err)
						}
						runtime.KeepAlive(wire)
					}
				}
			})
		}
	}
}

func (w *snapshotWriter) Write(b []byte) (int, error) {
	w.maxWrite = max(w.maxWrite, len(b))
	if w.short {
		return 0, nil
	}
	return w.Buffer.Write(b)
}

func TestSnapshotWriterMatchesBufferedSnapshot(t *testing.T) {
	for _, flags := range []byte{0, FlagZstd} {
		for _, compact := range []bool{false, true} {
			e := encoder(t, Options{Flags: flags})
			for i := range 10000 {
				add(t, e, fmt.Sprintf("example.snapshot.%080d", i), float64(i)/8, int64(i))
			}
			d := decoder(t, Options{Flags: flags, FrameSize: 4096})
			wire := flush(t, e, nil)
			if compact {
				d.SetNameBinder(&testNameBinder{})
				readBoundAll(t, d, wire, nil)
			} else {
				decodeAll(t, d, wire, nil)
			}
			want, err := d.Snapshot(nil)
			if err != nil {
				t.Fatal(err)
			}
			w := &snapshotWriter{}
			if err := d.WriteSnapshot(w); err != nil || !bytes.Equal(w.Bytes(), want) {
				t.Fatal("streaming snapshot differs")
			}
			if w.maxWrite > 8192 {
				t.Fatal("snapshot retained more than one bounded frame")
			}
			if err := d.WriteSnapshot(&snapshotWriter{short: true}); !errors.Is(err, io.ErrShortWrite) {
				t.Fatal("short snapshot write succeeded")
			}
		}
	}
}
