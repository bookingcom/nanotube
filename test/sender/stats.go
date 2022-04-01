package main

import (
	"sync/atomic"
	"time"

	"go.uber.org/zap"
)

// Stats is the test statistics struct.
type Stats struct {
	total            uint32
	intervalBucket   uint32
	lastReport       time.Time
	lastMessage      int64
	reportEvery      time.Duration
	finalReportAfter time.Duration
	finalReportSent  bool
	lg               *zap.Logger
}

func (s *Stats) finalReportTime() time.Duration {
	lastMessage := atomic.LoadInt64(&s.lastMessage)
	secondsSinceLastMessage := time.Second * time.Duration(time.Now().Unix()-lastMessage)
	finalReport := s.finalReportAfter - secondsSinceLastMessage
	if lastMessage == 0 || s.finalReportSent {
		finalReport = s.finalReportAfter
	}
	return finalReport
}

func (s *Stats) finalReporter() {
	for {
		finalReport := s.finalReportTime()
		<-time.After(finalReport)
		if s.finalReportTime() < 0 {
			s.Report(true)
		}
	}
}

func (s *Stats) periodicReporter() {
	for {
		<-time.After(s.reportEvery)
		s.Report(false)
	}
}

// NewStats constructs new statsistics.
func NewStats(reportEvery time.Duration, finalReportAfter time.Duration, lg *zap.Logger) *Stats {
	ret := &Stats{
		reportEvery:      reportEvery,
		finalReportAfter: finalReportAfter,
		lg:               lg,
	}

	go ret.periodicReporter()
	if finalReportAfter != 0 {
		go ret.finalReporter()
	}

	return ret
}

// Inc updates statistical data.
func (s *Stats) Inc() {
	now := time.Now().Unix()
	atomic.StoreInt64(&s.lastMessage, now)
	atomic.AddUint32(&s.intervalBucket, 1)
	atomic.AddUint32(&s.total, 1)
}

// Report prints a report to stdout.
func (s *Stats) Report(final bool) {
	addr := &s.intervalBucket
	if final {
		addr = &s.total
		s.finalReportSent = true
	}
	reqs := atomic.LoadUint32(addr)
	atomic.AddUint32(addr, -reqs)
	if reqs != 0 && !final {
		s.finalReportSent = false
	}
	lastReportDuration := time.Since(s.lastReport).Seconds()
	if lastReportDuration == 0.0 {
		return
	}
	rate := float64(reqs) / lastReportDuration
	if final {
		s.lg.Info("Final report", zap.Uint32("total requests", reqs))
	}

	s.lg.Info("Stats", zap.Uint32("sent", reqs), zap.Float64("rate", rate))
	s.lastReport = time.Now()
}
