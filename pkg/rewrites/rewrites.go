// Package rewrites provides primitives for working with rewrite rules.
package rewrites

import (
	"regexp"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/rec"

	"github.com/pkg/errors"
)

// Rewrites represent all the routing rewrites/routing table.
type Rewrites []Rewrite

// Rewrite is a routing rewrite.
type Rewrite struct {
	From string
	To   string
	Copy bool

	CompiledFrom *regexp.Regexp
}

// Build reads rewrite rules from config, compiles them.
func Build(crw conf.Rewrites) (Rewrites, error) {
	var rw Rewrites
	for _, cr := range crw.Rewrite {
		r := Rewrite{
			From: cr.From,
			To:   cr.To,
			Copy: cr.Copy,
		}

		rw = append(rw, r)
	}

	err := rw.Compile()
	if err != nil {
		return rw, errors.Wrap(err, "rewrite rule compilation failed :")
	}

	return rw, nil
}

// Compile precompiles regexps
func (rw Rewrites) Compile() error {
	for i, r := range rw {
		cre, err := regexp.Compile(r.From)
		if err != nil {
			return errors.Wrapf(err, "compiling rewrite regex: %s failed", cre)
		}
		rw[i].CompiledFrom = cre
	}

	return nil
}

// RewriteMetric executes all rewrite rules on a record
// If copy is true and rule matches, we generate new record
func (rw Rewrites) RewriteMetric(record *rec.Rec) []*rec.Rec {
	result := []*rec.Rec{record}

	for _, r := range rw {
		if r.CompiledFrom.MatchString(record.Path) {
			newPath := r.CompiledFrom.ReplaceAllString(record.Path, r.To)
			if r.Copy {
				// keep both old and new value
				copy := record.Copy()
				copy.Path = newPath

				result = append(result, copy)
			} else {
				// no copy, rewrite
				record.Path = newPath
			}
		}
	}
	return result
}
