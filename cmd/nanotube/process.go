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
	"github.com/bookingcom/nanotube/pkg/target"
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
	procBatch(s, rules, rewrites, shouldNormalize, shouldLog, time.Now, lg, metrics, nil)
}

// procBatch is proc staging the routed records in b; a nil b queues them on
// the hosts directly.
func procBatch(s []byte, rules rules.Rules, rewrites rewrites.Rewrites, shouldNormalize bool, shouldLog bool,
	nowF func() time.Time, lg *zap.Logger, metrics *metrics.Prom, b *target.Batch) {
	r, err := rec.ParseRec(s, shouldNormalize, shouldLog, nowF, lg)
	if err != nil {
		lg.Info("Error parsing incoming record", zap.String("record_str", string(s)),
			zap.Binary("record_base64", s), zap.Error(err))
		metrics.ErrorRecs.Inc()
		return
	}

	var recsBuf [4]*rec.RecBytes
	recs, err := rewrites.AppendRewrittenMetricBytes(recsBuf[:0], r, metrics)
	if err != nil {
		lg.Info("Error parsing incoming record", zap.String("record_str", string(s)),
			zap.Binary("record_base64", s), zap.Error(err))
		return
	}

	for _, rec := range recs {
		rules.RouteRecBatch(rec, lg, b)
	}
}

func workerBuf(wg *sync.WaitGroup, queue <-chan [][]byte, rules rules.Rules, rewrites rewrites.Rewrites,
	shouldValidate bool, shouldLog bool, lg *zap.Logger, metrics *metrics.Prom) {
	defer wg.Done()
	b := target.NewBatch()
	for j := range queue {
		procBuf(j, rules, rewrites, shouldValidate, shouldLog, lg, metrics, b)
	}
}

// procBuf processes a batch of records. Routed records are staged in b and
// handed to the host queues once the whole batch is processed.
func procBuf(s [][]byte, rules rules.Rules, rewrites rewrites.Rewrites, shouldNormalize bool, shouldLog bool, lg *zap.Logger,
	metrics *metrics.Prom, b *target.Batch) { //nolint:golint,unparam

	// Records of a batch are received together; one timestamp serves them all.
	now := time.Now()
	nowF := func() time.Time { return now }
	for _, rec := range s {
		procBatch(rec, rules, rewrites, shouldNormalize, shouldLog, nowF, lg, metrics, b)
	}
	b.Flush()
}
