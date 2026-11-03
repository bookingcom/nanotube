package rec

import (
	"bytes"
	"math"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestSharedStreamTokenLifetime(t *testing.T) {
	var token uint64
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 1000 {
				r := NewTyped([]byte("example.shared"), 1, 1700000000)
				r.SetPathHash(123)
				r.ShareStreamToken(&token)
				r.SetStreamToken(uint64(i + 1))
				if r.StreamToken() == 0 {
					t.Error("shared hint was lost")
				}
			}
		}()
	}
	wg.Wait()
	r := NewTyped([]byte("example.shared"), 1, 1700000000)
	r.ShareStreamToken(&token)
	r.Path = []byte("example.changed")
	r.PathHash()
	if r.StreamToken() != 0 {
		t.Fatal("path change retained another name's hint")
	}
	r.SetStreamToken(100)
	if token == 100 {
		t.Fatal("path change did not detach shared hint")
	}
}

func TestStreamHintPreservesTextSerialization(t *testing.T) {
	var token uint64
	plain, err := ParseRec([]byte("example.plain 1.25 1700000000"), false, false, time.Now, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	want := append([]byte(nil), plain.Bytes()...)
	plain.ShareStreamToken(&token)
	plain.SetStreamToken(10)
	if !bytes.Equal(plain.Bytes(), want) {
		t.Fatal("hint overwrote plaintext storage")
	}
	typed := NewTyped([]byte("example.typed"), 1.25, 1700000000)
	typed.ShareStreamToken(&token)
	typed.SetStreamToken(20)
	if !bytes.Equal(typed.Bytes(), typed.Serialize()) {
		t.Fatal("typed bytes exposed hint storage")
	}
	copy, err := typed.Copy()
	if err != nil {
		t.Fatal(err)
	}
	copy.SetStreamToken(30)
	if token != 20 || !bytes.Equal(copy.Bytes(), typed.Bytes()) {
		t.Fatal("copy retained the borrowed hint or changed text")
	}
}

func TestTypedSizeMatchesSerialization(t *testing.T) {
	for _, value := range []float64{0, math.Copysign(0, -1), 1, .125, math.MaxFloat64, math.SmallestNonzeroFloat64, math.Inf(1), math.Inf(-1), math.NaN()} {
		for _, ts := range []int64{0, 1700000000, math.MinInt64, math.MaxInt64} {
			r := NewTyped([]byte("example.size"), value, ts)
			r.PathHash()
			r.Path = []byte("example.changed.length")
			r.PathHash()
			v, stamp, typed := r.TypedValues()
			if !typed || math.Float64bits(v) != math.Float64bits(value) || stamp != ts {
				t.Fatal("path hash invalidation changed typed metadata")
			}
			if r.PlainSize() != len(r.Serialize()) {
				t.Fatalf("size mismatch for %g %d", value, ts)
			}
		}
	}
}

func TestSharedStreamTokenDoesNotCacheFailedAdmission(t *testing.T) {
	var source uint64
	first := NewTyped([]byte("example.full"), 1, 1)
	first.ShareStreamToken(&source)
	first.SetStreamToken(uint64(17) << 32)
	if first.StreamToken() != uint64(17)<<32 {
		t.Fatal("failed hint not retained on current record")
	}
	next := NewTyped([]byte("example.full"), 2, 2)
	next.ShareStreamToken(&source)
	if next.StreamToken() != 0 {
		t.Fatal("failed admission prevents later record from retrying")
	}
}

func TestTextSerializationDuringHashPublication(t *testing.T) {
	// A plaintext target can consume an unhashed literal before a later jump
	// target seeds its hash. The typed flag remains concurrently readable.
	r := &RecBytes{Path: []byte("example.concurrent"), Val: []byte("1.25"), Time: []byte("1700000000")}
	want := r.Serialize()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range 1000 {
			if !bytes.Equal(r.Serialize(), want) {
				t.Error("hash publication changed text")
			}
		}
	}()
	for range 1000 {
		r.SetPathHash(123)
	}
	wg.Wait()
}
