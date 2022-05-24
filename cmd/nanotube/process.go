package main

import (
	"runtime"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/bookingcom/nanotube/pkg/metrics"
	"github.com/bookingcom/nanotube/pkg/rec"
	"github.com/bookingcom/nanotube/pkg/rewrites"
	"github.com/bookingcom/nanotube/pkg/rules"
)

// ProcessBuf contains all the CPU-intensive processing operations.
func ProcessBuf(queue <-chan [][]byte, rules rules.Rules, rewrites rewrites.Rewrites, workerPoolSize int,
	shouldValidate bool, shouldLog bool, lg *zap.Logger, metrics *metrics.Prom) chan struct{} {
	done := make(chan struct{})

	if workerPoolSize < 1 {
		workerPoolSize = runtime.GOMAXPROCS(0) / 2
		if workerPoolSize == 0 {
			workerPoolSize = 1
		}
	}

	var wg sync.WaitGroup
	for w := 0; w < int(workerPoolSize); w++ {
		wg.Add(1)
		go workerBuf(&wg, queue, rules, rewrites, shouldValidate, shouldLog, lg, metrics)
	}
	go func() {
		wg.Wait()
		close(done)
	}()

	return done
}

// Process contains all the CPU-intensive processing operations
func Process(queue <-chan []byte, rules rules.Rules, rewrites rewrites.Rewrites, workerPoolSize int,
	shouldValidate bool, shouldLog bool, lg *zap.Logger, metrics *metrics.Prom) chan struct{} {
	done := make(chan struct{})

	if workerPoolSize < 1 {
		workerPoolSize = runtime.GOMAXPROCS(0) / 2
		if workerPoolSize == 0 {
			workerPoolSize = 1
		}
	}

	var wg sync.WaitGroup
	for w := 1; w <= int(workerPoolSize); w++ {
		wg.Add(1)
		go worker(&wg, queue, rules, rewrites, shouldValidate, shouldLog, lg, metrics)
	}
	go func() {
		wg.Wait()
		close(done)
	}()

	return done
}

func worker(wg *sync.WaitGroup, queue <-chan []byte, rules rules.Rules, rewrites rewrites.Rewrites,
	shouldValidate bool, shouldLog bool, lg *zap.Logger, metrics *metrics.Prom) {
	defer wg.Done()
	for j := range queue {
		proc(j, rules, rewrites, shouldValidate, shouldLog, lg, metrics)
	}
}

func proc(s []byte, rules rules.Rules, rewrites rewrites.Rewrites, shouldNormalize bool, shouldLog bool, lg *zap.Logger,
	metrics *metrics.Prom) { //nolint:golint,unparam
	r, err := rec.ParseRec(s, shouldNormalize, shouldLog, time.Now, lg)
	if err != nil {
		lg.Info("Error parsing incoming record", zap.String("record_str", string(s)),
			zap.Binary("record_base64", s), zap.Error(err))
		metrics.ErrorRecs.Inc()
		return
	}

	recs, err := rewrites.RewriteMetricBytes(r, metrics)
	if err != nil {
		lg.Info("Error parsing incoming record", zap.String("record_str", string(s)),
			zap.Binary("record_base64", s), zap.Error(err))
		return
	}

	for _, rec := range recs {
		rules.RouteRec(rec, lg)
	}
}

func workerBuf(wg *sync.WaitGroup, queue <-chan [][]byte, rules rules.Rules, rewrites rewrites.Rewrites,
	shouldValidate bool, shouldLog bool, lg *zap.Logger, metrics *metrics.Prom) {
	defer wg.Done()
	for j := range queue {
		procBuf(j, rules, rewrites, shouldValidate, shouldLog, lg, metrics)
	}
}

func procBuf(s [][]byte, rules rules.Rules, rewrites rewrites.Rewrites, shouldNormalize bool, shouldLog bool, lg *zap.Logger,
	metrics *metrics.Prom) { //nolint:golint,unparam

	for _, rec := range s {
		proc(rec, rules, rewrites, shouldNormalize, shouldLog, lg, metrics)

	}
}
