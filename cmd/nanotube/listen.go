package main

import (
	"net"
	"strconv"
	"sync"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/in"
	"github.com/bookingcom/nanotube/pkg/k8s"
	"github.com/bookingcom/nanotube/pkg/metrics"
	"github.com/bookingcom/nanotube/pkg/ratelimiter"

	"github.com/facebookgo/grace/gracenet"
	"github.com/libp2p/go-reuseport"
	"github.com/pkg/errors"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"
)

// Listen listens for incoming metric data
func Listen(n *gracenet.Net, rateLimiters []*ratelimiter.SlidingWindow, cfg *conf.Main, stop <-chan struct{}, lg *zap.Logger, ms *metrics.Prom, stream ...in.StreamFactory) (chan [][]byte, *in.BatchBudget, *in.TCPInput, error) {
	queue := make(chan []byte, cfg.MainQueueSize)
	queueBuf := make(chan [][]byte, cfg.MainQueueSize)
	budget := in.NewBatchBudget(cfg.TCPMainQueueBytes)
	prometheus.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{Namespace: "nanotube", Name: "main_queue_batches", Help: "Input batches awaiting routing."}, func() float64 { return float64(len(queueBuf)) }))

	if budget != nil {
		prometheus.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{Namespace: "nanotube", Name: "tcp_main_queue_bytes", Help: "Conservatively charged TCP batch bytes awaiting routing, including active workers."}, budget.Bytes))
	}
	var connWG sync.WaitGroup
	var tcpInput *in.TCPInput

	if cfg.K8sMode {
		connWG.Add(1)

		if cfg.K8sUseK8sServer {
			k8s.ObserveViaK8sAPI(queueBuf, rateLimiters, cfg, stop, &connWG, lg, ms)
		} else {
			k8s.ObserveLocal(queueBuf, rateLimiters, cfg, stop, &connWG, lg, ms)
		}
	} else {
		if cfg.ListenTCP != "" {
			ip, port, err := parseListenOption(cfg.ListenTCP)
			if err != nil {
				return nil, nil, nil, errors.Wrap(err, "error parsing ListenTCP option")
			}
			l, err := n.ListenTCP("tcp", &net.TCPAddr{
				IP:   ip,
				Port: port,
			})
			if err != nil {
				return nil, nil, nil, errors.Wrap(err,
					"error while opening TCP port for listening")
			}
			lg.Info("Launch: Opened TCP connection for listening.", zap.String("ListenTCP", cfg.ListenTCP))

			connWG.Add(1)

			tcpInput = in.NewTCPInput(queueBuf, stop, rateLimiters, cfg, ms, lg, budget, stream...)
			go func() { defer connWG.Done(); tcpInput.Serve(l, stop) }()
		}

		if cfg.ListenUDP != "" {
			conn, err := reuseport.ListenPacket("udp", cfg.ListenUDP)

			if err != nil {
				lg.Error("error while opening UDP port for listening", zap.Error(err))
				return nil, nil, nil, errors.Wrap(err, "error while opening UDP connection")
			}
			lg.Info("Launch: Opened UDP connection for listening.", zap.String("ListenUDP", cfg.ListenUDP))

			connWG.Add(1)
			go in.ListenUDPBuf(conn, queueBuf, stop, rateLimiters, &connWG, cfg, ms, lg)
		}

		if cfg.ListenGRPC != "" {
			ip, port, err := parseListenOption(cfg.ListenGRPC)
			if err != nil {
				return nil, nil, nil, errors.Wrap(err, "error parsing ListenGRPC option")
			}
			l, err := n.ListenTCP("tcp", &net.TCPAddr{
				IP:   ip,
				Port: port,
			})
			if err != nil {
				return nil, nil, nil, errors.Wrap(err,
					"error while opening TCP port for GRPC listening")
			}
			lg.Info("Launch: Started GRPC server.", zap.String("ListenGRPC", cfg.ListenGRPC))

			connWG.Add(1)
			// TODO Use buffered q in GRPC
			go in.ListenGRPC(l, queue, stop, &connWG, cfg, ms, lg)
		}
	}

	go func() {
		connWG.Wait()
		lg.Info("Termination: All incoming connections closed. Draining the main queue.")
		close(queueBuf)
		close(queue)
	}()

	return queueBuf, budget, tcpInput, nil
}

// parse "ip:port" string that is used for ListenTCP and ListenUDP config options
func parseListenOption(listenOption string) (net.IP, int, error) {
	ipStr, portStr, err := net.SplitHostPort(listenOption)
	if err != nil {
		return nil, 0, err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, port, err
	}
	if port < 0 || port > 65535 {
		return nil, port, errors.New("invalid port value")
	}
	if ipStr == "" {
		return nil, port, nil
	}
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return ip, port, errors.New("could not parse IP")
	}
	return ip, port, nil
}
