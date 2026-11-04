// Package rec includes everything related to datapoint record.
package rec

import (
	"bytes"
	"encoding/binary"
	"hash/fnv"
	"math"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/pkg/errors"
	"go.uber.org/zap"
)

// RecBytes represents a single piece of data (a datapoint) that can be sent.
type RecBytes struct { // nolint:revive
	streamToken       uint64 // first for atomic alignment on 32-bit platforms
	streamTokenSource *uint64
	Path              []byte
	Val               []byte
	Time              []byte
	typed             bool
	numericBytes      uint8 // canonical float + int text is at most 44 bytes
	value             float64
	timestamp         int64

	Received time.Time

	// line holds "Path Val Time\n" in a single allocation made by ParseRec.
	// Path, Val and Time alias it for as long as they are not reassigned.
	line []byte

	pathHash    uint64
	pathHashFor *byte
	pathHashLen int
}

// ParseRec parses a single datapoint record from a string. Makes sure it's valid.
// Performs normalizations.
func ParseRec(s []byte, normalize bool, shouldLog bool, nowF func() time.Time, lg *zap.Logger) (*RecBytes, error) {
	return ParseRecWithPath(s, normalize, nowF, nil)
}

// ParseRecWithPath validates all fields as ParseRec does. resolve may supply a
// previously normalized path and its FNV-1a hash. The supplied bytes are copied;
// neither the input nor cache storage is retained by the returned record.
func ParseRecWithPath(s []byte, normalize bool, nowF func() time.Time, resolve func([]byte) ([]byte, uint64, bool)) (*RecBytes, error) {
	pathStart, pathEnd, valStart, valEnd, timeStart, timeEnd, err := recFields(s)
	if err != nil {
		return nil, errors.Wrap(err, "failed to break record into fields")
	}

	val := s[valStart:valEnd]
	tm := s[timeStart:timeEnd]

	// The whole serialized record is built in one allocation; Path, Val and
	// Time are capped sub-slices of it so appends to them cannot clobber it.
	line := make([]byte, 0, (pathEnd-pathStart)+len(val)+len(tm)+3)
	var path []byte
	var hash uint64
	var cached bool
	if resolve != nil {
		path, hash, cached = resolve(s[pathStart:pathEnd])
	}
	if cached {
		line = append(line, path...)
	} else if normalize {
		line, err = appendNormalizedPath(line, s[pathStart:pathEnd])
		if err != nil {
			return nil, errors.Wrap(err, "failed to normalize path")
		}
	} else {
		line = append(line, s[pathStart:pathEnd]...)
	}
	pathLen := len(line)
	line = append(line, ' ')
	line = append(line, val...)
	line = append(line, ' ')
	line = append(line, tm...)
	line = append(line, '\n')

	valOff := pathLen + 1
	timeOff := valOff + len(val) + 1

	r := &RecBytes{
		Path:     line[:pathLen:pathLen],
		Val:      line[valOff : valOff+len(val) : valOff+len(val)],
		Time:     line[timeOff : timeOff+len(tm) : timeOff+len(tm)],
		Received: nowF(),
		line:     line,
	}
	if cached {
		r.SetPathHash(hash)
	}
	return r, nil
}

// SetPathHash seeds PathHash for the current Path. The caller must supply its
// exact 64-bit FNV-1a hash. Reassigning Path still invalidates this value.
func (r *RecBytes) SetPathHash(hash uint64) {
	r.streamTokenSource = nil
	atomic.StoreUint64(&r.streamToken, 0)
	r.pathHash, r.pathHashLen = hash, len(r.Path)
	r.pathHashFor = nil
	if len(r.Path) > 0 {
		r.pathHashFor = &r.Path[0]
	}
}

func recFields(s []byte) (pathStart, pathEnd, valStart, valEnd, timeStart, timeEnd int, retErr error) {
	pathStart, pathEnd, err := getField(s, 0)
	if err != nil {
		retErr = errors.Wrap(err, "failed to find path in record")
		return
	}

	valStart, valEnd, err = getField(s, pathEnd)
	if err != nil {
		retErr = errors.Wrap(err, "failed to find value in record")
		return
	}

	timeStart, timeEnd, err = getField(s, valEnd)
	if err != nil {
		retErr = errors.Wrap(err, "failed to find time in record")
		return
	}

	for i := timeEnd; i < len(s); i++ {
		if !isWhitespace(s[i]) {
			retErr = errors.New("record has additional characters after 3 fields")
			return
		}
	}

	return
}

func getField(s []byte, st int) (int, int, error) {
	if st == len(s) {
		return st, st, errors.New("start point beyond the string end")
	}

	start := st
	for ; start < len(s) && isWhitespace(s[start]); start++ {
	}
	if start == len(s) {
		return st, st, errors.New("string contains only whitespace")
	}

	return start, fieldEnd(s, start+1), nil
}

// fieldEnd returns the index of the first whitespace at or after i, or len(s).
func fieldEnd(s []byte, i int) int {
	rest := s[i:]
	n := bytes.IndexByte(rest, ' ')
	if n < 0 {
		n = len(rest)
	}
	if t := bytes.IndexByte(rest[:n], '\t'); t >= 0 {
		n = t
	}
	return i + n
}

func isWhitespace(c byte) bool {
	return c == byte(' ') || c == byte('\t')
}

// NewTyped borrows an immutable, owned routing-plan path. That path must remain
// alive while records are queued; a Go slice retains it across dictionary resets.
func NewTyped(path []byte, value float64, timestamp int64) *RecBytes {
	var scratch [64]byte
	b := strconv.AppendFloat(scratch[:0], value, 'g', -1, 64)
	b = strconv.AppendInt(b, timestamp, 10)
	return &RecBytes{Path: path, typed: true, value: value, timestamp: timestamp, numericBytes: uint8(len(b))}
}

// TypedValues exposes numeric input without altering legacy textual consumers.
func (r *RecBytes) TypedValues() (float64, int64, bool) { return r.value, r.timestamp, r.typed }

// Values avoids decimal parsing for typed input and matches the stream output's
// legacy numeric validation for textual records.
func (r *RecBytes) Values() (float64, int64, bool) {
	if r.typed {
		return r.value, r.timestamp, !math.IsNaN(r.value)
	}
	v, err := strconv.ParseFloat(string(r.Val), 64)
	if err != nil || math.IsNaN(v) {
		return 0, 0, false
	}
	ts, err := strconv.ParseFloat(string(r.Time), 64)
	if err != nil || math.IsNaN(ts) {
		return 0, 0, false
	}
	return v, int64(ts), true
}

// PlainSize is the equivalent canonical plaintext size, including newline.
func (r *RecBytes) PlainSize() int {
	if r.typed {
		return len(r.Path) + int(r.numericBytes) + 3
	}
	return len(r.Path) + len(r.Val) + len(r.Time) + 3
}

// NormalizedPath borrows immutable input when no bytes need changing. The caller
// must own that input independently of any decoder arena reused on reset.
func NormalizedPath(path []byte, normalize bool) ([]byte, error) {
	if !normalize {
		return path, nil
	}
	start, end, ok := trimDots(path)
	if !ok {
		return nil, errors.New("path contains only dots")
	}
	if !needsNormalization(path, start, end) {
		return path[start : end+1 : end+1], nil
	}
	return appendNormalizedPath(nil, path)
}

// Serialize makes a record ready for plaintext TCP, including typed records.
func (r *RecBytes) Serialize() []byte {
	if r.typed {
		b := make([]byte, 0, r.PlainSize())
		b = append(b, r.Path...)
		b = append(b, ' ')
		b = strconv.AppendFloat(b, r.value, 'g', -1, 64)
		b = append(b, ' ')
		b = strconv.AppendInt(b, r.timestamp, 10)
		return append(b, '\n')
	}
	res := make([]byte, 0, len(r.Path)+len(r.Time)+len(r.Val)+3)
	res = append(res, r.Path...)
	res = append(res, ' ')
	res = append(res, r.Val...)
	res = append(res, ' ')
	res = append(res, r.Time...)
	res = append(res, '\n')

	return res
}

// Bytes returns the same bytes as Serialize. While the record still holds the
// buffer built by ParseRec it is returned without copying; the result is
// shared and must not be modified.
func (r *RecBytes) Bytes() []byte {
	if r.lineIsCurrent() {
		return r.line
	}
	return r.Serialize()
}

// lineIsCurrent reports whether Path, Val and Time still point exactly at
// their places in line, i.e. line is their serialization.
func (r *RecBytes) lineIsCurrent() bool {
	pl, vl, tl := len(r.Path), len(r.Val), len(r.Time)
	return pl > 0 && vl > 0 && tl > 0 && len(r.line) == pl+vl+tl+3 &&
		&r.line[0] == &r.Path[0] &&
		&r.line[pl+1] == &r.Val[0] &&
		&r.line[pl+vl+2] == &r.Time[0]
}

// PathHash returns the 64-bit FNV-1a hash of Path. It is computed once and
// cached until Path is reassigned.
func (r *RecBytes) PathHash() uint64 {
	if len(r.Path) == 0 || len(r.Path) != r.pathHashLen || &r.Path[0] != r.pathHashFor {
		h := fnv.New64a()
		_, _ = h.Write(r.Path)
		r.SetPathHash(h.Sum64())
	}
	return r.pathHash
}

// trimDots returns the bounds of s without leading and trailing dots.
func trimDots(s []byte) (start int, end int, ok bool) {
	for ; start < len(s) && s[start] == '.'; start++ {
	}
	if start == len(s) {
		return 0, 0, false
	}

	end = len(s) - 1 // points to the last non-. char in path
	for ; end >= start && s[end] == '.'; end-- {
	}

	return start, end, true
}

func needsNormalization(s []byte, start, end int) bool {
	path := s[start : end+1]
	// Classify two bytes per lookup, and find adjacent dots a word at a time.
	// Overlap words by a byte so pairs spanning words are checked too.
	for len(path) >= 8 {
		w := binary.LittleEndian.Uint64(path)
		bad := invalidPairs[uint16(w)] | invalidPairs[uint16(w>>16)] |
			invalidPairs[uint16(w>>32)] | invalidPairs[uint16(w>>48)]
		x := w ^ 0x2e2e2e2e2e2e2e2e
		dots := (x - 0x0101010101010101) &^ x & 0x8080808080808080
		// The zero-byte trick can also mark '/' following '.', but '/' is
		// invalid, so that case must take the slow path anyway.
		if bad != 0 || dots&(dots>>8) != 0 {
			return true
		}
		path = path[7:]
	}
	for i, c := range path {
		if charClasses[c] == charInvalid || c == '.' && i+1 < len(path) && path[i+1] == '.' {
			return true
		}
	}
	return false
}

var invalidPairs = func() (t [65536]uint8) { //nolint:gochecknoglobals // read-only byte-pair classification
	for i := range t {
		if !validChar(byte(i)) || !validChar(byte(i>>8)) {
			t[i] = 1
		}
	}
	return t
}()

const (
	charInvalid = iota
	charValid
	charDot
)

// charClasses classifies every byte as validChar does, with '.' separated out.
var charClasses = func() (t [256]uint8) { //nolint:gochecknoglobals // read-only lookup table
	for c := range t {
		if validChar(byte(c)) {
			t[c] = charValid
		}
	}
	t['.'] = charDot
	return t
}()

// appendNormalizedPath appends the normalized form of s to dst. Path
// normalization is described in the docs.
func appendNormalizedPath(dst []byte, s []byte) ([]byte, error) {
	if len(s) == 0 {
		return dst, nil
	}

	start, end, ok := trimDots(s)
	if !ok {
		return dst, errors.New("path contains only dots")
	}

	if !needsNormalization(s, start, end) {
		return append(dst, s[start:end+1]...), nil
	}

	for i := start; i <= end; i++ {
		if s[i] == '.' {
			if i == len(s)-1 {
				continue
			} else if s[i+1] == '.' {
				continue
			}
		}

		if charClasses[s[i]] != charInvalid {
			dst = append(dst, s[i])
		} else {
			dst = append(dst, byte('_'))
		}
	}

	return dst, nil
}

// Copy returns a deep copy of the record
func (r RecBytes) Copy() (*RecBytes, error) {
	cpy := &RecBytes{
		typed: r.typed, value: r.value, timestamp: r.timestamp, numericBytes: r.numericBytes,
		Received: r.Received,
		Path:     make([]byte, len(r.Path)),
		Val:      make([]byte, len(r.Val)),
		Time:     make([]byte, len(r.Time)),
	}

	n := copy(cpy.Path, r.Path)
	if n != len(r.Path) {
		return nil, errors.Errorf("did not copy full path, expected %d bytes, copied %d bytes", len(r.Path), n)
	}
	n = copy(cpy.Val, r.Val)
	if n != len(r.Val) {
		return nil, errors.Errorf("did not copy full value, expected %d bytes, copied %d bytes", len(r.Val), n)
	}
	n = copy(cpy.Time, r.Time)
	if n != len(r.Time) {
		return nil, errors.Errorf("did not copy full time, expected %d bytes, copied %d bytes", len(r.Time), n)
	}

	return cpy, nil
}

func validChar(c byte) bool {
	if c >= 'A' && c <= 'Z' {
		return true
	}

	if c >= 'a' && c <= 'z' {
		return true
	}

	if c >= '0' && c <= '9' {
		return true
	}

	if c == ':' || c == '_' || c == '-' || c == '#' || c == '.' {
		return true
	}

	return false
}

// StreamToken caches a generation-checked name hint across replica outputs.
func (r *RecBytes) StreamToken() uint64 {
	if token := atomic.LoadUint64(&r.streamToken); token != 0 {
		return token
	}
	if r.streamTokenSource != nil {
		return atomic.LoadUint64(r.streamTokenSource)
	}
	return 0
}
func (r *RecBytes) SetStreamToken(token uint64) {
	// A zero-key hint means the table was full. Keep it on this record only:
	// persisting it would prevent future records from retrying admission.
	if r.streamTokenSource != nil && uint32(token) != 0 {
		atomic.StoreUint64(r.streamTokenSource, token)
		return
	}
	atomic.StoreUint64(&r.streamToken, token)
}

// ShareStreamToken reuses the output name hint across records of an immutable
// routing plan. The source must be eight-byte aligned and accessed atomically.
// Generation checks remain the output table's responsibility. SetPathHash
// detaches the hint when a path changes; queued records retain its allocation.
func (r *RecBytes) ShareStreamToken(source *uint64) { r.streamTokenSource = source }
