package conf

import (
	"fmt"
	"io"

	"github.com/BurntSushi/toml"
	"github.com/pkg/errors"
)

// Rewrites represents rewrites config.
type Rewrites struct {
	Rewrite []Rewrite
}

// Rewrite in configuration for a single rewrite rule.
type Rewrite struct {
	From string
	To   string
	Copy bool
}

// ReadRewrites reads rules from the reader. Errors when parsing fails.
func ReadRewrites(r io.Reader) (Rewrites, error) {
	var rewrites Rewrites

	_, err := toml.NewDecoder(r).Decode(&rewrites)
	if err != nil {
		return rewrites, errors.Wrap(err, "error parsing rewrites")
	}
	if len(rewrites.Rewrite) == 0 {
		return rewrites, fmt.Errorf("no rules specified in the rules file")
	}
	for idx, rewrite := range rewrites.Rewrite {
		if len(rewrite.From) == 0 {
			return rewrites, fmt.Errorf("rewrite %d is missing 'From' section", idx)
		}
		if len(rewrite.To) == 0 {
			return rewrites, fmt.Errorf("rule %d is missing 'clusters' section", idx)
		}
	}
	return rewrites, nil
}
