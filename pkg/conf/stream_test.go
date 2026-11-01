package conf

import (
	"fmt"
	"strings"
	"testing"
)

func TestStreamConfig(t *testing.T) {
	prefix := "[[cluster]]\nname='example'\ntype='toall'\n[[cluster.hosts]]\nname='localhost'\n"
	for _, tc := range []struct {
		options string
		valid   bool
	}{
		{"stream=true", true},
		{"stream=true\nmtcp=2\n[cluster.hosts.stream_options]\nmax_rows=128\nmax_bytes=65536\nflush_period_ms=100\nzstd=true\nmax_names=32", true},
		{"stream=true\ngrpc=true", false},
		{"stream=true\nmtcp=-1", false},
		{"stream=true\n[cluster.hosts.stream_options]\nmax_rows=-1", false},
		{"stream=true\n[cluster.hosts.stream_options]\nmax_rows=65536", true},
		{"stream=true\n[cluster.hosts.stream_options]\nmax_rows=65537", false},
		{"stream=true\n[cluster.hosts.stream_options]\nmax_bytes=4194305", false},
		{"stream=true\n[cluster.hosts.stream_options]\nflush_period_ms=1001", false},
		{"stream=true\n[cluster.hosts.stream_options]\nmax_names=-1", false},
		{"stream=true\n[cluster.hosts.stream_options]\npeer_dictionary_bytes=-1", false},
		{"stream=true\n[cluster.hosts.stream_options]\npeer_dictionary_bytes=0", true},
		{"stream=true\n[cluster.hosts.stream_options]\npeer_dictionary_bytes=1048575", false},
		{"stream=true\n[cluster.hosts.stream_options]\npeer_dictionary_bytes=1048576", true},
		{"stream=true\n[cluster.hosts.stream_options]\npeer_dictionary_bytes=536870912", true},
		{"stream=true\n[cluster.hosts.stream_options]\npeer_dictionary_bytes=1073741824", true},
		{"stream=true\n[cluster.hosts.stream_options]\npeer_dictionary_bytes=1073741825", false},
	} {
		t.Run(tc.options, func(t *testing.T) {
			c, err := ReadClustersConfig(strings.NewReader(prefix + tc.options))
			if (err == nil) != tc.valid {
				t.Fatalf("config valid=%v: %v", tc.valid, err)
			}
			if err == nil && !c.Cluster[0].Hosts[0].Stream {
				t.Fatal("stream not enabled")
			}
		})
	}
}

func TestStreamDefaults(t *testing.T) {
	for _, seconds := range []uint32{0, 1, 2} {
		t.Run(fmt.Sprint(seconds), func(t *testing.T) {
			s := (StreamOptions{}).WithDefaults(seconds)
			if s.MaxRows != 4096 || s.MaxBytes != 1<<20 || s.FlushPeriodMS != 1000 || s.MaxNames != 4<<20 || s.Zstd || s.DictionaryBytes != 12<<20 || s.PeerDictionaryBytes != 64<<20 {
				t.Fatalf("unexpected defaults: %+v", s)
			}
		})
	}
	s := StreamOptions{MaxRows: 10, MaxBytes: 2000, FlushPeriodMS: 50, Zstd: true, MaxNames: 3, DictionaryBytes: 4096, PeerDictionaryBytes: 256 << 20}
	if got := s.WithDefaults(2); got != s {
		t.Fatalf("explicit options changed: %+v", got)
	}
}

func TestClusterStreamInheritance(t *testing.T) {
	for _, kind := range []string{LB, StickyCluster} {
		c, err := ReadClustersConfig(strings.NewReader("[[cluster]]\nname='relays'\ntype='" + kind + "'\nstream=true\n[cluster.stream_options]\nzstd=true\ndictionary_bytes=8192\npeer_dictionary_bytes=1048576\n[[cluster.hosts]]\nname='127.0.0.1'\n[[cluster.hosts]]\nname='127.0.0.2'\n[cluster.hosts.stream_options]\ndictionary_bytes=16384\n"))
		if err != nil {
			t.Fatal(err)
		}
		for i, h := range c.Cluster[0].Hosts {
			if !h.Stream || !h.StreamOptions.Zstd || h.StreamOptions.PeerDictionaryBytes != 1<<20 || h.StreamOptions.DictionaryBytes != 8192*(i+1) {
				t.Fatalf("host %d: %+v", i, h)
			}
		}
	}
	_, err := ReadClustersConfig(strings.NewReader("[[cluster]]\nname='relays'\ntype='lb'\nstream=true\n[[cluster.hosts]]\nname='127.0.0.1'\ngrpc=true\n"))
	if err == nil {
		t.Fatal("inherited stream must reject grpc")
	}
}
