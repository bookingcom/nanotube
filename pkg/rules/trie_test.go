package rules

import "testing"

func TestTrie(t *testing.T) {
	tr := NewPrefixTrie()

	tr.Add([]byte("abc"))
	tr.Add([]byte("a"))
	tr.Add([]byte("xxxxxxxxxxxxxxxx"))

	samples := map[string]bool{
		"a":                        true,
		"aaa":                      true,
		"alkjlkmlksjdflkdsjlfkjsd": true,
		"xxxx":                     false,
		"clkj":                     false,
		"":                         true,
		"abc":                      true,
	}

	for s, res := range samples {
		if tr.Check([]byte(s)) != res {
			t.Errorf("expected %t for %s, got %t", res, s, !res)
		}
	}
}
