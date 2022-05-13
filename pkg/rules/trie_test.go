package rules

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"path/filepath"
	"testing"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/test"
	"github.com/pkg/errors"
)

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

func TestTrieArr(t *testing.T) {
	tr := NewPrefixTrieArr()

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

func extractPaths(data [][]byte) (paths [][]byte, errRet error) {
	for _, rec := range data {
		tokens := bytes.Split(rec, []byte{' '})
		if len(tokens) != 3 {
			errRet = fmt.Errorf("got a record of %d tokens", len(tokens))
			return
		}
		paths = append(paths, tokens[0])
	}

	return
}

func readRules() (rules conf.Rules, retErr error) {
	fixturesPath := "../testdata/"

	f, err := ioutil.ReadFile(filepath.Join(fixturesPath, "rules.toml"))
	if err != nil {
		retErr = errors.Wrap(err, "error reading rules file")
		return
	}

	r := bytes.NewReader(f)
	rules, err = conf.ReadRules(r)
	if err != nil {
		retErr = errors.Wrap(err, "error while compiling rules")
		return
	}

	return
}

func BenchmarkTrie(b *testing.B) {
	b.StopTimer()

	data, _, _, err := test.Setup()
	if err != nil {
		b.Fatalf("error during benchmark setup: %v", err)
	}

	paths, err := extractPaths(data)
	if err != nil {
		b.Fatalf("error during paths extraction: %v", err)
	}

	rules, err := readRules()
	if err != nil {
		b.Fatalf("error while reading rules: %v", err)
	}

	trie := NewPrefixTrie()
	for _, r := range rules.Rule {
		for _, p := range r.Prefixes {
			trie.Add([]byte(p))
		}
	}

	b.StartTimer()

	for i := 0; i < b.N; i++ {
		for _, path := range paths {
			trie.Check(path)
		}
	}
}

func BenchmarkTrieArr(b *testing.B) {
	b.StopTimer()

	data, _, _, err := test.Setup()
	if err != nil {
		b.Fatalf("error during benchmark setup: %v", err)
	}

	paths, err := extractPaths(data)
	if err != nil {
		b.Fatalf("error during paths extraction: %v", err)
	}

	rules, err := readRules()
	if err != nil {
		b.Fatalf("error while reading rules: %v", err)
	}

	trie := NewPrefixTrieArr()
	for _, r := range rules.Rule {
		for _, p := range r.Prefixes {
			trie.Add([]byte(p))
		}
	}

	b.StartTimer()

	for i := 0; i < b.N; i++ {
		for _, path := range paths {
			trie.Check(path)
		}
	}
}
