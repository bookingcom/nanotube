package target

import (
	context "context"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/grpcstreamer"
	"github.com/bookingcom/nanotube/pkg/metrics"
	otmetrics "github.com/bookingcom/nanotube/pkg/opentelemetry/proto/metrics/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/keepalive"

	"go.uber.org/zap"
)

// HostGRPC represents a single target hosts to send records to.
type HostGRPC struct {
	Host
}

// NewHostGRPC builds new host object from config.
// TODO: Add logger tag to specify protocol
func NewHostGRPC(clusterName string, mainCfg conf.Main, hostCfg conf.Host, lg *zap.Logger, ms *metrics.Prom) *HostGRPC {
	grpc.EnableTracing = mainCfg.GRPCTracing

	h := HostGRPC{*ConstructHost(clusterName, mainCfg, hostCfg, lg, ms)}

	return &h
}

// Stream ...
func (h *HostGRPC) Stream(wg *sync.WaitGroup) {
	defer wg.Done()

	kacp := keepalive.ClientParameters{
		// period to send HTTP2 pings if there is no activity
		Time: time.Duration(h.conf.GRPCOutKeepAlivePeriodSec) * time.Second,
		// wait time for ping ack before considering the connection dead
		Timeout: time.Duration(h.conf.GRPCOutKeepAlivePingTimeoutSec) * time.Second,
		// send pings even without active streams
		PermitWithoutStream: true,
	}

	// TODO: Mainain correct live status based on gRPC connection health.

	// Dial does not timeout. This is intentional since gRPC will keep trying. (check that)
	conn, err := grpc.Dial(net.JoinHostPort(h.Name, strconv.Itoa(int(h.Port))),
		grpc.WithInsecure(),
		grpc.WithBlock(),
		grpc.WithKeepaliveParams(kacp),
		grpc.WithConnectParams(grpc.ConnectParams{
			Backoff: backoff.Config{
				MaxDelay: time.Duration(h.conf.GRPCOutBackoffMaxDelaySec) * time.Second,
			},
			MinConnectTimeout: time.Duration(h.conf.GRPCOutMinConnectTimeoutSec) * time.Second,
		}),
	)
	if err != nil {
		h.Lg.Warn("error dialing for connection", zap.Error(err))
	}
	defer func() {
		cerr := conn.Close()
		if cerr != nil {
			h.Lg.Error("failed to close connection to target", zap.Error(cerr))
		}
	}()

	client := grpcstreamer.NewStreamerClient(conn)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	counter := 0
	var stream grpcstreamer.Streamer_StreamClient
	for r := range h.Ch {
		if stream == nil {
			stream, err = client.Stream(ctx)
			if err != nil {
				h.Lg.Warn("could not start streaming to target", zap.Error(err))
			}
		}

		if stream != nil {
			pbR := otmetrics.Metric{
				Name: string(r.Path),
				Data: &otmetrics.Metric_DoubleGauge{
					DoubleGauge: &otmetrics.DoubleGauge{
						DataPoints: [](*otmetrics.DoubleDataPoint){
							&otmetrics.DoubleDataPoint{
								TimeUnixNano: uint64(r.Time) * 1000 * 1000 * 1000,
								Value:        r.Val,
							},
						},
					},
				},
			}
			err := stream.Send(&pbR)
			if err != nil {
				h.Lg.Warn("error while streaming", zap.Error(err))
				stream = nil
			}
			counter++
		}
	}

	if stream != nil {
		summary, err := stream.CloseAndRecv()
		if err != nil {
			h.Lg.Error("error closing the connection to target", zap.Error(err))
		}
		// TODO: Replace w/ metrics
		h.Lg.Info("GRPC summary",
			zap.Int("sent", counter),
			zap.Int("received by server", int(summary.GetReceivedCount())))

	}

}
