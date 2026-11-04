package codec

import (
	"bytes"
	"fmt"
	"io"
	"math/rand/v2"
	"sync"
	"testing"

	"github.com/klauspost/compress/zstd"
)

// A shared workspace may previously have compressed a different connection.
// Independent frames must remain identical to the previous private encoder.
func TestSharedFrameCompression(t *testing.T) {
	shared, err := frameEncoder()
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for worker := range 8 {
		wg.Go(func() {
			private, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedFastest), zstd.WithEncoderConcurrency(1), zstd.WithWindowSize(1<<20))
			if err != nil {
				t.Error(err)
				return
			}
			defer func() { _ = private.Close() }()
			rng := rand.New(rand.NewPCG(uint64(worker), 19))
			for _, size := range []int{1, 4096, 64 << 10, 128 << 10, 256 << 10, 1 << 20, 2 << 20, 8192} {
				raw := make([]byte, size)
				for i := range raw {
					if i%128 < 16 {
						raw[i] = byte(rng.Uint32())
					} else {
						raw[i] = byte(i%127 + worker)
					}
				}
				if !bytes.Equal(shared.EncodeAll(raw, nil), private.EncodeAll(raw, nil)) {
					t.Errorf("shared compression differs at size %d", size)
				}
			}
		})
	}
	wg.Wait()
}

func TestSharedScratchConcurrentRecordsAndResets(t *testing.T) {
	var wg sync.WaitGroup
	for worker := range 40 {
		wg.Go(func() {
			options := Options{Flags: FlagZstd, DictionaryBytes: 1 << 20, MaxRows: 129, FrameSize: 16 << 10}
			e, err := NewEncoder(options)
			if err != nil {
				t.Error(err)
				return
			}
			defer e.Close()
			d, err := NewDecoder(options)
			if err != nil {
				t.Error(err)
				return
			}
			defer d.Close()
			const total = 3000
			seen := make([]bool, total)
			var wire []byte
			verify := func() error {
				wire, err = e.Flush(wire[:0])
				if err != nil {
					return err
				}
				r := bytes.NewReader(wire)
				for r.Len() > 0 {
					_, err := d.ReadFrame(r, func(_ uint32, name []byte, value float64, ts int64, _ *any) {
						i := int(ts / 37)
						if ts%37 != 0 || i < 0 || i >= total || seen[i] || value != float64(i)+float64(worker)/8 || string(name) != fmt.Sprintf("worker%d.%03d", worker, i%257) {
							t.Error("shared scratch changed, lost or duplicated a record")
							return
						}
						seen[i] = true
					})
					if err != nil && err != io.EOF {
						return err
					}
				}
				return nil
			}
			for i := range total {
				if i > 0 && i%511 == 0 {
					if err := e.Reset(); err != nil {
						t.Error(err)
						return
					}
				}
				if err := e.Add(fmt.Appendf(nil, "worker%d.%03d", worker, i%257), float64(i)+float64(worker)/8, int64(i)*37); err != nil {
					t.Error(err)
					return
				}
				if e.Ready() {
					if err := verify(); err != nil {
						t.Error(err)
						return
					}
				}
			}
			if err := verify(); err != nil {
				t.Error(err)
			}
			for _, ok := range seen {
				if !ok {
					t.Error("shared scratch lost a record")
					return
				}
			}
		})
	}
	wg.Wait()
}
