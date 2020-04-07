package conf

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestConfSimple(t *testing.T) {
	conf := `
	ListeningPort = 2003
	EnableUDP = true
	EnableTCP = false
	TargetPort = 2008

	SendTimeoutSec = 7
	OutConnTimeoutSec = 9
	TermTimeoutSec = 11
	IncomingConnIdleTimeoutSec = 13

	MainQueueSize = 100
	HostQueueSize = 10
	WorkerPoolSize = 10
	TCPOutBufSize = 11
	TCPOutBufFlushPeriodSec = 3
	KeepAliveSec = 3
	MaxHostReconnectPeriodMs = 777
	HostReconnectPeriodDeltaMs = 13
	ConnectionLossThresholdMs = 200`

	expected := Main{
		ListeningPort: 2003,
		TargetPort:    2008,

		EnableTCP: false,
		EnableUDP: true,

		MainQueueSize: 100,
		HostQueueSize: 10,

		WorkerPoolSize: 10,

		IncomingConnIdleTimeoutSec: 13,
		SendTimeoutSec:             7,
		OutConnTimeoutSec:          9,
		TermTimeoutSec:             11,
		TCPOutBufSize:              11,
		TCPOutBufFlushPeriodSec:    3,
		KeepAliveSec:               3,
		MaxHostReconnectPeriodMs:   777,
		HostReconnectPeriodDeltaMs: 13,
		ConnectionLossThresholdMs:  200,

		NormalizeRecords:  true,
		LogSpecialRecords: true,

		PprofPort: 6000,
		PromPort:  9090,

		HostQueueLengthBucketFactor: 3,
		HostQueueLengthBuckets:      10,

		ProcessingDurationBucketFactor: 2,
		ProcessingDurationBuckets:      10,
	}

	cfg, err := ReadMain(strings.NewReader(conf))
	if err != nil {
		t.Fatalf("error during config parsing, %v", err)
	}
	if diff := cmp.Diff(expected, cfg); diff != "" {
		t.Fatalf("Expected and factual configs differ:\n%s",
			diff)
	}
}
