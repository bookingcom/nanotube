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
	// Clusters config path. Mandatory.
	ClustersConfig string
	// Rules config path. Mandatory.
	RulesConfig string
	// Rewrites config path. Optional.
	RewritesConfig string

	// Launch NT as a k8s daemonset forwarding metrics from pods.
	K8sMode bool
	// Use filter pods by label?
	K8sUseK8sServer bool
	// Pods to inject for TCP listening on selected pods. Metics sent to that port will be forwarded
	// to the running daemon-set.
	K8sInjectPortTCP uint16
	// The label to use in order to turn on forwarding from a pod.
	K8sSwitchLabelKey string
	// K8s label value. Default is "enabled".
	K8sSwitchLabelVal string
	// The period for updating the containers for metrics forwarding in k8s.
	K8sContainerUpdPeriodSec int
	// Range of jitter applied when querying k8s API while looking for pods.
	// Jitter added will be [0, this_number]
	K8sObserveJitterRangeSec int

	// The default target port on receiver hosts. Can be overridden in the clusters setup.
	TargetPort uint16

	// Setting this to ip:port will enable TCP listening on specified address.
	// Use empty string to disable TCP listening.
	// Use empty ip to listen on all addresses.
	ListenTCP string
	// Setting this to ip:port will enable UDP listening on specified address.
	// Use the empty string to disable UDP listening.
	// Use the empty IP to listen on all addresses.
	ListenUDP string
	// Setting this to ip:port will enable gRPC listening on specified address.
	// Use the empty string to disable gRPC listening.
	// Use the empty IP to listen on all addresses.
	ListenGRPC string

	// The size on main queue between listen and routing. Refer to docs for details.
	MainQueueSize uint64
	// Each host has it's own queue that contains records to be sent to it. This is the size of it.
	// Refer to docs for more insight.
	HostQueueSize uint64
	// The size of records batch sent into main queue.
	MainQueueBatchSize uint64
	// Period to flush the buffer when making batches for the main q.
	BatchFlushPerdiodSec uint32

	// Number of workers processing main queue and putting records into host queues.
	// If 0, set to be GOMAXPROCS / 2.
	// Default: 0.
	WorkerPoolSize int

	// Timeout for dropping an incoming connection if no data is sent in.
	IncomingConnIdleTimeoutSec uint32

	// Timeout for sending data to target host via TCP connection.
	// Has to be > TCPOutBufFlushPeriodSec to allow enough time for the buffered records to be sent.
	SendTimeoutSec uint32
	// Timeout for connecting to a target host. Does not influence re-connections with exponential backoffs.
	OutConnTimeoutSec uint32

	MaxHostReconnectPeriodMs   uint32
	HostReconnectPeriodDeltaMs uint32
	KeepAliveSec               uint32
	// Time to wait for the processing pipeline to terminate when quitting. After this timeout is passed,
	// forced termination is done. This helps when graceful shutdown is stuck or slow.
	TermTimeoutSec uint16
	// The size of the buffer for sending data to a target host via a TCP connections.
	// 0 value turns off buffering
	TCPOutBufSize int
	// The period over which the out TCP buffer for the connections sending to target hosts is flushed.
	// This helps if the traffic is low and records get stuck in the buffer.
	// The value of zero means no flushing.
	// 0 value turns off flushing
	TCPOutBufFlushPeriodSec uint32

	// The period to refresh the outgoing TCP connection.
	// This solves the following problem. If nothing is sent for a prolonged period of time to a client
	// it may drop the connection because of read i/o timeout. This will go unnoticed until the first
	// write attempt that will fail and data will be lost. To prevent this situation and data loss, connection
	// can be dropped and established from scratch.
	// 0 = no connection refresh.
	TCPOutConnectionRefreshPeriodSec uint32
	// Perform connection check to target right away?
	TCPInitialConnCheck bool

	// Enable jitter when connecting to targets?
	TargetConnectionJitter bool
	// Minimal amplitude when adding jitter to target connections. Has to be >0.
	TargetConnectionJitterMinAmplitudeMs uint32

	// gRPC target (client) params
	// gRPC HTTP2 keepalive ping period https://github.com/grpc/grpc-go/blob/master/Documentation/keepalive.md
	GRPCOutKeepAlivePeriodSec      uint32
	GRPCOutKeepAlivePingTimeoutSec uint32
	// Max connect backoff period https://github.com/grpc/grpc/blob/master/doc/connection-backoff.md
	GRPCOutBackoffMaxDelaySec uint32
	// Min time for the connection to complete https://github.com/grpc/grpc/blob/master/doc/connection-backoff.md
	GRPCOutMinConnectTimeoutSec uint32

	// gRPC listener (server) params
	GRPCListenMaxConnectionIdleSec     uint32
	GRPCListenMaxConnectionAgeSec      uint32
	GRPCListenMaxConnectionAgeGraceSec uint32
	GRPCListenTimeSec                  uint32
	GRPCListenTimeoutSec               uint32

	GRPCTracing bool

	// Turns on and off the normalization of records path. Described in the docs in detail.
	NormalizeRecords bool
	// Turns on logging for special kinds of records. For now it's recrods with fractional timestamps.
	LogSpecialRecords bool

	// -1 turns off pprof server
	PprofPort int
	PromPort  int
	// Expose the Prometheus metrics locally.
	PromLocal bool
	// Switch to expose only small subset of essential metrics.
	// (Useful to reduce Prometheus load when running as a sidecar on many nodes in a large setup.)
	LessMetrics bool
	// Expose prometheus metrics with the total time for running each regex from config.
	// Can be used to understand what regexs from config are more 'expensive'
	RegexDurationMetric bool

	// Absolute path for a pidfile. Not written if left empty.
	PidFilePath string

	// Initial number of allowed log records during LogLimitWindowSec with same msg and level
	LogLimitInitial int
	// Log every LogLimitThereafter record during LogLimitWindowSec after initial set with same msg and level
	LogLimitThereafter int
	// Timeframe for log limiting
	LogLimitWindowSec int

	// Histogram parameters for the host queue size
	HostQueueLengthBucketFactor float64
	HostQueueLengthBuckets      int

	// Histogram parameters for the processing duration
	ProcessingDurationBucketFactor float64
	ProcessingDurationBuckets      int

	// Limits the number of records in the time window a container can send to maximum of
	// RateLimiterContainerRecordLimit records. Setting this to zero, disables container rate limiters.
	RateLimiterContainerRecordLimit int
	// Limits the number of records in the time window that generally can be sent to maximum of
	// RateLimiterGlobalRecordLimit records. Setting this to zero, disables the global rate limiter.
	RateLimiterGlobalRecordLimit int
	// Time window size of rate limiter.
	RateLimiterWindowSizeSec int
	// Record threshold for updating rate limiter in a reader. If set to zero or lower, rate limiter is
	// updated on each record
	RateLimiterPerReaderRecordThreshold int
	// Interval in which rate limiter is updated. If set equal or lower than zero, rate limiter updates only on record
	// threshold.
	RateLimiterIntervalMs int
	// Duration in which rate limiter hangs until trying again.
	RateLimiterRetryDurationMs int
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
	if cfg.TargetConnectionJitterMinAmplitudeMs == 0 {
		return cfg, errors.New("TargetConnectionJitterMinAmplitudeMs is 0")
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
		K8sUseK8sServer:          false,
		K8sInjectPortTCP:         2003,
		K8sSwitchLabelKey:        "graphite_tcp_port",
		K8sSwitchLabelVal:        "enabled",
		K8sContainerUpdPeriodSec: 30,
		K8sObserveJitterRangeSec: 10,

		TargetPort: 2004,

		ListenTCP: ":2003",

		MainQueueSize:        1000,
		HostQueueSize:        1000,
		MainQueueBatchSize:   1000,
		BatchFlushPerdiodSec: 5,

		WorkerPoolSize: 0,

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

		TargetConnectionJitterMinAmplitudeMs: 200,

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

		NormalizeRecords: true,

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

		RateLimiterWindowSizeSec:            10,
		RateLimiterIntervalMs:               1000,
		RateLimiterPerReaderRecordThreshold: 1000,
		RateLimiterRetryDurationMs:          100,
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

// ClustersHash calculates hash of the clusters config to track its versions.
func ClustersHash(clusters *Clusters) (string, error) {
	h := md5.New()
	_, err := h.Write([]byte(fmt.Sprintf("%v", clusters)))
	if err != nil {
		return "", fmt.Errorf("failed to calculate clusters config hash: %w", err)
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}
