package main

import (
	"bufio"
	"bytes"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/metrics"

	"github.com/pkg/errors"
	"go.uber.org/zap"
)

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

// Listen listens for incoming metric data
func Listen(cfg *conf.Main, stop <-chan struct{}, lg *zap.Logger, ms *metrics.Prom) (chan string, error) {
	queue := make(chan string, cfg.MainQueueSize)

	if cfg.ListenTCP != "" {
		ip, port, err := parseListenOption(cfg.ListenTCP)
		if err != nil {
			return nil, errors.Wrap(err, "error parsing ListenTCP option")
		}
		l, err := net.ListenTCP("tcp", &net.TCPAddr{
			IP:   ip,
			Port: port,
		})
		if err != nil {
			return nil, errors.Wrap(err,
				"error while opening TCP port for listening")
		}
		go acceptAndListenTCP(l, queue, stop, cfg, ms, lg)
	}

	if cfg.ListenUDP != "" {
		ip, port, err := parseListenOption(cfg.ListenUDP)
		if err != nil {
			return nil, errors.Wrap(err, "error parsing ListenUDP option")
		}
		conn, err := net.ListenUDP("udp", &net.UDPAddr{
			IP:   ip,
			Port: port,
		})
		if err != nil {
			lg.Error("error while opening UDP port for listening", zap.Error(err))
			return nil, errors.Wrap(err,
				"error while opening UDP connection")
		}
		if cfg.UDPOSBufferSize != 0 {
			err := conn.SetReadBuffer(int(cfg.UDPOSBufferSize))
			if err != nil {
				lg.Error("error setting UDP reader buffer size", zap.Error(err))
			}
		}

		go listenUDP(conn, queue, stop, lg, ms)

	}

	return queue, nil
}

func acceptAndListenTCP(l net.Listener, queue chan string, term <-chan struct{}, cfg *conf.Main, ms *metrics.Prom, lg *zap.Logger) {
	var wg sync.WaitGroup

loop:
	for {
		connCh := make(chan net.Conn)
		errCh := make(chan error)
		go func() {
			conn, err := l.Accept()

			ms.ActiveTCPConnections.Inc()
			ms.InConnectionsTotalTCP.Inc()

			if err != nil {
				errCh <- err
			} else {
				connCh <- conn
			}
		}()

		select {
		case <-term:
			// stop accepting new connections on termination signal
			break loop // need to break both select and the loop
		case err := <-errCh:
			lg.Error("accpeting connection failed", zap.Error(err))
		case conn := <-connCh:
			wg.Add(1)
			go readFromConnectionTCP(&wg, conn, queue, term, cfg, ms, lg)
		}
	}
	lg.Info("Termination: stopped accepting new connections. Starting to close incoming connections...")
	wg.Wait()
	lg.Info("Termination: all incoming connections closed. Draining the main queue...")
	close(queue)
}

func listenUDP(conn net.Conn, queue chan string, stop <-chan struct{}, lg *zap.Logger, ms *metrics.Prom) {
	defer func() {
		cerr := conn.Close()
		if cerr != nil {
			lg.Error("closing the incoming connection", zap.Error(cerr))
		}
	}()

	buf := make([]byte, 64*1024) // 64k is the max UDP datagram size
	for {
		nRead, err := conn.Read(buf)
		if err != nil {
			lg.Error("error reading UDP datagram", zap.Error(err))
			continue
		}

		lines := bytes.Split(buf[:nRead], []byte{'\n'})

		// TODO (grzkv): string -> []bytes
		for i := 0; i < len(lines)-1; i++ {
			sendToMainQ(string(lines[i]), queue, ms)
		}

		select {
		case <-stop:
			return
		default:
		}
	}
}

func sendToMainQ(rec string, q chan<- string, ms *metrics.Prom) {
	select {
	case q <- rec:
		ms.InRecs.Inc()
	default:
		ms.ThrottledRecs.Inc()
	}
}

func readFromConnectionTCP(wg *sync.WaitGroup, conn net.Conn, queue chan string, stop <-chan struct{}, cfg *conf.Main, ms *metrics.Prom, lg *zap.Logger) {
	defer wg.Done() // executed after the connection is closed
	defer func() {
		err := conn.Close()
		if err != nil {
			lg.Error("closing the incoming connection", zap.Error(err))
		}
		ms.ActiveTCPConnections.Dec()
	}()

	// what if client connects and does nothing? protect!
	err := conn.SetReadDeadline(time.Now().Add(
		time.Duration(cfg.IncomingConnIdleTimeoutSec) * time.Second))
	if err != nil {
		lg.Error("error setting read deadline",
			zap.Error(err),
			zap.String("sender", conn.RemoteAddr().String()))
	}

	scanForRecordsTCP(conn, queue, stop, cfg, ms, lg)
}

func scanForRecordsTCP(conn net.Conn, queue chan string, stop <-chan struct{}, cfg *conf.Main, ms *metrics.Prom, lg *zap.Logger) {
	sc := bufio.NewScanner(conn)
	scanin := make(chan string)
	go func() {
		for sc.Scan() {
			scanin <- sc.Text()
		}
		close(scanin)
	}()

loop:
	for {
		select {
		case rec, open := <-scanin:
			if !open {
				break loop
			} else {
				// what if client connects and does nothing? protect!
				err := conn.SetReadDeadline(time.Now().Add(
					time.Duration(cfg.IncomingConnIdleTimeoutSec) * time.Second))
				if err != nil {
					lg.Error("error setting read deadline",
						zap.Error(err),
						zap.String("sender", conn.RemoteAddr().String()))
				}

				sendToMainQ(rec, queue, ms)
			}
		case <-stop:
			// give the reader the ability to drain the queue and close afterwards
			break loop // break both from select and from for
		}
	}
}
