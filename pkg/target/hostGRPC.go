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
	"google.golang.org/grpc"
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
	grpc.EnableTracing = true

	h := HostGRPC{*NewHost(clusterName, mainCfg, hostCfg, lg, ms)}

	return &h
}

// Stream ...
// TODO: Add TLS option
func (h *HostGRPC) Stream(wg *sync.WaitGroup) {

	// TODO: Add reconnection logic

	kacp := keepalive.ClientParameters{
		// period to send HTTP2 pings if there is no activity
		Time: time.Duration(h.conf.GRPCOutKeepAlivePeriodSec) * time.Second,
		// wait time for ping ack before considering the connection dead
		Timeout: time.Duration(h.conf.GRPCOutKeepAlivePingTimeoutSec) * time.Second,
		// send pings even without active streams
		PermitWithoutStream: true,
	}

	conn, err := grpc.Dial(net.JoinHostPort(h.Name, strconv.Itoa(int(h.Port))),
		grpc.WithInsecure(),
		grpc.WithBlock(),
		grpc.WithKeepaliveParams(kacp))
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

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(h.conf.GRPCOutSendTimeoutSec)*time.Second)
	defer cancel()
	stream, err := client.Stream(ctx)
	if err != nil {
		h.Lg.Warn("could not start streaming to target", zap.Error(err))
	}

	// TODO: Break streaming into chunks
	counter := 0
	for r := range h.Ch {
		pbR := grpcstreamer.Rec{
			Rec: []byte(r.Serialize()),
		}
		err := stream.Send(&pbR)
		if err != nil {
			h.Lg.Warn("error while streaming", zap.Error(err))
		}
		counter++
	}

	// TODO: This will be reached when host is stopped
	summary, err := stream.CloseAndRecv()
	if err != nil {
		h.Lg.Error("error closing the connection to target", zap.Error(err))
	}

	// TODO: Replace w/ metrics
	h.Lg.Info("GRPC summary",
		zap.Int("sent", counter),
		zap.Int("received by server", int(summary.GetReceivedCount())))
}
