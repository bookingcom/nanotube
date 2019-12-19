package conf

import (
	"fmt"
	"io"

	"github.com/burntsushi/toml"
	"github.com/pkg/errors"
)

// Rules represents rules config.
type Rules struct {
	Rule []Rule
}

// Rule in configuration for a single rule.
type Rule struct {
	Regexs   []string
	Clusters []string
	Continue bool
}

// ReadRules reads rules from the reader. Errors when parsing fails.
func ReadRules(r io.Reader) (Rules, error) {
	var rs Rules
	_, err := toml.DecodeReader(r, &rs)
	if err != nil {
		return rs, errors.Wrap(err, "error parsing rules")
	}
	if len(rs.Rule) == 0 {
		return rs, fmt.Errorf("no rules specified in the rules file")
	}
	for idx, rule := range rs.Rule {
		if len(rule.Regexs) == 0 {
			return rs, fmt.Errorf("rule %d is missing 'regexs' section", idx)
		}
		if len(rule.Clusters) == 0 {
			return rs, fmt.Errorf("rule %d is missing 'clusters' section", idx)
		}
	}
	return rs, nil
}
