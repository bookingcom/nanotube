package conf

import (
	"io"

	"github.com/burntsushi/toml"
	"github.com/pkg/errors"
)

// Main is the main and generic nanotube config.
type Main struct {
	ListeningPort uint32
	TargetPort    uint16

	EnableTCP bool
	EnableUDP bool
	// 0 does not set buffer size
	UDPOSBufferSize uint32

	MainQueueSize uint64
	HostQueueSize uint64

	WorkerPoolSize uint16

	IncomingConnIdleTimeoutSec uint32
	SendTimeoutSec             uint32
	OutConnTimeoutSec          uint32
	TermTimeoutSec             uint16
	// 0 value turns off buffering
	TCPOutBufSize int
	// 0 value turns off flushing
	TCPOutBufFlushPeriodSec    uint32
	MaxHostReconnectPeriodMs   uint32
	HostReconnectPeriodDeltaMs uint32

	NormalizeRecords  bool
	LogSpecialRecords bool

	PprofPort uint16
	PromPort  uint16

	HostQueueLengthBucketFactor float64
	HostQueueLengthBuckets      int

	ProcessingDurationBucketFactor float64
	ProcessingDurationBuckets      int
}

// ReadMain reads the main config
func ReadMain(r io.Reader) (Main, error) {
	cfg := MakeDefault()

	_, err := toml.DecodeReader(r, &cfg)
	if err != nil {
		return cfg, errors.Wrap(err, "parsing error")
	}
	if cfg.PprofPort == cfg.PromPort {
		return cfg, errors.New("PromPort and PprofPort can't have the same value")
	}
	if !cfg.EnableTCP && !cfg.EnableUDP {
		return cfg, errors.New("we don't listen neither on TCP nor on UDP")
	}
	return cfg, nil
}

// MakeDefault creates configuration with default values.
func MakeDefault() Main {
	return Main{
		ListeningPort: 2003,
		TargetPort:    2004,

		EnableTCP: true,

		MainQueueSize:  1000,
		HostQueueSize:  1000,
		WorkerPoolSize: 10,

		IncomingConnIdleTimeoutSec: 90,
		SendTimeoutSec:             5,
		OutConnTimeoutSec:          5,
		TermTimeoutSec:             10,
		TCPOutBufSize:              0,
		TCPOutBufFlushPeriodSec:    2,
		MaxHostReconnectPeriodMs:   5000,
		HostReconnectPeriodDeltaMs: 10,

		NormalizeRecords:  true,
		LogSpecialRecords: true,

		PprofPort: 6000,
		PromPort:  9090,

		HostQueueLengthBucketFactor: 3,
		HostQueueLengthBuckets:      10,

		ProcessingDurationBucketFactor: 2,
		ProcessingDurationBuckets:      10,
	}
}
