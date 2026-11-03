//go:build unix

package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/gob"
	"os"
	"syscall"
	"testing"
)

type relayFixtureArchive struct {
	Names, Cycles int
	Zero, One     relayFixture
}

// Producer bytes belong outside the consumer's Go heap. Mapping a fixed base-
// generated archive avoids adding hundreds of MiB of fixture-only GC headroom.
func readRelayFixture(t testing.TB, path string) (relayFixtureArchive, func()) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var result relayFixtureArchive
	if os.Getenv("RELAY_MODEL_LEGACY_GOB") == "1" {
		if err := gob.NewDecoder(f).Decode(&result); err != nil {
			t.Fatal(err)
		}
		return result, func() {}
	}
	stat, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	data, err := syscall.Mmap(int(f.Fd()), 0, int(stat.Size()), syscall.PROT_READ, syscall.MAP_SHARED)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) < 8 || string(data[:8]) != "NTRLY001" {
		t.Fatal("bad fixture header")
	}
	position := 8
	read := func() []byte {
		if position > len(data)-8 {
			t.Fatal("truncated fixture length")
		}
		n := binary.BigEndian.Uint64(data[position:])
		position += 8
		if n > uint64(len(data)-position) {
			t.Fatal("truncated fixture bytes")
		}
		b := data[position : position+int(n)]
		position += int(n)
		return b
	}
	if err := gob.NewDecoder(bytes.NewReader(read())).Decode(&result); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []*relayFixture{&result.Zero, &result.One} {
		for connection := range 80 {
			fixture.Initial[connection] = read()
			fixture.Frames[connection] = make([][]byte, len(fixture.CycleExpected))
			for cycle := range fixture.Frames[connection] {
				fixture.Frames[connection][cycle] = read()
			}
		}
	}
	if position != len(data) {
		t.Fatal("trailing fixture bytes")
	}
	return result, func() {
		if err := syscall.Munmap(data); err != nil {
			t.Error(err)
		}
	}
}

func writeRelayFixture(t testing.TB, path string, input relayFixtureArchive) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w := bufio.NewWriterSize(f, 1<<20)
	if _, err := w.WriteString("NTRLY001"); err != nil {
		t.Fatal(err)
	}
	write := func(b []byte) {
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len(b)))
		if _, err := w.Write(size[:]); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(b); err != nil {
			t.Fatal(err)
		}
	}
	metadata := input
	metadata.Zero.Initial, metadata.One.Initial = [80][]byte{}, [80][]byte{}
	metadata.Zero.Frames, metadata.One.Frames = [80][][]byte{}, [80][][]byte{}
	var header bytes.Buffer
	if err := gob.NewEncoder(&header).Encode(metadata); err != nil {
		t.Fatal(err)
	}
	write(header.Bytes())
	for _, fixture := range []*relayFixture{&input.Zero, &input.One} {
		for connection := range 80 {
			write(fixture.Initial[connection])
			for _, frame := range fixture.Frames[connection] {
				write(frame)
			}
		}
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
}
