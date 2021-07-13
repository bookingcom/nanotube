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

	// Start NT as a metrics forwarder in k8s?
	K8sMode bool
	// The port to listen on when forwarding metrics from containers.
	K8sInjectPortTCP uint16
	// The label to use in order to turn on forwarding from a pod.
	// If left unset or "", all pods get metrics forwarding.
	K8sSwitchLabelKey string
	// K8s label value. Default is "yes". Unused if K8sSwitchLabelKey is "".
	K8sSwitchLabelVal string
	// The period for updating the containers for metrics forwarding in k8s.
	K8sContainerUpdPeriodSec int

	TargetPort uint16

	// empty string not to listen
	ListenTCP  string
	ListenUDP  string
	ListenGRPC string

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

	// GRPC target (client) params
	GRPCOutKeepAlivePeriodSec      uint32
	GRPCOutKeepAlivePingTimeoutSec uint32
	//	GRPCOutSendTimeoutSec          uint32
	GRPCOutBackoffMaxDelaySec   uint32
	GRPCOutMinConnectTimeoutSec uint32

	// GRPC listener (server) params
	GRPCListenMaxConnectionIdleSec     uint32
	GRPCListenMaxConnectionAgeSec      uint32
	GRPCListenMaxConnectionAgeGraceSec uint32
	GRPCListenTimeSec                  uint32
	GRPCListenTimeoutSec               uint32

	GRPCTracing bool

	NormalizeRecords  bool
	LogSpecialRecords bool

	// -1 turns off pprof server
	PprofPort           int
	PromPort            int
	LessMetrics         bool
	RegexDurationMetric bool

	PidFilePath string

	LogLimitInitial    int
	LogLimitThereafter int
	LogLimitWindowSec  int

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
	if cfg.ListenTCP == "" && cfg.ListenUDP == "" && cfg.ListenGRPC == "" {
		return cfg, errors.New("we don't listen one any port and protocol")
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

		K8sMode:                  false,
		K8sInjectPortTCP:         2003,
		K8sSwitchLabelKey:        "",
		K8sSwitchLabelVal:        "yes",
		K8sContainerUpdPeriodSec: 30,

		TargetPort: 2004,

		ListenTCP: ":2003",

		MainQueueSize:  1000,
		HostQueueSize:  1000,
		WorkerPoolSize: 10,

		IncomingConnIdleTimeoutSec: 90,
		SendTimeoutSec:             5,
		OutConnTimeoutSec:          5,
		MaxHostReconnectPeriodMs:   5000,
		HostReconnectPeriodDeltaMs: 10,
		KeepAliveSec:               1,
		TermTimeoutSec:             10,

		TCPOutBufSize:                    0,
		TCPOutBufFlushPeriodSec:          2,
		TCPOutConnectionRefreshPeriodSec: 0,

		GRPCOutKeepAlivePeriodSec:      5,
		GRPCOutKeepAlivePingTimeoutSec: 1,
		//		GRPCOutSendTimeoutSec:          20,
		GRPCOutBackoffMaxDelaySec:   30,
		GRPCOutMinConnectTimeoutSec: 5,

		GRPCListenMaxConnectionIdleSec:     1200,
		GRPCListenMaxConnectionAgeSec:      7200,
		GRPCListenMaxConnectionAgeGraceSec: 60,
		GRPCListenTimeSec:                  600,
		GRPCListenTimeoutSec:               20,

		GRPCTracing: true,

		NormalizeRecords:  true,
		LogSpecialRecords: false,

		PprofPort:           -1,
		PromPort:            9090,
		LessMetrics:         false,
		RegexDurationMetric: false,

		PidFilePath: "",

		LogLimitInitial:    10,
		LogLimitThereafter: 1000,
		LogLimitWindowSec:  1,

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
