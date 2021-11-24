package in

import (
	"bytes"
	"errors"
	"net"
	"sync"

	"github.com/bookingcom/nanotube/pkg/metrics"
	"go.uber.org/zap"
)

// ListenUDP listens for incoming UDP connections.
func ListenUDP(conn net.PacketConn, queue chan string, stop <-chan struct{}, connWG *sync.WaitGroup, ms *metrics.Prom, lg *zap.Logger) {
	go func() {
		<-stop
		lg.Info("Termination: Closing the UDP connection.")
		cerr := conn.Close()
		if cerr != nil {
			lg.Error("closing the incoming UDP connection failed", zap.Error(cerr))
		}
	}()

	buf := make([]byte, 64*1024) // 64k is the max UDP datagram size
	for {
		nRead, _, err := conn.ReadFrom(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				break
			}

			lg.Error("error reading UDP datagram", zap.Error(err))
			continue
		}

		// WARNING: The split does not copy the data.
		lines := bytes.Split(buf[:nRead], []byte{'\n'})

		// TODO (grzkv): string -> []bytes, line has to be copied to avoid races.
		for i := 0; i < len(lines)-1; i++ {
			sendToMainQ(string(lines[i]), queue, ms)
		}
	}

	lg.Info("Termination: Stopped accepting UDP data.")
	connWG.Done()
}
