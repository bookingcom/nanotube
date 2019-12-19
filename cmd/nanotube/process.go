package main

import (
	"sync"
	"time"

	"go.uber.org/zap"

	"nanotube/pkg/metrics"
	"nanotube/pkg/rec"
	"nanotube/pkg/rules"
	"nanotube/pkg/target"
)

// Process contains all the CPU-intensive processing operations
func Process(queue <-chan string, rules rules.Rules, workerPoolSize uint16, shouldValidate bool, shouldLog bool, lg *zap.Logger, metrics *metrics.Prom) chan struct{} {
	done := make(chan struct{})
	var wg sync.WaitGroup
	for w := 1; w <= int(workerPoolSize); w++ {
		go worker(&wg, queue, rules, shouldValidate, shouldLog, lg, metrics)
		wg.Add(1)
	}
	go func() {
		wg.Wait()
		close(done)
	}()

	return done
}

func worker(wg *sync.WaitGroup, queue <-chan string, rules rules.Rules, shouldValidate bool, shouldLog bool, lg *zap.Logger, metrics *metrics.Prom) {
	defer wg.Done()
	for j := range queue {
		proc(&j, rules, shouldValidate, shouldLog, lg, metrics)
	}
}

func proc(s *string, rules rules.Rules, shouldNormalize bool, shouldLog bool, lg *zap.Logger, metrics *metrics.Prom) {
	r, err := rec.ParseRec(*s, shouldNormalize, shouldLog, time.Now, lg)
	if err != nil {
		lg.Info("Error parsing incoming record", zap.String("record", *s), zap.Error(err))
		metrics.ErrorRecs.Inc()
		return
	}

	pushedTo := make(map[*target.Cluster]struct{})

	for _, rl := range rules {
		matchedRules := false
		for _, re := range rl.CompiledRE {
			if re.MatchString(r.Path) {
				matchedRules = true
				for _, cl := range rl.Targets {
					if _, pushedBefore := pushedTo[cl]; pushedBefore {
						continue
					}
					err = cl.Push(r, metrics)
					if err != nil {
						lg.Error("push to cluster failed",
							zap.Error(err),
							zap.String("cluster", cl.Name),
							zap.String("record", *r.Serialize()))
					}
					pushedTo[cl] = struct{}{}
				}
				// if one regex in the group matches, move to next rule
				break
			}
		}
		if matchedRules && !rl.Continue {
			break
		}
	}
}
