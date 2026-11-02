package codec

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"sync"
	"testing"
)

func TestDecoderRejectsBufferGrowthBeforeAllocation(t *testing.T) {
	e := encoder(t, Options{Flags: FlagZstd})
	add(t, e, "example.one", 1, 2)
	wire := flush(t, e, nil)
	d := decoder(t, Options{Flags: FlagZstd})
	d.SetBufferBudget(func(n, _ int) bool { return n <= HeaderSize })
	if _, err := d.ReadFrame(bytes.NewReader(wire), nil); !errors.Is(err, ErrLimit) {
		t.Fatalf("buffer growth: %v", err)
	}
	if cap(d.wire) != HeaderSize || cap(d.raw)+cap(d.rows)+cap(d.definitions) != 0 {
		t.Fatal("rejected buffer growth allocated capacity")
	}
}

func TestDecoderBufferBudgetKeepsOwnedCapacityAndResetReleases(t *testing.T) {
	e := encoder(t, Options{Flags: FlagZstd})
	for i := range 100 {
		add(t, e, string(bytes.Repeat([]byte{byte('a' + i%26)}, 100))+string(rune(i+256)), 1, 2)
	}
	large := flush(t, e, nil)
	add(t, e, string(bytes.Repeat([]byte{'a'}, 100))+string(rune(256)), 1, 2)
	small := flush(t, e, nil)
	d := decoder(t, Options{Flags: FlagZstd})
	charged := 0
	d.SetBufferBudget(func(n, _ int) bool { charged = n; return true })
	if _, err := d.ReadFrame(bytes.NewReader(large), nil); err != nil {
		t.Fatal(err)
	}
	capacity := cap(d.raw)
	if _, err := d.ReadFrame(bytes.NewReader(small), nil); err != nil {
		t.Fatal(err)
	}
	if cap(d.raw) != capacity || charged != cap(d.wire)+cap(d.raw)+cap(d.rows)*24+cap(d.definitions)*24 {
		t.Fatal("small frame hid an owned allocation from admission")
	}
	if err := e.Reset(); err != nil {
		t.Fatal(err)
	}
	reset := flush(t, e, nil)
	if _, err := d.ReadFrame(bytes.NewReader(reset), nil); err != nil {
		t.Fatal(err)
	}
	if charged != HeaderSize || cap(d.raw)+cap(d.rows)+cap(d.definitions) != 0 {
		t.Fatal("reset retained frame buffers")
	}
}

func TestDecoderAdmitsCapacityBeforeGrowthAndReleasesOnReset(t *testing.T) {
	e := encoder(t, Options{})
	add(t, e, "example.one", 1, 2)
	wire := flush(t, e, nil)
	d := decoder(t, Options{})
	d.SetNameResolver(func(name []byte) any { return &testNameReference{name: append([]byte(nil), name...)} })
	calls := 0
	d.SetDictionaryBudget(func(n int) bool {
		calls++
		if d.RetainedBytes() != 0 || n != 16*40 {
			t.Fatalf("admission after allocation: retained=%d wanted=%d", d.RetainedBytes(), n)
		}
		return false
	})
	rows := 0
	if _, err := d.Decode(wire, func(uint32, []byte, float64, int64, *any) { rows++ }); !errors.Is(err, ErrLimit) || calls != 1 || rows != 0 || d.RetainedBytes() != 0 {
		t.Fatalf("failed growth: err=%v calls=%d rows=%d retained=%d", err, calls, rows, d.RetainedBytes())
	}
	d = decoder(t, Options{})
	d.SetNameResolver(func(name []byte) any { return &testNameReference{name: append([]byte(nil), name...)} })
	reserved := 0
	d.SetDictionaryBudget(func(n int) bool { reserved = n; return true })
	decodeAll(t, d, wire, nil)
	if reserved != d.RetainedBytes() || reserved == 0 {
		t.Fatal("capacity was not charged")
	}
	if err := e.Reset(); err != nil {
		t.Fatal(err)
	}
	decodeAll(t, d, flush(t, e, nil), nil)
	if reserved != 0 || d.RetainedBytes() != 0 {
		t.Fatal("reset retained capacity or reservation")
	}
}

type testNameReference struct{ name []byte }

func (n *testNameReference) StreamName() []byte { return n.name }

func TestDecoderPoolConcurrentAndRejectedFrames(t *testing.T) {
	e := encoder(t, Options{Flags: FlagZstd})
	add(t, e, "example.one", 1, 2)
	wire := flush(t, e, nil)
	p := NewDecoderPool(DefaultMaxFrameSize, 1)
	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			for range 10 {
				d, err := NewDecoder(Options{Flags: FlagZstd})
				if err != nil {
					t.Error(err)
					return
				}
				d.SetDecoderPool(p)
				rows := 0
				if _, err := d.Decode(wire, func(uint32, []byte, float64, int64, *any) { rows++ }); err != nil || rows != 1 || d.z != nil {
					t.Errorf("pooled decode err=%v rows=%d private=%v", err, rows, d.z != nil)
				}
				d.Close()
				bad := append([]byte(nil), wire...)
				// Pass framing/CRC validation but fail inside the pooled decoder.
				bad[HeaderSize] ^= 1
				binary.BigEndian.PutUint32(bad[16:20], crc32.Checksum(bad[HeaderSize:], castagnoli))
				d, _ = NewDecoder(Options{Flags: FlagZstd})
				d.SetDecoderPool(p)
				if _, err := d.Decode(bad, nil); err == nil {
					t.Error("bad compressed frame accepted")
				}
				d.Close()
			}
		})
	}
	wg.Wait()
	if len(p.slots) != 1 {
		t.Fatal("context token leaked")
	}
}
