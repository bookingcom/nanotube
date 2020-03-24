package metrics

import (
	"log"

	"github.com/bookingcom/nanotube/pkg/conf"

	"github.com/prometheus/client_golang/prometheus"
)

// Prom is the set of Prometheus metrics.
type Prom struct {
	InRecs         prometheus.Counter
	OutRecs        *prometheus.CounterVec
	ThrottledRecs  prometheus.Counter
	BlackholedRecs prometheus.Counter
	ErrorRecs      prometheus.Counter

	ThrottledHosts *prometheus.CounterVec

	MainQueueLength prometheus.Gauge
	HostQueueLength prometheus.Histogram

	ProcessingDuration prometheus.Histogram

	ActiveTCPConnections  prometheus.Gauge
	InConnectionsTotalTCP prometheus.Counter

	UDPReadFailures prometheus.Counter

	Version *prometheus.CounterVec
}

// New creates a new set of metrics from the main config.
// This does not include metrics registration.
func New(conf *conf.Main) *Prom {
	return &Prom{
		InRecs: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "nanotube",
			Name:      "in_records_total",
			Help:      "Incoming records counter.",
		}),
		OutRecs: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "nanotube",
			Name:      "out_records_total",
			Help:      "Outgoing records by cluster and hostname.",
		}, []string{"cluster", "upstream_host"}),
		ThrottledRecs: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "nanotube",
			Name:      "throttled_records_total",
			Help:      "Records dropped from the main queue because it's full.",
		}),
		ThrottledHosts: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "nanotube",
			Name:      "throttled_host_records_total",
			Help:      "Records dropped from the host queue because it's full.",
		}, []string{"cluster", "upstream_host"}),
		BlackholedRecs: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "nanotube",
			Name:      "blackholed_records_total",
			Help:      "Black holed records counter.",
		}),
		ErrorRecs: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "nanotube",
			Name:      "error_records_total",
			Help:      "Records that we were not able to parse.",
		}),
		MainQueueLength: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "nanotube",
			Name:      "main_queue_length",
			Help:      "The length of the main queue. Updated every second.",
		}),
		HostQueueLength: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: "nanotube",
			Name:      "host_queue_length",
			Help:      "The histogram of the lengths of the host queues.",
			Buckets:   prometheus.ExponentialBuckets(1, conf.HostQueueLengthBucketFactor, conf.HostQueueLengthBuckets),
		}),
		ProcessingDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: "nanotube",
			Name:      "processing_duration_seconds",
			Help:      "Time to process one record.",
			Buckets:   prometheus.ExponentialBuckets(0.001, conf.ProcessingDurationBucketFactor, conf.ProcessingDurationBuckets),
		}),
		ActiveTCPConnections: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "nanotube",
			Name:      "active_connections",
			Help:      "Number of active connections.",
		}),
		InConnectionsTotalTCP: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "nanotube",
			Name:      "open_in_connections_total",
			Help:      "Number of incoming connections.",
		}),
		UDPReadFailures: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "nanotube",
			Name:      "udp_read_failures_total",
			Help:      "Counter of failures when reading incoming data from the UDP connection.",
		}),
		Version: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "nanotube",
			Name:      "version",
			Help:      "Version info in label. Value should be always 1.",
		}, []string{"version"}),
	}
}

// Register registers the metrics. It fatally fails and exits if metrics fail to register.
// Meant to be called from main and fail completely if something goes wrong.
func Register(m *Prom) {
	err := prometheus.Register(m.InRecs)
	if err != nil {
		log.Fatalf("error registering the in_records_counter metric: %v", err)
	}

	err = prometheus.Register(m.OutRecs)
	if err != nil {
		log.Fatalf("error registering the out_records_counter metric: %v", err)
	}

	err = prometheus.Register(m.ThrottledRecs)
	if err != nil {
		log.Fatalf("error registering the throttled_records_counter metric: %v", err)
	}

	err = prometheus.Register(m.ThrottledHosts)
	if err != nil {
		log.Fatalf("error registering the throttled_host_records_total metrics: %v", err)
	}

	err = prometheus.Register(m.BlackholedRecs)
	if err != nil {
		log.Fatalf("error registering the blackholed_records_counter metric: %v", err)
	}

	err = prometheus.Register(m.ErrorRecs)
	if err != nil {
		log.Fatalf("error registering the error_records_counter metric: %v", err)
	}

	err = prometheus.Register(m.MainQueueLength)
	if err != nil {
		log.Fatalf("error registering the main_queue_length_hist metric: %v", err)
	}

	err = prometheus.Register(m.HostQueueLength)
	if err != nil {
		log.Fatalf("error registering the host_queue_length_hist metric: %v", err)
	}

	err = prometheus.Register(m.ProcessingDuration)
	if err != nil {
		log.Fatalf("error registering the host_queue_length_hist metric: %v", err)
	}

	err = prometheus.Register(m.ActiveTCPConnections)
	if err != nil {
		log.Fatalf("error registering the host_queue_length_hist metric: %v", err)
	}

	err = prometheus.Register(m.InConnectionsTotalTCP)
	if err != nil {
		log.Fatalf("error registering the host_queue_length_hist metric: %v", err)
	}

	err = prometheus.Register(m.UDPReadFailures)
	if err != nil {
		log.Fatalf("error registering the udp_read_failures_total metric: %v", err)
	}

	err = prometheus.Register(m.Version)
	if err != nil {
		log.Fatalf("error registering the version metric: %v", err)
	}
}
