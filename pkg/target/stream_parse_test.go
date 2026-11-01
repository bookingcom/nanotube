package target

import (
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/bookingcom/nanotube/pkg/rec"
)

func TestStreamPlaintextNumericParity(t *testing.T) {
	values := []string{"0", "-0", "1.25", "-1e200", "0x1.8p+2", "+Inf", "-Inf", "NaN", "nan", "1e1000", "bad", "1_000.5"}
	stamps := []string{"0", "-0.9", "1700000000.999", "-123.8", "1e10", "0x1p10", "+Inf", "-Inf", "NaN", "1e1000", "bad"}
	for _, v := range values {
		for _, ts := range stamps {
			t.Run(v+"/"+ts, func(t *testing.T) {
				value, ve := strconv.ParseFloat(v, 64)
				timestamp, te := strconv.ParseFloat(ts, 64)
				valid := ve == nil && te == nil && !math.IsNaN(value) && !math.IsNaN(timestamp)
				r := &rec.RecBytes{Path: []byte("example.metric"), Val: []byte(v), Time: []byte(ts)}
				p, ok := parseStreamPoint(r)
				if ok != valid {
					t.Fatalf("accepted=%v, plaintext accepts=%v", ok, valid)
				}
				if ok && (math.Float64bits(p.value) != math.Float64bits(value) || p.timestamp != int64(timestamp)) {
					t.Fatalf("point changed: %+v", p)
				}
			})
		}
	}
}

func TestStreamKnownPointParsingAllocations(t *testing.T) {
	r := &rec.RecBytes{Path: []byte(strings.Repeat("example.prefix.", 15)), Val: []byte("12345.125"), Time: []byte("1700000000.999")}
	if n := testing.AllocsPerRun(1000, func() {
		if _, ok := parseStreamPoint(r); !ok {
			panic("invalid fixture")
		}
	}); n != 0 {
		t.Fatalf("numeric parsing allocated %v times", n)
	}
}
