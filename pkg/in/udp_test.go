package in

import (
	"bufio"
	"bytes"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/metrics"
	"go.uber.org/zap"
)

type packetConnMock struct {
	traffic [][]byte
	done    chan struct{}
	pos     int
	m       *sync.Mutex
}

func (c *packetConnMock) ReadFrom(p []byte) (int, net.Addr, error) {
	c.m.Lock()
	defer c.m.Unlock()

	if c.pos > len(c.traffic) {
		return 0, &net.UDPAddr{}, nil
	}
	if c.pos == len(c.traffic) {
		close(c.done)
		c.pos++
		return 0, &net.UDPAddr{}, nil
	}

	nBytes := copy(p, append(c.traffic[c.pos], '\n'))
	c.pos++

	return nBytes, &net.UDPAddr{}, nil
}

func (c packetConnMock) WriteTo(p []byte, addr net.Addr) (n int, err error) {
	c.m.Lock()
	defer c.m.Unlock()

	return 0, nil
}
func (c packetConnMock) Close() error {
	c.m.Lock()
	defer c.m.Unlock()

	return nil
}

func (c packetConnMock) LocalAddr() net.Addr {
	c.m.Lock()
	defer c.m.Unlock()

	return &net.UDPAddr{}
}

func (c packetConnMock) SetDeadline(t time.Time) error {
	c.m.Lock()
	defer c.m.Unlock()

	return nil
}

func (c packetConnMock) SetReadDeadline(t time.Time) error {
	c.m.Lock()
	defer c.m.Unlock()

	return nil
}

func (c packetConnMock) SetWriteDeadline(t time.Time) error {
	c.m.Lock()
	defer c.m.Unlock()

	return nil
}

func setup(t *testing.T) (data [][]byte, ms *metrics.Prom, lg *zap.Logger) {
	fixturesPath := "testdata/"

	in, err := os.Open(filepath.Join(fixturesPath, "in"))
	if err != nil {
		t.Fatalf("error opening the in data file %v", err)
	}
	defer func() {
		err := in.Close()
		if err != nil {
			t.Fatalf("error closing in data test file: %v", err)
		}
	}()

	scanner := bufio.NewScanner(in)
	for scanner.Scan() {
		token := scanner.Bytes()
		rec := make([]byte, len(token))
		copy(rec, token)
		data = append(data, rec)
	}

	if err := scanner.Err(); err != nil {
		log.Fatalf("error while scan-reading the sample in metrics %v", err)
	}

	lg, _ = zap.NewProduction()

	cfg := conf.MakeDefault()
	ms = metrics.New(&cfg)

	return
}

func TestUdpStreaming(t *testing.T) {
	data, ms, lg := setup(t)

	stop := make(chan struct{})
	conn := &packetConnMock{
		traffic: data,
		done:    stop,
		m:       &sync.Mutex{},
	}

	q := make(chan []byte, len(data)) // should be >= num packets
	var wg sync.WaitGroup
	wg.Add(1)
	go ListenUDP(conn, q, stop, &wg, ms, lg)
	errCh := make(chan error, 1)
	go func() {
		i := 0
		for r := range q {
			if !bytes.Equal(data[i], r) {
				errCh <- fmt.Errorf("got %s while expecting %s", string(r), string(data[i]))
				break
			}
			i++
		}
		errCh <- nil
	}()
	wg.Wait()
	close(q)

	err := <-errCh
	if err != nil {
		t.Fatal(err)
	}
}
