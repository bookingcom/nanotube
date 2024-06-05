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

// ToallCluster broadcasts records to all hosts in the cluster.
const ToallCluster string = "toall"

// BlackholeCluster is used for testing. It cannot be specified in config.
// It eats the records, they are not sent anywhere.
const BlackholeCluster string = "blackhole"

// Cluster is the config of a single cluster.
// Cluster type can be either *toall* or *jump*.
type Cluster struct {
	Name  string `toml:"name"`
	Type  string `toml:"type"`
	Hosts []Host `toml:"hosts"`
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
	// Optional,
	MTCP int `toml:"mtcp"`
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
		if cluster.Name == "" {
			return cls, fmt.Errorf("cluster with index %d does not have a name", idx)
		}
		if cluster.Type == "" {
			return cls, fmt.Errorf("cluster with index %d does not have a type", idx)
		}
		if len(cluster.Hosts) == 0 && cluster.Type != BlackholeCluster {
			return cls, fmt.Errorf("cluster with index %d does not have any hosts", idx)
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
