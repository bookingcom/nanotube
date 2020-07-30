package conf

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestRulesSimple(t *testing.T) {
	cfg := `
	[[rule]]
	regexs = [
		"abc",
		"a*b*c"
	]
	clusters = [
		"cl1",
		"cl2"
	]

	[[rule]]
	regexs = [
		"^abc$",
		"^cbd*df$"
	]
	prefixes = [
		"xyz",
		"oiu"
	]
	clusters = [
		"aaa",
		"bbb"
	]`

	expected := Rules{
		Rule: []Rule{
			{
				Regexs: []string{
					"abc",
					"a*b*c",
				},
				Clusters: []string{
					"cl1",
					"cl2",
				},
			},
			{
				Regexs: []string{
					"^abc$",
					"^cbd*df$",
				},
				Prefixes: []string{
					"xyz",
					"oiu",
				},
				Clusters: []string{
					"aaa",
					"bbb",
				},
			},
		},
	}

	rs, err := ReadRules(strings.NewReader(cfg))
	if err != nil {
		t.Fatalf("rules parsing failed, %v", err)
	}

	if diff := cmp.Diff(rs, expected); diff != "" {
		t.Fatalf("expceted rules are different from factual\n%s", diff)
	}
}
