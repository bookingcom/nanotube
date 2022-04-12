package rewrites

import (
	"testing"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/metrics"
	"github.com/bookingcom/nanotube/pkg/rec"

	"github.com/google/go-cmp/cmp"
)

func TestRewrites(t *testing.T) {
	rewrites := conf.Rewrites{
		Rewrite: []conf.Rewrite{
			{
				From: "a.*bc",
				To:   "cde",
				Copy: false,
			},
			{
				From: "def",
				To:   "acd",
				Copy: true,
			},
			{
				From: "gh([A-Za-z0-9_-]+)",
				To:   "[[$1]]",
				Copy: false,
			},
		},
	}

	tests := []struct {
		in  []byte
		out [][]byte
	}{
		{
			in:  []byte("abcxxx"),
			out: [][]byte{[]byte("cdexxx")},
		},
		{
			in:  []byte("def"),
			out: [][]byte{[]byte("def"), []byte("acd")},
		},
		{
			in:  []byte("ghtesttesttest"),
			out: [][]byte{[]byte("[[testtesttest]]")},
		},
	}

	cfg := conf.MakeDefault()
	ms := metrics.New(&cfg)

	for _, test := range tests {
		rewrites, err := Build(&rewrites, false, ms)
		if err != nil {
			t.Error(err)
		}
		record := &rec.RecBytes{
			Path: test.in,
		}
		resultRecords, err := rewrites.RewriteMetricBytes(record, ms)
		if err != nil {
			t.Error("could not rewrite record")
		}
		result := make([][]byte, 0)
		for _, rec := range resultRecords {
			result = append(result, rec.Path)
		}

		if !cmp.Equal(result, test.out) {
			diff := cmp.Diff(test.out, result)
			t.Errorf("Expected: %s\n received: %s\n diff: %s\n", test.out, result, diff)
		}
	}

}
