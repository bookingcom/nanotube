package target

import (
	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/metrics"
	"go.uber.org/zap"
)

// HostTCP will represent TCP host
// It's a stub for now, it's role is currently carried out by Host struct
type HostTCP struct {
	Host
}

// NewHostTCP ...
func NewHostTCP(clusterName string, mainCfg conf.Main, hostCfg conf.Host, lg *zap.Logger, ms *metrics.Prom) *HostTCP {
	return &HostTCP{*NewHost(clusterName, mainCfg, hostCfg, lg, ms)}
}
