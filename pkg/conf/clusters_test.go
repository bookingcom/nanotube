package conf

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestClustersSimple(t *testing.T) {
	clsConfig :=
		`[[cluster]]
		name = "aaa"
		type = "jump"
			[[cluster.hosts]]
			name = "host1"
			index = 0
			port = 123
			[[cluster.hosts]]
			name = "host2"
			index = 1
			port = 456

		[[cluster]]
		name = "bbb"
		type = "jump"
			[[cluster.hosts]]
			name = "host11"
			index = 0
			port = 234
			[[cluster.hosts]]
			name = "host12"
			index = 1
			port = 567`

	expected := Clusters{
		Cluster: []Cluster{
			{
				Name: "aaa",
				Type: "jump",
				Hosts: []Host{
					{
						Name:  "host1",
						Index: 0,
						Port:  123,
					},
					{
						Name:  "host2",
						Index: 1,
						Port:  456,
					},
				},
			},
			{
				Name: "bbb",
				Type: "jump",
				Hosts: []Host{
					{
						Name:  "host11",
						Index: 0,
						Port:  234,
					},
					{
						Name:  "host12",
						Index: 1,
						Port:  567,
					},
				},
			},
		},
	}

	cls, err := ReadClustersConfig(strings.NewReader(clsConfig))
	if err != nil {
		t.Errorf("reading config failed with error : %v", err)
	}

	if diff := cmp.Diff(expected, cls); diff != "" {
		t.Errorf("Expected and factual configs differ:\n%s", diff)
	}
}
