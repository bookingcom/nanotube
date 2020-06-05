package main

import (
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/bookingcom/nanotube/pkg/metrics"
	"github.com/bookingcom/nanotube/pkg/rec"
	"github.com/bookingcom/nanotube/pkg/rewrites"
	"github.com/bookingcom/nanotube/pkg/rules"
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
		proc(j, rules, rewrites, shouldValidate, shouldLog, lg, metrics)
	}
}

func proc(s string, rules rules.Rules, rewrites rewrites.Rewrites, shouldNormalize bool, shouldLog bool, lg *zap.Logger, metrics *metrics.Prom) {
	r, err := rec.ParseRec(s, shouldNormalize, shouldLog, time.Now, lg)
	if err != nil {
		lg.Info("Error parsing incoming record", zap.String("record", s), zap.Error(err))
		metrics.ErrorRecs.Inc()
		return
	}

	recs := rewrites.RewriteMetric(r)

	for _, rec := range recs {
		rules.RouteRec(rec, lg)
	}

	// TODO: counter for dropped metrics
}
