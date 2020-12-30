package target

import (
	context "context"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/linegrpc"
	"github.com/bookingcom/nanotube/pkg/metrics"
	"google.golang.org/grpc"

	"go.uber.org/zap"
)

// HostGRPC represents a single target hosts to send records to.
type HostGRPC struct {
	Host
}

// NewHostGRPC builds new host object from config.
// TODO: Add logger tag to specify protocol
func NewHostGRPC(clusterName string, mainCfg conf.Main, hostCfg conf.Host, lg *zap.Logger, ms *metrics.Prom) *HostGRPC {
	h := HostGRPC{*NewHost(clusterName, mainCfg, hostCfg, lg, ms)}

	return &h
}

// Stream ...
// TODO: Add TLS option
func (h *HostGRPC) Stream(wg *sync.WaitGroup) {
	opts := []grpc.DialOption{
		grpc.WithInsecure(),
		grpc.WithBlock(),
	}

	// TODO: Add reconnection logic
	// TODO: Add add keepalive options
	conn, err := grpc.Dial(net.JoinHostPort(h.Name, strconv.Itoa(int(h.Port))), opts...)
	if err != nil {
		h.Lg.Warn("error dialing for connection", zap.Error(err))
	}
	defer func() {
		cerr := conn.Close()
		if cerr != nil {
			h.Lg.Error("failed to close connection to target", zap.Error(cerr))
		}
	}()

	client := linegrpc.NewMainClient(conn)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stream, err := client.Send(ctx)
	if err != nil {
		h.Lg.Warn("could not connect to ")
	}

	// TODO: Break streaming into chunks
	counter := 0
	for r := range h.Ch {
		pbR := linegrpc.Rec{
			Path: r.Path,
			Val:  r.Val,
			Time: r.Time,
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
