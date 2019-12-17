package conf

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestRewritesSimple(t *testing.T) {
	cfg := `
	[[rewrite]]
	from = "a.*bc"
	to = "cde"

	[[rewrite]]
	from = "def"
	to = "acd"
	copy = true
	`

	expected := Rewrites{
		Rewrite: []Rewrite{
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
		},
	}

	rs, err := ReadRewrites(strings.NewReader(cfg))
	if err != nil {
		t.Fatalf("rewrites parsing failed, %v", err)
	}

	if diff := cmp.Diff(rs, expected); diff != "" {
		t.Fatalf("expected rewrites are different from actual\n%s", diff)
	}
}
