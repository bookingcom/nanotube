package codec

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"math"
	"reflect"
	"testing"
)

type testNameBinder struct {
	refs     map[*BoundName]bool
	bound    int
	released int
	reject   int
}

func (b *testNameBinder) BindStreamName(name []byte) (*BoundName, bool, error) {
	b.bound++
	if b.bound == b.reject {
		return nil, false, ErrLimit
	}
	ref := &BoundName{Text: string(name)}
	cached := len(name)%2 == 0
	if b.refs == nil {
		b.refs = make(map[*BoundName]bool)
	}
	b.refs[ref] = cached
	return ref, cached, nil
}

func (b *testNameBinder) ReleaseStreamName(ref *BoundName, cached bool) {
	if want, ok := b.refs[ref]; !ok || want != cached {
		panic("released an unbound reference or changed its caller state")
	}
	delete(b.refs, ref)
	b.released++
}

func readBoundAll(t *testing.T, d *Decoder, wire []byte, visit BoundVisit) {
	t.Helper()
	r := bytes.NewReader(wire)
	for r.Len() > 0 {
		if _, err := d.ReadBoundFrame(r, visit); err != nil {
			t.Fatal(err)
		}
	}
}

func TestBoundDecoderMatchesOrdinaryAndSnapshots(t *testing.T) {
	for _, flags := range []byte{0, FlagZstd} {
		e := encoder(t, Options{Flags: flags, MaxRows: 400})
		d := decoder(t, Options{Flags: flags})
		ordinary := decoder(t, Options{Flags: flags})
		binder := &testNameBinder{}
		d.SetNameBinder(binder)
		var got, want []observation
		visit := func(ref *BoundName, cached bool, v float64, ts int64) {
			if binder.refs[ref] != cached {
				t.Fatal("caller state changed")
			}
			got = append(got, observation{ref.Text, ts, math.Float64bits(v)})
		}
		for epoch := range 3 {
			if err := e.Reset(); err != nil {
				t.Fatal(err)
			}
			for i := range 5000 {
				add(t, e, fmt.Sprintf("name.%d", (i*37)%1025), math.Float64frombits(uint64(i)*0x9e3779b97f4a7c15), int64(i*7-epoch))
			}
			wire := flush(t, e, nil)
			decodeAll(t, ordinary, wire, observe(&want))
			readBoundAll(t, d, wire, visit)
			if !reflect.DeepEqual(got, want) {
				t.Fatal("compact predictors changed decoded records")
			}
			snapshot, err := d.Snapshot(nil)
			if err != nil {
				t.Fatal(err)
			}
			reference, err := ordinary.Snapshot(nil)
			if err != nil || !bytes.Equal(snapshot, reference) {
				t.Fatal("snapshot differs from ordinary decoder")
			}
			next := decoder(t, Options{Flags: flags})
			nextBinder := &testNameBinder{}
			next.SetNameBinder(nextBinder)
			readBoundAll(t, next, snapshot, nil)
			add(t, e, "name.37", -1.25, math.MinInt64)
			wire = flush(t, e, nil)
			var restored []observation
			readBoundAll(t, next, wire, func(ref *BoundName, _ bool, v float64, ts int64) {
				restored = append(restored, observation{ref.Text, ts, math.Float64bits(v)})
			})
			readBoundAll(t, d, wire, visit)
			decodeAll(t, ordinary, wire, observe(&want))
			if !reflect.DeepEqual(restored, got[len(got)-1:]) {
				t.Fatal("snapshot failed to restore compact predictors")
			}
			next.Close()
			if len(nextBinder.refs) != 0 {
				t.Fatal("snapshot references leaked")
			}
		}
		d.Close()
		d.Close()
		if len(binder.refs) != 0 || binder.bound != binder.released {
			t.Fatal("reset/close references leaked")
		}
	}
}

func TestBoundDecoderValidationAndAdmission(t *testing.T) {
	for _, mode := range []string{"crc", "column", "admission", "limit"} {
		t.Run(mode, func(t *testing.T) {
			e := encoder(t, Options{})
			for i := range 130 {
				add(t, e, fmt.Sprintf("name.%d", i), 1, 2)
			}
			wire := flush(t, e, nil)
			binder := &testNameBinder{}
			options := Options{}
			switch mode {
			case "crc":
				wire[len(wire)-1] ^= 1
			case "column":
				wire = append(wire, 0xff)
				binary.BigEndian.PutUint32(wire[4:8], uint32(len(wire)-HeaderSize))
				binary.BigEndian.PutUint32(wire[8:12], uint32(len(wire)-HeaderSize))
				binary.BigEndian.PutUint32(wire[16:20], crc32.Checksum(wire[HeaderSize:], castagnoli))
			case "admission":
				binder.reject = 129 // Cross a page boundary before partial admission.
			case "limit":
				options.DictionaryBytes = 4096
			}
			d := decoder(t, options)
			d.SetNameBinder(binder)
			visited := 0
			_, err := d.DecodeBound(wire, func(*BoundName, bool, float64, int64) { visited++ })
			if err == nil || visited != 0 {
				t.Fatal("rejected frame visited rows")
			}
			if mode != "admission" && binder.bound != 0 {
				t.Fatal("invalid frame bound definitions")
			}
			d.Close()
			if len(binder.refs) != 0 {
				t.Fatal("partial admission leaked references")
			}
		})
	}
}

func TestBoundStateWithoutPredictor(t *testing.T) {
	raw := []byte{0, 2, 3, 'o', 'n', 'e', 0, 3, 't', 'w', 'o', 0}
	wire, err := appendFrame(nil, raw, StateFrame, 0, nil, 4096)
	if err != nil {
		t.Fatal(err)
	}
	d := decoder(t, Options{})
	d.SetNameBinder(&testNameBinder{})
	readBoundAll(t, d, wire, nil)
	ordinary := decoder(t, Options{})
	decodeAll(t, ordinary, wire, nil)
	a, err := d.Snapshot(nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := ordinary.Snapshot(nil)
	if err != nil || !bytes.Equal(a, b) {
		t.Fatal("uninitialized predictor state changed")
	}
}
