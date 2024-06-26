package in

import (
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/grpcstreamer"
	"github.com/bookingcom/nanotube/pkg/metrics"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"
)

// ListenGRPC listens for incoming gRPC connections.
// Blocking. Returns when server returns.
func ListenGRPC(l net.Listener, queue chan []byte, stop <-chan struct{}, connWG *sync.WaitGroup, cfg *conf.Main, ms *metrics.Prom, lg *zap.Logger) {
	grpc.EnableTracing = cfg.GRPCTracing

	s := streamerServer{
		queue: queue,
		stop:  stop,

		lg: lg,
		ms: ms,
	}
	gRPCServer := grpc.NewServer(
		grpc.KeepaliveParams(keepalive.ServerParameters{
			MaxConnectionIdle:     time.Duration(cfg.GRPCListenMaxConnectionIdleSec) * time.Second,
			MaxConnectionAge:      time.Duration(cfg.GRPCListenMaxConnectionAgeSec) * time.Second,
			MaxConnectionAgeGrace: time.Duration(cfg.GRPCListenMaxConnectionIdleSec) * time.Second,
			Time:                  time.Duration(cfg.GRPCListenMaxConnectionIdleSec) * time.Second,
			Timeout:               time.Duration(cfg.GRPCListenMaxConnectionIdleSec) * time.Second,
		}),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             5 * time.Second,
			PermitWithoutStream: true,
		}))
	grpcstreamer.RegisterStreamerServer(gRPCServer, &s)
	err := gRPCServer.Serve(l)
	if err != nil {
		lg.Error("serving gRPC failed", zap.Error(err))
	}

	connWG.Done()
}

type streamerServer struct {
	grpcstreamer.UnimplementedStreamerServer

	queue chan []byte
	stop  <-chan struct{}

	lg *zap.Logger
	ms *metrics.Prom
}

// This function can be running in multiple goroutines simultaneously. Each new incoming
// transmission will call a new instance.
func (server *streamerServer) Stream(s grpcstreamer.Streamer_StreamServer) error {
	res := grpcstreamer.Result{}

loop:
	for {
		select {
		case <-server.stop:
			err := s.SendAndClose(&res)
			if err != nil {
				server.lg.Error("error when sending a response to stream while stopping", zap.Error(err))
			}
			break loop
		default:
		}
		m, err := s.Recv()

		if err == io.EOF {
			err := s.SendAndClose(&res)
			if err != nil {
				server.lg.Error("error when sending a response to stream", zap.Error(err))
			}
			return nil
		} else if err != nil {
			server.lg.Error("error receiving record", zap.Error(err))
			break // gRPC docs: "On any non-EOF error, the stream is aborted and the error contains the RPC status."
		}

		res.ReceivedCount++
		rStr := fmt.Sprintf("%s %e %d",
			m.Name,
			m.GetDoubleGauge().DataPoints[0].Value,
			uint32(m.GetDoubleGauge().DataPoints[0].TimeUnixNano/1000/1000/1000), // ns -> sec
		)
		server.queue <- []byte(rStr) // TODO: Stop using strings, move to parsed structures
	}

	return nil
}
