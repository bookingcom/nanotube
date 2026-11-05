package target

import (
	"fmt"
	"math/bits"
	"sync"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/metrics"
	"github.com/prometheus/client_golang/prometheus"

	"go.uber.org/zap"
)

// Clusters are all clusters mapped by name.
type Clusters map[string]*Cluster

// Send continuously sends data to all clusters.
// Under the hood it delegates this task to separate clusters
// and manages the lifecycles.
//
// Returned chan is closed when everything was stopped.
func (cls Clusters) Send(finish chan struct{}) chan struct{} {
	done := make(chan struct{})

	go func() {
		var wg sync.WaitGroup
		wg.Add(len(cls))

		for _, c := range cls {
			c.Send(&wg, finish)
		}

		wg.Wait()
		close(done)
	}()

	return done
}

// NewClusters builds new set of clusters from config.
func NewClusters(mainCfg *conf.Main, cfg *conf.Clusters, lg *zap.Logger, ms *metrics.Prom) (Clusters, error) {
	// Reserve a share even for disconnected sockets. No connection can consume
	// another's allowance, and reconnects release their old state. Shares follow
	// each host's dictionary_bytes, so a host with a large name set (long names
	// over several sockets) can be given more without raising every socket's
	// allowance; equal requests still get equal shares.
	connections, requested := 0, uint64(0)
	for _, cc := range cfg.Cluster {
		for _, h := range cc.Hosts {
			if h.Stream {
				n := max(1, h.MTCP)
				connections += n
				requested += uint64(n) * uint64(h.StreamOptions.WithDefaults(mainCfg.TCPOutBufFlushPeriodSec).DictionaryBytes)
			}
		}
	}
	total := mainCfg.StreamDictionaryBytes
	if total == 0 {
		total = 1 << 30
	}
	if total < 0 || (connections > 0 && (total-total/2)/connections < 4096) {
		return nil, fmt.Errorf("StreamDictionaryBytes is too small for %d connections", connections)
	}
	if connections > 0 {
		if err := ms.Stream.ConfigureNames(total/2, mainCfg.StreamFullNames); err != nil {
			return nil, err
		}
	}
	sockets := uint64(total - total/2)
	share := func(want int) int {
		if requested <= sockets {
			return want
		}
		hi, lo := bits.Mul64(uint64(want), sockets)
		q, _ := bits.Div64(hi, lo, requested) // hi < requested: want ≤ requested and sockets < requested
		return int(q)
	}
	if connections > 0 {
		lg.Info("stream dictionary budget", zap.Int("connections", connections), zap.Uint64("socket_bytes", sockets), zap.Uint64("requested_bytes", requested))
	}
	cls := make(map[string]*Cluster)
	var err error
	for _, cc := range cfg.Cluster {
		cc.Hosts = append([]conf.Host(nil), cc.Hosts...)
		for i := range cc.Hosts {
			h := &cc.Hosts[i]
			if h.Stream {
				// Relay clients retain no larger logical replay batch than the
				// existing plaintext writer unless an operator opts in. Store
				// clusters keep their established frame-size defaults.
				if h.StreamOptions.MaxBytes == 0 && (cc.Type == conf.LB || cc.Type == conf.StickyCluster) {
					bytes := mainCfg.TCPOutBufSize
					if bytes <= 0 {
						bytes = 4096
					}
					h.StreamOptions.MaxBytes = min(bytes, 4<<20)
				}
				h.StreamOptions = h.StreamOptions.WithDefaults(mainCfg.TCPOutBufFlushPeriodSec)
				h.StreamOptions.DictionaryBytes = share(h.StreamOptions.DictionaryBytes)
				if h.StreamOptions.DictionaryBytes < 4096 {
					return nil, fmt.Errorf("StreamDictionaryBytes is too small: host %s gets %d dictionary bytes per connection", h.Name, h.StreamOptions.DictionaryBytes)
				}
			}
		}
		clusterCfg := *mainCfg
		if cc.Compression != "" {
			clusterCfg.TCPOutCompression = cc.Compression
		}
		cl := &Cluster{
			Name: cc.Name,
			Type: cc.Type,
		}
		switch cc.Type {
		case conf.JumpCluster:
			cl, err = getJumpCluster(cl, cc, clusterCfg, lg, ms)
		case conf.BlackholeCluster, conf.ToallCluster, conf.LB, conf.StickyCluster:
			for _, h := range cc.Hosts {
				cl.Hosts = append(cl.Hosts, NewHost(cc.Name, clusterCfg, h, lg, ms))
			}
		default:
			return cls, fmt.Errorf("incorrect cluster type %s for cluster %s",
				cl.Type, cl.Name)
		}
		if err != nil {
			return cls, err
		}
		if cc.Type == conf.StickyCluster {
			cl.sticky, err = newStickyCluster(cl, ms)
			if err != nil {
				return cls, err
			}
		}
		ms.NumberOfTargets.With(prometheus.Labels{"cluster": cl.Name}).Set(float64(len(cl.Hosts)))

		cls[cl.Name] = cl
	}

	return cls, err
}

func getJumpCluster(cl *Cluster, cc conf.Cluster, mainCfg conf.Main, lg *zap.Logger, ms *metrics.Prom) (*Cluster, error) {
	cl.Hosts = make([]Target, len(cc.Hosts))
	for _, h := range cc.Hosts {
		if cl.Hosts[h.Index] != nil {
			return cl, fmt.Errorf("duplicate index value, or index not set for a hashed cluster %s", cc.Name)
		}

		if h.Index >= len(cl.Hosts) {
			return cl, fmt.Errorf("host %s index %d out of bounds in cluster %s (cluster size %d)", h.Name, h.Index, cl.Name, len(cl.Hosts))
		}

		cl.Hosts[h.Index] = NewHost(cc.Name, mainCfg, h, lg, ms)
	}

	for _, hst := range cl.Hosts {
		if hst == nil {
			return cl, fmt.Errorf("not all host indexes were set for cluster %v", cc.Name)
		}
	}
	return cl, nil
}
