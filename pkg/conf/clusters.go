// Package conf represents configuration of the clusters set.
package conf

import (
	"fmt"
	"io"

	"github.com/burntsushi/toml"
	"github.com/pkg/errors"
)

// Clusters is the config for all the clusters in the system.
type Clusters struct {
	Cluster []Cluster
}

// JumpCluster distributes the incoming datapoints between hosts according to
// fnv1a hasing following by the jump one. Every record is sent to only one host.
const JumpCluster string = "jump"

// LB cluster type distributes incoming datapoints using fnv modulo N, where
// N  equal no. of available hosts
// As such, this cluster type keeps the list of available hosts and sends traffic
// only to these
const LB string = "lb"

// StickyCluster uses endpoint-keyed rendezvous hashing with health-aware failover.
const StickyCluster string = "sticky"

// ToallCluster broadcasts records to all hosts in the cluster.
const ToallCluster string = "toall"

// BlackholeCluster is used for testing. It cannot be specified in config.
// It eats the records, they are not sent anywhere.
const BlackholeCluster string = "blackhole"

// Cluster is the config of a single cluster.
// Cluster type can be toall, jump, lb, or sticky.
type Cluster struct {
	Name        string `toml:"name"`
	Type        string `toml:"type"`
	Hosts       []Host `toml:"hosts"`
	Compression string `toml:"compression"`
}

// Host is the config of a single machine to send metrics to.
// The index is set explicitly for easy modification of config and
// to make it more robust.
// Port is optional, set to default if absent.
type Host struct {
	// Name must be an FQDN or IP.
	Name string `toml:"name"`
	// Only makes sense when there is hashing.
	Index int `toml:"index"`
	// Optional, can be taken from default config.
	Port uint16 `toml:"port"`
	// Optional, TCP by default
	GRPC bool `toml:"grpc"`
	// Optional, number of TCP connections to open to the host.
	MTCP        int    `toml:"mtcp"`
	Compression string `toml:"compression"`
	// Stream selects the go-carbon binary stream protocol on TCP connections.
	Stream        bool          `toml:"stream"`
	StreamOptions StreamOptions `toml:"stream_options"`
}

// StreamOptions bounds each connection's frames and name dictionary. Zero
// values select defaults; these settings have no effect on plaintext hosts.
type StreamOptions struct {
	MaxRows         int  `toml:"max_rows"`
	MaxBytes        int  `toml:"max_bytes"`
	FlushPeriodMS   int  `toml:"flush_period_ms"`
	Zstd            bool `toml:"zstd"`
	MaxNames        int  `toml:"max_names"`
	DictionaryBytes int  `toml:"dictionary_bytes"`
}

// WithDefaults returns effective stream settings. Stream always flushes at
// least once a second, even when periodic plaintext flushing is disabled.
func (s StreamOptions) WithDefaults(tcpFlushSeconds uint32) StreamOptions {
	if s.MaxRows == 0 {
		s.MaxRows = 4096
	}
	if s.MaxBytes == 0 {
		s.MaxBytes = 1 << 20
	}
	if s.FlushPeriodMS == 0 {
		s.FlushPeriodMS = 1000
	}
	if tcpFlushSeconds > 0 && uint64(s.FlushPeriodMS) > uint64(tcpFlushSeconds)*1000 {
		s.FlushPeriodMS = int(uint64(tcpFlushSeconds) * 1000)
	}
	if s.MaxNames == 0 {
		s.MaxNames = 4 << 20
	}
	if s.DictionaryBytes == 0 {
		s.DictionaryBytes = 12 << 20
	}
	return s
}

func (h Host) validateStream() error {
	s := h.StreamOptions
	if h.Stream && h.GRPC {
		return errors.New("stream and grpc are mutually exclusive")
	}
	if s.MaxRows < 0 || s.MaxRows > 64<<10 {
		return errors.New("stream max_rows must be between 0 and 65536")
	}
	if s.MaxBytes < 0 || s.MaxBytes > 4<<20 {
		return errors.New("stream max_bytes must be between 0 and 4194304")
	}
	if s.FlushPeriodMS < 0 || s.FlushPeriodMS > 1000 {
		return errors.New("stream flush_period_ms must be between 0 and 1000")
	}
	if s.DictionaryBytes < 0 || (s.DictionaryBytes > 0 && s.DictionaryBytes < 4096) {
		return errors.New("stream dictionary_bytes must be zero or at least 4096")
	}
	if s.MaxNames < 0 || uint64(s.MaxNames) > 1<<32-1 {
		return errors.New("stream max_names must fit a positive uint32")
	}
	if h.Stream && h.MTCP < 0 {
		return errors.New("stream mtcp cannot be negative")
	}
	return nil
}

// ReadClustersConfig reads clusters set from a reader.
func ReadClustersConfig(r io.Reader) (Clusters, error) {
	var cls Clusters
	_, err := toml.DecodeReader(r, &cls)
	if err != nil {
		return cls, errors.Wrap(err, "error while decoding")
	}

	if len(cls.Cluster) == 0 {
		return cls, errors.New("could not find any cluster in clusters configuration")
	}
	for idx, cluster := range cls.Cluster {
		if err := ValidateCompression(cluster.Compression); err != nil {
			return cls, err
		}
		for i := range cluster.Hosts {
			if err := ValidateCompression(cluster.Hosts[i].Compression); err != nil {
				return cls, err
			}
			if cluster.Hosts[i].Compression == "" {
				cls.Cluster[idx].Hosts[i].Compression = cluster.Compression
			}
		}
		if cluster.Name == "" {
			return cls, fmt.Errorf("cluster with index %d does not have a name", idx)
		}
		if cluster.Type == "" {
			return cls, fmt.Errorf("cluster with index %d does not have a type", idx)
		}
		if len(cluster.Hosts) == 0 && cluster.Type != BlackholeCluster {
			return cls, fmt.Errorf("cluster with index %d does not have any hosts", idx)
		}
		for _, host := range cluster.Hosts {
			if cluster.Type == StickyCluster && (host.Name == "" || host.GRPC) {
				return cls, fmt.Errorf("sticky cluster %s requires named TCP hosts", cluster.Name)
			}
			if err := host.validateStream(); err != nil {
				return cls, errors.Wrapf(err, "host %s in cluster %s", host.Name, cluster.Name)
			}
		}
		if cluster.Type == JumpCluster {
			inxs := map[int]bool{}
			for _, host := range cluster.Hosts {
				if host.Name == "" {
					return cls, fmt.Errorf("host %d in cluster does not have a name", idx)
				}

				if host.Index < 0 || host.Index >= len(cluster.Hosts) {
					return cls, fmt.Errorf("host %s index is %d; it is out of bounds in cluster %s", host.Name, host.Index, cluster.Name)
				}
				if inxs[host.Index] {
					return cls, fmt.Errorf("duplicate host index %d in cluster %s", host.Index, cluster.Name)
				}
				inxs[host.Index] = true
			}
		}
	}
	return cls, nil
}
