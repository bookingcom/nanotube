// Package rec includes everything related to datapoint record.
package rec

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/errors"
	"go.uber.org/zap"
)

// Rec represents a single piece of data (a datapoint) that can be sent.
type Rec struct {
	Path    string
	Val     float64
	RawVal  string // this is to avoid discrepancies in precision and formatting
	Time    uint32
	RawTime string // to avoid differences when encoding, and save time
	//	Raw  string // to avoid wasting time for serialization
	Received time.Time
}

// ParseRec parses a single datapoint record from a string. Makes sure it's valid.
// Performs normalizations.
func ParseRec(s string, normalize bool, shouldLog bool, nowF func() time.Time, lg *zap.Logger) (*Rec, error) {
	// strings.Fields does normalization by working with any number and any kind of whitespace
	words := strings.Fields(s)
	if len(words) != 3 {
		return nil, fmt.Errorf("got record of %d tokens for parsing", len(words))
	}

	val, err := strconv.ParseFloat(words[1], 64)
	if err != nil {
		return nil, errors.Wrapf(err, "error converting incoming record val %s", words[1])
	}

	var tm uint64
	tm, err = strconv.ParseUint(words[2], 0, 32)
	if err != nil {
		//even though this does not follow convention, clients sometimes send fractional timestamp
		// this is a special case and it is logged
		timeF, err := strconv.ParseFloat(words[2], 32)
		tm = uint64(timeF)
		if err != nil {
			return nil, errors.Wrapf(err, "error converting incoming record time %s", words[2])
		}

		if shouldLog {
			lg.Info("Got floating point timestamp", zap.String("record", s))
		}
	}

	var path string
	if normalize {
		path = normalizePath(words[0])
	} else {
		path = words[0]
	}

	return &Rec{
		Path:     path,
		Val:      float64(val),
		RawVal:   words[1],
		Time:     uint32(tm),
		RawTime:  words[2],
		Received: nowF(),
	}, nil
}

// Serialize makes record into a string ready to be sent via TCP w/ line protocol.
func (r *Rec) Serialize() string {
	// TODO (grzkv): serialization can be avoided in case there is no normalization

	// out of printf, strings.Builder (with pre-grow) and simply
	// concat of strings, the latter turns out to be the fastest.
	// If you change anything in the next line, benchmark:  You
	// may cause a switch from a fast path in the Go runtime to
	// a slow path and strings.Builder might be faster then.
	s := r.Path + " " + r.RawVal + " " + r.RawTime + "\n"

	return s
}

// Copy record
func (r *Rec) Copy() *Rec {
	ret := *r
	return &ret
}

// normalizePath does path normalization as described in the docs.
// It happens in one linear pass along the string. Pointers are used for input and output to save time on data copying.
func normalizePath(s string) string {
	if len(s) == 0 {
		res := ""
		return res
	}

	start := 0
	for ; s[start] == '.' && start < len(s); start++ {
	}
	if start == len(s) {
		res := ""
		return res
	}

	end := len(s) - 1
	for ; s[end] == '.' && end >= 0; end-- {
	}
	// check for string consisting only of points was done before

	var b strings.Builder
	for i := start; i <= end; i++ {
		if s[i] == '.' {
			// a dot cannot be the last char
			if s[i+1] == '.' {
				continue
			}
		}

		if validChar(s[i]) {
			b.WriteByte(s[i])
		} else {
			b.WriteRune('_')
		}
	}

	res := b.String()
	return res
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
