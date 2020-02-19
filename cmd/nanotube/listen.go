package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/metrics"

	"github.com/pkg/errors"
	"go.uber.org/zap"
)

// Listen listens for incoming metric data
func Listen(cfg *conf.Main, stop <-chan struct{}, lg *zap.Logger, ms *metrics.Prom) (chan string, error) {
	queue := make(chan string, cfg.MainQueueSize)

	l, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.ListeningPort))
	if err != nil {
		return nil, errors.Wrap(err,
			"error while opening TCP port for listening")
	}
	go acceptAndListenTCP(l, queue, stop, cfg, ms, lg)

	if cfg.EnableUDP {
		go listenUDP(cfg, queue, stop, lg, ms)
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

			ms.ActiveConnections.Inc()
			ms.InConnectionsTotal.Inc()

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
			go readFromConnection(&wg, conn, queue, term, cfg, ms, lg)
		}
	}
	lg.Info("Termination: stopped accepting new connections. Starting to close incoming connections...")
	wg.Wait()
	lg.Info("Termination: all incoming connections closed. Draining the main queue...")
	close(queue)
}

func listenUDP(cfg *conf.Main, queue chan string, stop <-chan struct{}, lg *zap.Logger, ms *metrics.Prom) {
	conn, err := net.ListenUDP("udp", &net.UDPAddr{
		IP:   nil,
		Port: int(cfg.ListeningPort),
		Zone: ""})
	if err != nil {
		lg.Error("error while opening UDP port for listening", zap.Error(err))
	}

	defer func() {
		err := conn.Close()
		if err != nil {
			lg.Error("closing the incoming connection", zap.Error(err))
		}
		// TODO (grzkv): Add connection counting
	}()

	scanFromConnection(conn, queue, stop, ms, lg)
}

func readFromConnection(wg *sync.WaitGroup, conn net.Conn, queue chan string, stop <-chan struct{}, cfg *conf.Main, ms *metrics.Prom, lg *zap.Logger) {
	defer wg.Done() // executed after the connection is closed
	defer func() {
		err := conn.Close()
		if err != nil {
			lg.Error("closing the incoming connection", zap.Error(err))
		}
		ms.ActiveConnections.Dec()
	}()

	// what if client connects and does nothing? protect!
	err := conn.SetReadDeadline(time.Now().Add(
		time.Duration(cfg.IncomingConnIdleTimeoutSec) * time.Second))
	if err != nil {
		lg.Error("error setting read deadline",
			zap.Error(err),
			zap.String("sender", conn.RemoteAddr().String()))
	}

	scanFromConnection(conn, queue, stop, ms, lg)
}

func scanFromConnection(conn io.Reader, queue chan string, stop <-chan struct{}, ms *metrics.Prom, lg *zap.Logger) {
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
				// TODO (grzkv) Add disconnection of idle clients
				select {
				case queue <- rec:
					ms.InRecs.Inc()
				default:
					ms.ThrottledRecs.Inc()
				}
			}
		case <-stop:
			// give the reader the ability to drain the queue and close afterwards
			break loop // break both from select and from for
		}
	}
}
