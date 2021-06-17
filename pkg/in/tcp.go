package in

import (
	"bufio"
	"net"
	"sync"
	"time"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/metrics"
	"go.uber.org/zap"
)

// AcceptAndListenTCP listens for incoming TCP connections.
func AcceptAndListenTCP(l net.Listener, queue chan<- string, term <-chan struct{},
	cfg *conf.Main, connWG *sync.WaitGroup, ms *metrics.Prom, lg *zap.Logger) {
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
			err := l.Close()
			if err != nil {
				lg.Error("failed to close listening TCP connection", zap.Error(err))
			}
			break loop
		case err := <-errCh:
			lg.Error("accepting connection failed", zap.Error(err))
		case conn := <-connCh:
			wg.Add(1)
			go readFromConnectionTCP(&wg, conn, queue, term, cfg, ms, lg)
		}
	}
	// TODO: Change logging level to debug
	lg.Info("Stopped accepting new TCP connections. Starting to close incoming connections...", zap.String("address", l.Addr().String()))
	wg.Wait()
	lg.Info("Finished previously accpted TCP connections.", zap.String("address", l.Addr().String()))

	connWG.Done()
}

func readFromConnectionTCP(wg *sync.WaitGroup, conn net.Conn, queue chan<- string, stop <-chan struct{}, cfg *conf.Main, ms *metrics.Prom, lg *zap.Logger) {
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

func scanForRecordsTCP(conn net.Conn, queue chan<- string, stop <-chan struct{}, cfg *conf.Main, ms *metrics.Prom, lg *zap.Logger) {
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

func sendToMainQ(rec string, q chan<- string, ms *metrics.Prom) {
	select {
	case q <- rec:
		ms.InRecs.Inc()
	default:
		ms.ThrottledRecs.Inc()
	}
}
