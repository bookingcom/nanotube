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

// Empty reports whether names can go directly to rule resolution.
func (rw Rewrites) Empty() bool { return len(rw.rewrites) == 0 }

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
func Build(crw *conf.Rewrites, measureRegex bool, metrics *metrics.Prom) (Rewrites, error) {
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
		return rw, errors.Wrap(err, "rewrite rule compilation failed")
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

// RewriteMetricBytes executes all rewrite rules on a record.
// If copy is true and rule matches, we generate new record.
func (rw Rewrites) RewriteMetricBytes(record *rec.RecBytes, ms *metrics.Prom) ([]*rec.RecBytes, error) {
	return rw.AppendRewrittenMetricBytes(nil, record, ms)
}

// AppendRewrittenMetricBytes is like RewriteMetricBytes, but appends the
// resulting records to dst.
func (rw Rewrites) AppendRewrittenMetricBytes(dst []*rec.RecBytes, record *rec.RecBytes, ms *metrics.Prom) ([]*rec.RecBytes, error) {
	records, matched, err := rw.PlanMetricBytes(dst, record)
	if matched {
		ms.RewrittenRecsTotal.Inc()
	}
	return records, err
}

// PlanMetricBytes evaluates immutable name rewrites without counting a point.
// ID-based inputs reuse this plan, then count matched points on every replay.
func (rw Rewrites) PlanMetricBytes(dst []*rec.RecBytes, record *rec.RecBytes) ([]*rec.RecBytes, bool, error) {
	var timer *prometheus.Timer

	result := append(dst, record)

	wasMatched := false

	for _, r := range rw.rewrites {
		if rw.measureRegex {
			timer = prometheus.NewTimer(r.matchDuration)
		}

		matched := r.CompiledFrom.Match(record.Path)
		if rw.measureRegex {
			timer.ObserveDuration()
		}

		if matched {
			wasMatched = true

			if rw.measureRegex {
				timer = prometheus.NewTimer(r.replaceDuration)
			}

			newPath := r.CompiledFrom.ReplaceAll(record.Path, []byte(r.To))

			if rw.measureRegex {
				timer.ObserveDuration()
			}

			if r.Copy {
				// keep both old and new value
				copy, err := record.Copy()
				if err != nil {
					return nil, false, errors.Wrapf(err, "could not copy the record: %v", record)
				}
				copy.Path = newPath

				result = append(result, copy)
			} else {
				// no copy, rewrite
				record.Path = newPath
			}
		}
	}

	return result, wasMatched, nil
}
