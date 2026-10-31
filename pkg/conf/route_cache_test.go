package conf

import (
	"fmt"
	"strings"
	"testing"
)

func TestRouteCacheBudget(t *testing.T) {
	for _, limit := range []uint64{0, 1, 65535, 65536, 2147483648, ^uint64(0)} {
		_, err := ReadMain(strings.NewReader(fmt.Sprintf("ClustersConfig='clusters.toml'\nRulesConfig='rules.toml'\nRouteCacheBytes=%d\n", limit)))
		valid := limit == 0 || limit >= 65536 && limit <= uint64(^uint(0)>>1)
		if (err == nil) != valid {
			t.Errorf("limit %d: %v", limit, err)
		}
	}
}
