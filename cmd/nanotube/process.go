package main

import (
	"sync"
	"time"

	"go.uber.org/zap"

	"nanotube/pkg/metrics"
	"nanotube/pkg/rec"
	"nanotube/pkg/rewrites"
	"nanotube/pkg/rules"
	"nanotube/pkg/target"
)

// Process contains all the CPU-intensive processing operations
func Process(queue <-chan string, rules rules.Rules, rewrites rewrites.Rewrites, workerPoolSize uint16, shouldValidate bool, shouldLog bool, lg *zap.Logger, metrics *metrics.Prom) chan struct{} {
	done := make(chan struct{})
	var wg sync.WaitGroup
	for w := 1; w <= int(workerPoolSize); w++ {
		go worker(&wg, queue, rules, rewrites, shouldValidate, shouldLog, lg, metrics)
		wg.Add(1)
	}
	go func() {
		wg.Wait()
		close(done)
	}()

	return done
}

func worker(wg *sync.WaitGroup, queue <-chan string, rules rules.Rules, rewrites rewrites.Rewrites, shouldValidate bool, shouldLog bool, lg *zap.Logger, metrics *metrics.Prom) {
	defer wg.Done()
	for j := range queue {
		proc(&j, rules, rewrites, shouldValidate, shouldLog, lg, metrics)
	}
}

func routeRec(rec *rec.Rec, rules rules.Rules, lg *zap.Logger, metrics *metrics.Prom) {
	pushedTo := make(map[*target.Cluster]struct{})

	for _, rl := range rules {
		matchedRule := rl.Match(rec)
		if matchedRule {
			for _, cl := range rl.Targets {
				if _, pushedBefore := pushedTo[cl]; pushedBefore {
					continue
				}
				err := cl.Push(rec, metrics)
				if err != nil {
					lg.Error("push to cluster failed",
						zap.Error(err),
						zap.String("cluster", cl.Name),
						zap.String("record", *rec.Serialize()))
				}
				pushedTo[cl] = struct{}{}
			}
		}

		if matchedRule && !rl.Continue {
			break
		}
	}
}

func proc(s *string, rules rules.Rules, rewrites rewrites.Rewrites, shouldNormalize bool, shouldLog bool, lg *zap.Logger, metrics *metrics.Prom) {
	r, err := rec.ParseRec(*s, shouldNormalize, shouldLog, time.Now, lg)
	if err != nil {
		lg.Info("Error parsing incoming record", zap.String("record", *s), zap.Error(err))
		metrics.ErrorRecs.Inc()
		return
	}

	recs, err := rewrites.RewriteMetric(r)
	if err != nil {
		//even if we get error here, we should still receive back original record
		lg.Info("Error rewriting metric", zap.String("record", *s), zap.Error(err))
	}

	for _, rec := range recs {
		routeRec(rec, rules, lg, metrics)
	}

	// TODO: counter for dropped metrics
}
