package in

import (
	"io"
	"net"
	"sync"

	"github.com/bookingcom/nanotube/pkg/grpcstreamer"
	"github.com/bookingcom/nanotube/pkg/metrics"
	"go.uber.org/zap"
	"google.golang.org/grpc"
)

// TODO: Add defense against open streams that are never used.

// Blocking. Returns when server returns.
func listenGRPC(l net.Listener, queue chan string, stop <-chan struct{}, connWG *sync.WaitGroup, trace bool, ms *metrics.Prom, lg *zap.Logger) {
	// TODO: How much overhead does enabling the tracing contribute?
	grpc.EnableTracing = trace

	// TODO: Check for optimal server options
	s := streamerServer{
		queue: queue,
		stop:  stop,

		lg: lg,
		ms: ms,
	}
	gRPCServer := grpc.NewServer()
	grpcstreamer.RegisterStreamerServer(gRPCServer, &s)
	err := gRPCServer.Serve(l)
	if err != nil {
		lg.Error("serving gRPC failed", zap.Error(err))
	}

	connWG.Done()
}

type streamerServer struct {
	queue chan string
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
		rec, err := s.Recv()

		if err == io.EOF {
			server.lg.Info("got EOF")
			// TODO: Do we have to do something special to re-start listening?
			err := s.SendAndClose(&res)
			if err != nil {
				server.lg.Error("error when sending a response to stream", zap.Error(err))
			}
			return nil
		} else if err != nil {
			res.ErrorCount++
			server.lg.Error("error receiving record", zap.Error(err))
			// TODO: Is it ok to continue after an error?
			continue
		}
		// TODO: Cleanup
		server.lg.Info("got record", zap.ByteString("rec", rec.Rec))

		res.ReceivedCount++
		server.queue <- string(rec.Rec)
	}

	return nil
}
