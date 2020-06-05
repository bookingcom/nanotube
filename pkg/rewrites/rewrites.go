// Package rewrites provides primitives for working with rewrite rules.
package rewrites

import (
	"regexp"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/metrics"
	"github.com/bookingcom/nanotube/pkg/rec"

	"github.com/pkg/errors"
	"github.com/prometheus/client_golang/prometheus"
)

// Rewrites represent all the routing rewrites/routing table.
type Rewrites struct {
	rewrites     []Rewrite
	measureRegex bool
	metrics      *metrics.Prom
}

// Rewrite is a routing rewrite.
type Rewrite struct {
	From string
	To   string
	Copy bool

	CompiledFrom    *regexp.Regexp
	matchDuration   prometheus.Observer
	replaceDuration prometheus.Observer
}

// Build reads rewrite rules from config, compiles them.
func Build(crw conf.Rewrites, measureRegex bool, metrics *metrics.Prom) (Rewrites, error) {
	var rw Rewrites

	rw.measureRegex = measureRegex
	rw.metrics = metrics

	for _, cr := range crw.Rewrite {
		r := Rewrite{
			From: cr.From,
			To:   cr.To,
			Copy: cr.Copy,
		}

		rw.rewrites = append(rw.rewrites, r)
	}

	err := rw.compile()
	if err != nil {
		return rw, errors.Wrap(err, "rewrite rule compilation failed :")
	}

	return rw, nil
}

// compile precompiles regexps
func (rw Rewrites) compile() error {
	for i, r := range rw.rewrites {
		cre, err := regexp.Compile(r.From)
		if err != nil {
			return errors.Wrapf(err, "compiling rewrite regex: %s failed", cre)
		}
		rw.rewrites[i].CompiledFrom = cre
		if rw.measureRegex {
			rw.rewrites[i].matchDuration = rw.metrics.RegexDuration.With(prometheus.Labels{
				"rule_type": "rewrite_match",
				"regex":     r.From,
			})
			rw.rewrites[i].replaceDuration = rw.metrics.RegexDuration.With(prometheus.Labels{
				"rule_type": "rewrite_replace",
				"regex":     r.From + " :: " + r.To,
			})
		}
	}
	return nil
}

// RewriteMetric executes all rewrite rules on a record
// If copy is true and rule matches, we generate new record
func (rw Rewrites) RewriteMetric(record *rec.Rec) []*rec.Rec {
	var timer *prometheus.Timer

	result := []*rec.Rec{record}

	for _, r := range rw.rewrites {
		if rw.measureRegex {
			timer = prometheus.NewTimer(r.matchDuration)
		}
		matched := r.CompiledFrom.MatchString(record.Path)
		if rw.measureRegex {
			timer.ObserveDuration()
		}
		if matched {
			if rw.measureRegex {
				timer = prometheus.NewTimer(r.replaceDuration)
			}
			newPath := r.CompiledFrom.ReplaceAllString(record.Path, r.To)
			if rw.measureRegex {
				timer.ObserveDuration()
			}
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
