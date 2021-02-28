package in

import (
	"net"
	"strconv"
	"sync"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/metrics"
	"github.com/libp2p/go-reuseport"

	"github.com/facebookgo/grace/gracenet"
	"github.com/pkg/errors"
	"go.uber.org/zap"
)

// Listen listens for incoming metric data
func Listen(n *gracenet.Net, cfg *conf.Main, stop <-chan struct{}, lg *zap.Logger, ms *metrics.Prom) (chan string, error) {
	queue := make(chan string, cfg.MainQueueSize)
	var connWG sync.WaitGroup

	if cfg.ListenTCP != "" {
		ip, port, err := parseListenOption(cfg.ListenTCP)
		if err != nil {
			return nil, errors.Wrap(err, "error parsing ListenTCP option")
		}
		l, err := n.ListenTCP("tcp", &net.TCPAddr{
			IP:   ip,
			Port: port,
		})
		if err != nil {
			return nil, errors.Wrap(err,
				"error while opening TCP port for listening")
		}
		lg.Info("Launch: Opened TCP connection for listening.", zap.String("ListenTCP", cfg.ListenTCP))

		connWG.Add(1)
		go acceptAndListenTCP(l, queue, stop, cfg, &connWG, ms, lg)
	}

	if cfg.ListenUDP != "" {
		conn, err := reuseport.ListenPacket("udp", cfg.ListenUDP)

		if err != nil {
			lg.Error("error while opening UDP port for listening", zap.Error(err))
			return nil, errors.Wrap(err, "error while opening UDP connection")
		}
		lg.Info("Launch: Opened UDP connection for listening.", zap.String("ListenUDP", cfg.ListenUDP))

		connWG.Add(1)
		go listenUDP(conn, queue, stop, &connWG, ms, lg)
	}

	if cfg.ListenGRPC != "" {
		ip, port, err := parseListenOption(cfg.ListenGRPC)
		if err != nil {
			return nil, errors.Wrap(err, "error parsing ListenGRPC option")
		}
		l, err := n.ListenTCP("tcp", &net.TCPAddr{
			IP:   ip,
			Port: port,
		})
		if err != nil {
			return nil, errors.Wrap(err,
				"error while opening TCP port for GRPC listening")
		}
		lg.Info("Launch: Started GRPC server.", zap.String("ListenGRPC", cfg.ListenGRPC))

		connWG.Add(1)
		go listenGRPC(l, queue, stop, &connWG, cfg, ms, lg)
	}

	go func() {
		connWG.Wait()
		lg.Info("Termination: All incoming connections closed. Draining the main queue.")
		close(queue)
	}()

	return queue, nil
}

func sendToMainQ(rec string, q chan<- string, ms *metrics.Prom) {
	select {
	case q <- rec:
		ms.InRecs.Inc()
	default:
		ms.ThrottledRecs.Inc()
	}
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
	// ":2003" will listen on all IPs
	if ipStr == "" {
		return nil, port, nil
	}
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return ip, port, errors.New("could not parse IP")
	}
	return ip, port, nil
}
