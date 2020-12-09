package conf

import (
	"crypto/md5"
	"fmt"
	"io"
	"path/filepath"

	"github.com/burntsushi/toml"
	"github.com/pkg/errors"
)

// Main is the main and generic nanotube config.
type Main struct {
	ClustersConfig string
	RulesConfig    string
	RewritesConfig string

	TargetPort uint16

	// empty string not to listen
	ListenTCP string
	ListenUDP string

	// 0 does not set buffer size
	// UDPOSBufferSize uint32

	MainQueueSize uint64
	HostQueueSize uint64

	WorkerPoolSize uint16

	IncomingConnIdleTimeoutSec uint32
	SendTimeoutSec             uint32
	OutConnTimeoutSec          uint32
	MaxHostReconnectPeriodMs   uint32
	HostReconnectPeriodDeltaMs uint32
	KeepAliveSec               uint32
	TermTimeoutSec             uint16
	// 0 value turns off buffering
	TCPOutBufSize int
	// 0 value turns off flushing
	TCPOutBufFlushPeriodSec uint32
	// 0 value turns off connection refresh
	TCPOutConnectionRefreshPeriodSec uint32

	NormalizeRecords  bool
	LogSpecialRecords bool

	// -1 turns off pprof server
	PprofPort           int
	PromPort            int
	LessMetrics         bool
	RegexDurationMetric bool

	PidFilePath string

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
	if cfg.ClustersConfig == "" {
		return cfg, errors.New("missing mandatory ClustersConfig setting")
	}
	if cfg.RulesConfig == "" {
		return cfg, errors.New("missing mandatory RulesConfig setting")
	}
	if cfg.PprofPort != -1 && cfg.PprofPort == cfg.PromPort {
		return cfg, errors.New("PromPort and PprofPort can't have the same value")
	}
	if cfg.ListenTCP == "" && cfg.ListenUDP == "" {
		return cfg, errors.New("we don't listen neither on TCP nor on UDP")
	}
	if cfg.SendTimeoutSec <= cfg.TCPOutBufFlushPeriodSec {
		return cfg, errors.New("TCP send timeout is lesser or equal to TCP buffer flush period")
	}
	if cfg.PidFilePath != "" {
		if !filepath.IsAbs(cfg.PidFilePath) {
			return cfg, errors.New("pidfile path is not valid or not an absolute path")
		}
	}

	return cfg, nil
}

// MakeDefault creates configuration with default values.
func MakeDefault() Main {
	return Main{
		ClustersConfig: "",
		RulesConfig:    "",
		RewritesConfig: "",

		TargetPort: 2004,

		ListenTCP: ":2003",

		MainQueueSize:  1000,
		HostQueueSize:  1000,
		WorkerPoolSize: 10,

		IncomingConnIdleTimeoutSec:       90,
		SendTimeoutSec:                   5,
		OutConnTimeoutSec:                5,
		MaxHostReconnectPeriodMs:         5000,
		HostReconnectPeriodDeltaMs:       10,
		KeepAliveSec:                     1,
		TermTimeoutSec:                   10,
		TCPOutBufSize:                    0,
		TCPOutBufFlushPeriodSec:          2,
		TCPOutConnectionRefreshPeriodSec: 0,

		NormalizeRecords:  true,
		LogSpecialRecords: false,

		PprofPort:           -1,
		PromPort:            9090,
		LessMetrics:         false,
		RegexDurationMetric: false,

		PidFilePath: "",

		HostQueueLengthBucketFactor: 3,
		HostQueueLengthBuckets:      10,

		ProcessingDurationBucketFactor: 2,
		ProcessingDurationBuckets:      10,
	}
}

// Hash calculates hash of all the configs to track config versions.
func Hash(cfg *Main, clusters *Clusters, rules *Rules, rewrites *Rewrites) (string, error) {
	h := md5.New()
	_, err := h.Write([]byte(fmt.Sprintf("%v%v%v%v", cfg, clusters, rules, rewrites)))
	if err != nil {
		return "", fmt.Errorf("failed to calculate config hash: %w", err)
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}
