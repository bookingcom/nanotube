// Package rules provides primitives for working with routing rules.
package rules

import (
	"fmt"
	"regexp"

	"go.uber.org/zap"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/metrics"
	"github.com/bookingcom/nanotube/pkg/rec"
	"github.com/bookingcom/nanotube/pkg/target"

	"github.com/pkg/errors"
	"github.com/prometheus/client_golang/prometheus"
)

// Rules represent all the routing rules/routing table.
type Rules struct {
	rules        []Rule
	measureRegex bool
	metrics      *metrics.Prom
}

// Rule is a routing rule.
type Rule struct {
	Regexs     []string
	Prefixes   []string
	Targets    []target.ClusterTarget
	Continue   bool
	CompiledRE []*regexp.Regexp

	PrefixTrie *PrefixTrie

	matchers      []matcher
	regexDuration []prometheus.Observer
}

// Build reads rules from config, compiles them.
func Build(crs *conf.Rules, clusters target.Clusters, measureRegex bool, ms *metrics.Prom) (Rules, error) {
	var rs Rules

	rs.measureRegex = measureRegex
	rs.metrics = ms

	for _, cr := range crs.Rule {
		r := Rule{
			Regexs:   cr.Regexs,
			Prefixes: cr.Prefixes,
			Continue: cr.Continue,
		}
		for _, clName := range cr.Clusters {
			cl, ok := clusters[clName]
			if !ok {
				return rs,
					fmt.Errorf("got non-existent cluster name %s in the rules config",
						clName)
			}
			r.Targets = append(r.Targets, cl)
		}

		rs.rules = append(rs.rules, r)
	}

	err := rs.compile()
	if err != nil {
		return rs, errors.Wrap(err, "rules compilation failed")
	}

	return rs, nil
}

// compile precompiles regexps for perf and performs validation.
func (rs Rules) compile() error {
	for i := range rs.rules {
		rs.rules[i].CompiledRE = make([]*regexp.Regexp, 0)
		rs.rules[i].matchers = make([]matcher, 0, len(rs.rules[i].Regexs))
		for _, re := range rs.rules[i].Regexs {
			cre, err := regexp.Compile(re)
			if err != nil {
				return errors.Wrapf(err, "compiling regex %s failed", cre)
			}
			rs.rules[i].CompiledRE = append(rs.rules[i].CompiledRE, cre)
			rs.rules[i].matchers = append(rs.rules[i].matchers, newMatcher(cre))
			if rs.measureRegex {
				labels := prometheus.Labels{
					"rule_type": "routing",
					"regex":     re,
				}
				rs.rules[i].regexDuration = append(rs.rules[i].regexDuration, rs.metrics.RegexDuration.With(labels))
			}
		}

		rs.rules[i].PrefixTrie = NewPrefixTrie()
		for _, pre := range rs.rules[i].Prefixes {
			rs.rules[i].PrefixTrie.Add([]byte(pre))
		}
	}

	return nil
}

// TestBuild makes a set of rules for testing.
func TestBuild(crs conf.Rules, clusters map[string]*target.TestTarget, measureRegex bool, ms *metrics.Prom) (Rules, error) {
	var rs Rules

	rs.measureRegex = measureRegex
	rs.metrics = ms

	for _, cr := range crs.Rule {
		r := Rule{
			Regexs:   cr.Regexs,
			Prefixes: cr.Prefixes,
			Continue: cr.Continue,
		}
		for _, clName := range cr.Clusters {
			cl, ok := clusters[clName]
			if !ok {
				return rs,
					fmt.Errorf("got non-existent cluster name %s in the rules config",
						clName)
			}
			r.Targets = append(r.Targets, cl)
		}

		rs.rules = append(rs.rules, r)
	}

	err := rs.compile()
	if err != nil {
		return rs, errors.Wrap(err, "rules compilation failed")
	}

	return rs, nil
}

// RouteRec a record by following the rules
func (rs Rules) RouteRec(r *rec.RecBytes, lg *zap.Logger) {
	rs.RouteRecBatch(r, lg, nil)
}

// RouteRecBatch routes a record like RouteRec, but stages it in b for the
// cluster targets that support batching. The caller must flush b.
func (rs Rules) RouteRecBatch(r *rec.RecBytes, lg *zap.Logger, b *target.Batch) {
	// Rules rarely name more than a handful of clusters; a small slice scanned
	// linearly avoids allocating a set per record.
	var pushedBuf [8]target.ClusterTarget
	pushedTo := pushedBuf[:0]

	for i := range rs.rules {
		rl := &rs.rules[i]
		matchedRule := rl.match(r, rs.measureRegex)
		if matchedRule {
			for _, cl := range rl.Targets {
				if pushedBefore(pushedTo, cl) {
					continue
				}
				var err error
				if bp, ok := cl.(target.BatchPusher); ok && b != nil {
					err = bp.PushBytesBatch(r, rs.metrics, b)
				} else {
					err = cl.PushBytes(r, rs.metrics)
				}
				if err != nil {
					lg.Error("push to cluster failed",
						zap.Error(err),
						zap.String("cluster", cl.GetName()),
						zap.String("record", string(r.Serialize())))
				}
				pushedTo = append(pushedTo, cl)
			}
		}

		if matchedRule && !rl.Continue {
			break
		}
	}
}

func pushedBefore(pushedTo []target.ClusterTarget, cl target.ClusterTarget) bool {
	for _, p := range pushedTo {
		if p == cl {
			return true
		}
	}
	return false
}

// Match a record with any of the rule regexps
func (rl Rule) Match(r *rec.RecBytes, measureRegex bool) bool {
	return rl.match(r, measureRegex)
}

func (rl *Rule) match(r *rec.RecBytes, measureRegex bool) bool {
	if rl.PrefixTrie.Check(r.Path) {
		return true
	}

	// Rules not built through compile() have no matchers.
	useMatchers := len(rl.matchers) == len(rl.CompiledRE)

	var timer *prometheus.Timer

	for idx, re := range rl.CompiledRE {
		if measureRegex {
			timer = prometheus.NewTimer(rl.regexDuration[idx])
		}
		var matched bool
		if useMatchers {
			matched = rl.matchers[idx].Match(r.Path)
		} else {
			matched = re.Match(r.Path)
		}
		if measureRegex {
			timer.ObserveDuration()
		}
		if matched {
			return true
		}
	}
	return false
}
