package rewrites

import (
	"testing"

	"nanotube/pkg/conf"
	"nanotube/pkg/rec"

	"github.com/google/go-cmp/cmp"
)

func TestRewrites(t *testing.T) {
	rewrites := map[string]conf.Rewrites{
		"first": conf.Rewrites{
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
		},
	}

	tests := []struct {
		rewrites string
		in       string
		out      []string
	}{
		{
			rewrites: "first",
			in:       "abc",
			out:      []string{"cde"},
		},
		{
			rewrites: "first",
			in:       "def",
			out:      []string{"def", "acd"},
		},
		{
			rewrites: "first",
			in:       "ghtesttesttest",
			out:      []string{"[[testtesttest]]"},
		},
	}

	for _, test := range tests {
		rewrites, err := Build(rewrites[test.rewrites])
		if err != nil {
			t.Error(err)
		}
		record := &rec.Rec{
			Path: test.in,
		}
		resultRecords := rewrites.RewriteMetric(record)
		result := make([]string, 0)
		for _, rec := range resultRecords {
			result = append(result, rec.Path)
		}

		if !cmp.Equal(result, test.out) {
			diff := cmp.Diff(test.out, result)
			t.Errorf("Expected: %s\n received: %s\n rewrites: %s\n diff: %s\n", test.out, result, test.rewrites, diff)
		}
	}

}
