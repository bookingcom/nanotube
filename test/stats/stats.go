package stats

import (
	"log"
	"sync/atomic"
	"time"
)

// Stats is the test statistics struct.
type Stats struct {
	total            uint32
	intervalBucket   uint32
	lastReport       int64
	lastMessage      int64
	reportEvery      time.Duration
	finalReportAfter time.Duration
	finalReportSent  bool
	prefix           string
	finalCallback    func()
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
func NewStats(reportEvery time.Duration, finalReportAfter time.Duration,
	prefix string, finalCallback func()) *Stats {
	ret := &Stats{
		reportEvery:      reportEvery,
		finalReportAfter: finalReportAfter,
		prefix:           prefix,
		finalCallback:    finalCallback,
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
	lastReportDuration := time.Now().Unix() - s.lastReport
	if lastReportDuration == 0 {
		return
	}
	rate := float64(reqs) / float64(lastReportDuration)
	if final {
		log.Printf("%s -- Final report -- Requests: %d ", s.prefix, reqs)
		s.finalCallback()
	}

	log.Printf("%s -- Requests: %d -- Rate: %g", s.prefix, reqs, rate)
	s.lastReport = time.Now().Unix()
}
