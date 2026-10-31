package conf

import (
	"strings"
	"testing"
)

func TestTCPCompressionConfig(t *testing.T) {
	for _, compression := range []string{"", "none", "zstd"} {
		c := MakeDefault()
		c.TCPOutCompression = compression
		if err := c.ValidateTCPCompression(); err != nil {
			t.Fatal(err)
		}
	}
	for _, field := range []string{"compression", "level", "size", "negative"} {
		c := MakeDefault()
		switch field {
		case "compression":
			c.TCPOutCompression = "gzip"
		case "level":
			c.TCPOutZstdLevel = "best"
		case "size":
			c.TCPOutZstdFrameSize = 17 << 20
		case "negative":
			c.TCPOutZstdFrameSize = -1
		}
		if err := c.ValidateTCPCompression(); err == nil {
			t.Fatalf("accepted bad %s", field)
		}
	}
	c, err := ReadMain(strings.NewReader("ClustersConfig='clusters.toml'\nRulesConfig='rules.toml'\nTCPOutCompression='zstd'"))
	if err != nil || c.TCPOutZstdFrameSize != 1<<20 || c.TCPOutZstdLevel != "fastest" {
		t.Fatalf("defaults: %v", err)
	}
	for _, value := range []string{"zstd", "none", "gzip"} {
		text := "[[cluster]]\nname='example'\ntype='toall'\ncompression='" + value + "'\n[[cluster.hosts]]\nname='127.0.0.1'\n[[cluster.hosts]]\nname='127.0.0.2'\ncompression='none'"
		clusters, err := ReadClustersConfig(strings.NewReader(text))
		if value == "gzip" {
			if err == nil {
				t.Fatal("accepted unknown cluster compression")
			}
			continue
		}
		if err != nil || clusters.Cluster[0].Hosts[0].Compression != value || clusters.Cluster[0].Hosts[1].Compression != "none" {
			t.Fatalf("inheritance: %v", err)
		}
	}
}
