package target

import (
	"fmt"
	"testing"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/metrics"
	"go.uber.org/zap"
)

func TestRelayDictionaryBudgetShares(t *testing.T) {
	main := conf.MakeDefault()
	main.TCPInitialConnCheck = false
	main.StreamDictionaryBytes = 1 << 20
	cfg := &conf.Clusters{Cluster: []conf.Cluster{{Name: "example", Type: conf.ToallCluster}}}
	for i := range 9 {
		cfg.Cluster[0].Hosts = append(cfg.Cluster[0].Hosts, conf.Host{Name: fmt.Sprintf("sink-%d", i), Stream: true, MTCP: 10})
	}
	ms := metrics.New()
	clusters, err := NewClusters(&main, cfg, zap.NewNop(), ms)
	if err != nil {
		t.Fatal(err)
	}
	reserved := main.StreamDictionaryBytes / 2
	for _, target := range clusters["example"].Hosts {
		h := target.(*HostMTCP)
		reserved += h.NumMTCP * h.streamOptions.DictionaryBytes
		if h.streamNames != ms.Stream.NameTable(main.StreamDictionaryBytes/2, false) {
			t.Fatal("hosts did not share one name table")
		}
	}
	if reserved > main.StreamDictionaryBytes {
		t.Fatal("socket shares exceed relay byte budget")
	}
	if cfg.Cluster[0].Hosts[0].StreamOptions.DictionaryBytes != 0 {
		t.Fatal("construction mutated input configuration")
	}
	tooSmall := main
	tooSmall.StreamDictionaryBytes = 90 * 4096
	if _, err := NewClusters(&tooSmall, cfg, zap.NewNop(), metrics.New()); err == nil {
		t.Fatal("accepted socket shares below the encoder's minimum byte budget")
	}
	main.StreamDictionaryBytes *= 2
	if _, err := NewClusters(&main, cfg, zap.NewNop(), ms); err == nil {
		t.Fatal("changed global reservation without restarting its owner")
	}
}

func TestRelayPoolStreamBatchMatchesPlaintext(t *testing.T) {
	for _, kind := range []string{conf.LB, conf.StickyCluster, conf.ToallCluster} {
		for _, size := range []int{0, 4096, 65536} {
			for _, explicit := range []int{0, 32768} {
				main := conf.MakeDefault()
				main.TCPInitialConnCheck = false
				main.TCPOutBufSize = size
				cc := &conf.Clusters{Cluster: []conf.Cluster{{Name: "relays", Type: kind, Hosts: []conf.Host{{Name: "127.0.0.1", Stream: true, StreamOptions: conf.StreamOptions{MaxBytes: explicit}}}}}}
				cls, err := NewClusters(&main, cc, zap.NewNop(), metrics.New())
				if err != nil {
					t.Fatal(err)
				}
				want := explicit
				if want == 0 {
					want = 1 << 20
					if kind == conf.LB || kind == conf.StickyCluster {
						want = size
						if want <= 0 {
							want = 4096
						}
					}
				}
				if got := cls["relays"].Hosts[0].(*Host).streamOptions.MaxBytes; got != want {
					t.Fatalf("kind=%s size=%d explicit=%d got=%d want=%d", kind, size, explicit, got, want)
				}
			}
		}
	}
}
