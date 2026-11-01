package conf

import (
	"strings"
	"testing"
)

func TestStickyConfiguration(t *testing.T) {
	for _, tc := range []struct {
		host  string
		valid bool
	}{
		{"name='relay.example.net'\nport=2003", true},
		{"name='relay.example.net'\nstream=true\nmtcp=2", true},
		{"name='relay.example.net'\ngrpc=true", false},
		{"port=2003", false},
	} {
		config := "[[cluster]]\nname='relays'\ntype='sticky'\n[[cluster.hosts]]\n" + tc.host
		_, err := ReadClustersConfig(strings.NewReader(config))
		if (err == nil) != tc.valid {
			t.Fatalf("%q: valid=%v, err=%v", tc.host, tc.valid, err)
		}
	}
}
