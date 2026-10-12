// Package rec includes everything related to datapoint record.
package rec

import (
	"hash/fnv"
	"time"

	"github.com/pkg/errors"
	"go.uber.org/zap"
)

// RecBytes represents a single piece of data (a datapoint) that can be sent.
type RecBytes struct { // nolint:revive
	Path []byte
	Val  []byte
	Time []byte

	Received time.Time

	// line holds "Path Val Time\n" in a single allocation made by ParseRec.
	// Path, Val and Time alias it for as long as they are not reassigned.
	line []byte

	pathHash    uint64
	pathHashFor []byte
}

// ParseRec parses a single datapoint record from a string. Makes sure it's valid.
// Performs normalizations.
func ParseRec(s []byte, normalize bool, shouldLog bool, nowF func() time.Time, lg *zap.Logger) (*RecBytes, error) {
	pathStart, pathEnd, valStart, valEnd, timeStart, timeEnd, err := recFields(s)
	if err != nil {
		return nil, errors.Wrap(err, "failed to break record into fields")
	}

	val := s[valStart:valEnd]
	tm := s[timeStart:timeEnd]

	// The whole serialized record is built in one allocation; Path, Val and
	// Time are capped sub-slices of it so appends to them cannot clobber it.
	line := make([]byte, 0, (pathEnd-pathStart)+len(val)+len(tm)+3)
	if normalize {
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

	return &RecBytes{
		Path:     line[:pathLen:pathLen],
		Val:      line[valOff : valOff+len(val) : valOff+len(val)],
		Time:     line[timeOff : timeOff+len(tm) : timeOff+len(tm)],
		Received: nowF(),
		line:     line,
	}, nil
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

	end := start + 1
	for ; end < len(s) && !isWhitespace(s[end]); end++ {
	}

	return start, end, nil
}

func isWhitespace(c byte) bool {
	return c == byte(' ') || c == byte('\t')
}

// Serialize makes record into a string ready to be sent via TCP w/ line protocol.
func (r *RecBytes) Serialize() []byte {
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
	if len(r.Path) == 0 || len(r.Path) != len(r.pathHashFor) || &r.Path[0] != &r.pathHashFor[0] {
		h := fnv.New64a()
		_, _ = h.Write(r.Path)
		r.pathHash = h.Sum64()
		r.pathHashFor = r.Path
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
	for i := start; i <= end; i++ {
		switch charClasses[s[i]] {
		case charInvalid:
			return true
		case charDot:
			if i == len(s)-1 || s[i+1] == '.' {
				return true
			}
		}
	}

	return false
}

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
