package metrics

import (
	"log"

	"github.com/bookingcom/nanotube/pkg/conf"

	"github.com/prometheus/client_golang/prometheus"
)

// Prom is the set of Prometheus metrics.
type Prom struct {
	InRecs         prometheus.Counter
	InRecsBySource *prometheus.CounterVec
	ThrottledRecs  prometheus.Counter

	StateChangeHosts          *prometheus.CounterVec
	StateChangeHostsTotal     prometheus.Counter
	OldConnectionRefresh      *prometheus.CounterVec
	OldConnectionRefreshTotal prometheus.Counter
	ThrottledHosts            *prometheus.CounterVec
	ThrottledHostsTotal       prometheus.Counter
	OutRecs                   *prometheus.CounterVec
	OutRecsTotal              prometheus.Counter

	RewrittenRecsTotal prometheus.Counter

	BlackholedRecs prometheus.Counter
	ErrorRecs      prometheus.Counter

	ActiveTCPConnections  prometheus.Gauge
	InConnectionsTotalTCP prometheus.Counter

	UDPReadFailures prometheus.Counter

	TargetStates             *prometheus.GaugeVec
	NumberOfAvailableTargets *prometheus.GaugeVec
	NumberOfTargets          *prometheus.GaugeVec
	HostQueueSize            *prometheus.GaugeVec

	GlobalRateLimiterBlockedReaders    prometheus.Counter
	ContainerRateLimiterBlockedReaders *prometheus.CounterVec

	K8sPickedUpContainers         prometheus.Counter
	K8sCurrentForwardedContainers prometheus.Gauge

	RegexDuration *prometheus.SummaryVec

	Version         *prometheus.CounterVec
	ConfVersion     *prometheus.CounterVec
	ClustersVersion *prometheus.CounterVec
}

// New creates a new set of metrics from the main config.
// This does not include metrics registration.
func New() *Prom {
	return &Prom{
		InRecs: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "nanotube",
			Name:      "in_records_total",
			Help:      "Incoming records counter.",
		}),
		InRecsBySource: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "nanotube",
			Name:      "in_records_by_source_total",
			Help:      "Incoming records counter by source.",
		}, []string{"source"}),
		OutRecs: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "nanotube",
			Name:      "out_records",
			Help:      "Outgoing records by cluster and hostname.",
		}, []string{"cluster", "upstream_host"}),
		OutRecsTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "nanotube",
			Name:      "out_records_total",
			Help:      "Total outgoing records.",
		}),
		RewrittenRecsTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "nanotube",
			Name:      "rewritten_records_total",
			Help:      "Number of records that matched at least one rewrite rule at least once.",
		}),
		ThrottledRecs: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "nanotube",
			Name:      "throttled_records_total",
			Help:      "Records dropped from the main queue because it's full.",
		}),
		ThrottledHosts: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "nanotube",
			Name:      "throttled_host_records",
			Help:      "Records dropped from the host queues because they're full labeled by cluster and host.",
		}, []string{"cluster", "upstream_host"}),
		ThrottledHostsTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "nanotube",
			Name:      "throttled_host_records_total",
			Help:      "Total records dropped from the host queues because it's full.",
		}),
		StateChangeHosts: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "nanotube",
			Name:      "state_change_hosts",
			Help:      "Availability state change for hosts",
		}, []string{"cluster", "upstream_host"}),
		StateChangeHostsTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "nanotube",
			Name:      "state_change_hosts_total",
			Help:      "Total availability state change for hosts",
		}),
		OldConnectionRefresh: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "nanotube",
			Name:      "old_connection_refresh",
			Help:      "Old connection refreshes per target host",
		}, []string{"cluster", "upstream_host"}),
		OldConnectionRefreshTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "nanotube",
			Name:      "old_connection_refresh_total",
			Help:      "Total old connection refreshes for target hosts",
		}),
		BlackholedRecs: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "nanotube",
			Name:      "blackholed_records_total",
			Help:      "Blackholed records counter.",
		}),
		ErrorRecs: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "nanotube",
			Name:      "error_records_total",
			Help:      "Records that we were not able to parse.",
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
		TargetStates: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "nanotube",
			Name:      "target_states",
			Help:      "The current states of target hosts.",
		}, []string{"upstream_host", "cluster"}),
		HostQueueSize: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "nanotube",
			Name:      "host_queue_size",
			Help:      "The current size of host queue.",
		}, []string{"upstream_host", "cluster"}),
		NumberOfAvailableTargets: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "nanotube",
			Name:      "number_of_available_targets",
			Help:      "Number of available targets in cluster.",
		}, []string{"cluster"}),
		NumberOfTargets: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "nanotube",
			Name:      "number_of_targets",
			Help:      "Number of targets by cluster as seen by LB. Only counted for LB clusters.",
		}, []string{"cluster"}),
		GlobalRateLimiterBlockedReaders: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "nanotube",
			Name:      "global_rate_limiter_blocked_readers",
			Help:      "Number of readers that global rate limiter has blocked.",
		}),
		ContainerRateLimiterBlockedReaders: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "nanotube",
			Name:      "container_rate_limiter_blocked_readers",
			Help:      "Number of readers that container rate limiter has blocked.",
		}, []string{"container_id"}),
		K8sPickedUpContainers: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "nanotube",
			Name:      "k8s_picked_up_containers_total",
			Help:      "The total number of containers that forwarding has started from. If container blips, it's counted twice.",
		}),
		K8sCurrentForwardedContainers: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "nanotube",
			Name:      "k8s_current_forwarded_containers",
			Help:      "The number of current containers that have their metrics forwarded.",
		}),
		RegexDuration: prometheus.NewSummaryVec(prometheus.SummaryOpts{
			Namespace: "nanotube",
			Name:      "regex_duration_seconds",
			Help:      "Time to evaluate each regex from configuration",
		}, []string{"regex", "rule_type"}),
		Version: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "nanotube",
			Name:      "version",
			Help:      "Version info in label. Value should be always 1.",
		}, []string{"version"}),
		ConfVersion: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "nanotube",
			Name:      "conf_version",
			Help:      "Config version in label. Value should be always 1.",
		}, []string{"conf_version"}),
		ClustersVersion: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "nanotube",
			Name:      "clusters_version",
			Help:      "Clusters config version in label. Value should be always 1.",
		}, []string{"clusters_version"}),
	}
}

// Register registers the metrics. It fatally fails and exits if metrics fail to register.
// Meant to be called from main and fail completely if something goes wrong.
func Register(m *Prom, cfg *conf.Main) {
	err := prometheus.Register(m.InRecs)
	if err != nil {
		log.Fatalf("error registering the in_records_total metric: %v", err)
	}

	err = prometheus.Register(m.OutRecsTotal)
	if err != nil {
		log.Fatalf("error registering the out_records_total metric: %v", err)
	}

	err = prometheus.Register(m.RewrittenRecsTotal)
	if err != nil {
		log.Fatalf("error registering the rewritten_records_total metric: %v", err)
	}

	err = prometheus.Register(m.ErrorRecs)
	if err != nil {
		log.Fatalf("error registering the error_records_counter metric: %v", err)
	}

	err = prometheus.Register(m.ThrottledRecs)
	if err != nil {
		log.Fatalf("error registering the throttled_records_counter metric: %v", err)
	}

	err = prometheus.Register(m.ThrottledHostsTotal)
	if err != nil {
		log.Fatalf("error registering the throttled_host_records_total metric: %v", err)
	}

	err = prometheus.Register(m.BlackholedRecs)
	if err != nil {
		log.Fatalf("error registering the blackholed_records_counter metric: %v", err)
	}

	err = prometheus.Register(m.InConnectionsTotalTCP)
	if err != nil {
		log.Fatalf("error registering the host_queue_length_hist metric: %v", err)
	}

	err = prometheus.Register(m.StateChangeHostsTotal)
	if err != nil {
		log.Fatalf("error registering the state_change_hosts_total metric: %v", err)
	}

	err = prometheus.Register(m.OldConnectionRefreshTotal)
	if err != nil {
		log.Fatalf("error registering the old_connection_refresh metric: %v", err)
	}

	err = prometheus.Register(m.Version)
	if err != nil {
		log.Fatalf("error registering the version metric: %v", err)
	}

	err = prometheus.Register(m.ConfVersion)
	if err != nil {
		log.Fatalf("error registering the conf version metric: %v", err)
	}

	err = prometheus.Register(m.ClustersVersion)
	if err != nil {
		log.Fatalf("error registering the clusters_version metric: %v", err)
	}

	err = prometheus.Register(m.GlobalRateLimiterBlockedReaders)
	if err != nil {
		log.Fatalf("error while registering global_rate_limiter_blocking metric: %v", err)
	}

	if !cfg.LessMetrics {
		err = prometheus.Register(m.OutRecs)
		if err != nil {
			log.Fatalf("error registering the out_records metric: %v", err)
		}

		err = prometheus.Register(m.InRecsBySource)
		if err != nil {
			log.Fatalf("error registering the in_records_by_source_total metric: %v", err)
		}

		err = prometheus.Register(m.StateChangeHosts)
		if err != nil {
			log.Fatalf("error registering the state_change_hosts metric: %v", err)
		}

		err = prometheus.Register(m.OldConnectionRefresh)
		if err != nil {
			log.Fatalf("error registering the old_connection_refresh_total metric: %v", err)
		}

		err = prometheus.Register(m.ThrottledHosts)
		if err != nil {
			log.Fatalf("error registering the throttled_host_records metric: %v", err)
		}

		err = prometheus.Register(m.ActiveTCPConnections)
		if err != nil {
			log.Fatalf("error registering the host_queue_length_hist metric: %v", err)
		}

		err = prometheus.Register(m.UDPReadFailures)
		if err != nil {
			log.Fatalf("error registering the udp_read_failures_total metric: %v", err)
		}

		err = prometheus.Register(m.TargetStates)
		if err != nil {
			log.Fatalf("error while registering target_states metric: %v", err)
		}

		err = prometheus.Register(m.HostQueueSize)
		if err != nil {
			log.Fatalf("error while registering target_states metric: %v", err)
		}

		err = prometheus.Register(m.NumberOfAvailableTargets)
		if err != nil {
			log.Fatalf("error while registering number_of_available_targets metric: %v", err)
		}

		err = prometheus.Register(m.NumberOfTargets)
		if err != nil {
			log.Fatalf("error while registering number_of_targets metric: %v", err)
		}

		err = prometheus.Register(m.ContainerRateLimiterBlockedReaders)
		if err != nil {
			log.Fatalf("error while registering container_rate_limiter_blocking metric: %v", err)
		}

		err = prometheus.Register(m.K8sPickedUpContainers)
		if err != nil {
			log.Fatalf("error registering the k8s_picked_up_containers_total metric: %v", err)
		}

		err = prometheus.Register(m.K8sCurrentForwardedContainers)
		if err != nil {
			log.Fatalf("error registering the k8s_current_forwarded_containers metric: %v", err)
		}

		if cfg.RegexDurationMetric {
			err = prometheus.Register(m.RegexDuration)
			if err != nil {
				log.Fatalf("error register the RegexDuration metric")
			}
		}
	}

}
