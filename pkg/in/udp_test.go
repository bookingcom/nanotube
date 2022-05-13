package in

import (
	"bytes"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/bookingcom/nanotube/pkg/test"
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

func (c *packetConnMock) WriteTo(p []byte, addr net.Addr) (n int, err error) {
	c.m.Lock()
	defer c.m.Unlock()

	return 0, nil
}
func (c *packetConnMock) Close() error {
	c.m.Lock()
	defer c.m.Unlock()

	return nil
}

func (c *packetConnMock) LocalAddr() net.Addr {
	c.m.Lock()
	defer c.m.Unlock()

	return &net.UDPAddr{}
}

func (c *packetConnMock) SetDeadline(t time.Time) error {
	c.m.Lock()
	defer c.m.Unlock()

	return nil
}

func (c *packetConnMock) SetReadDeadline(t time.Time) error {
	c.m.Lock()
	defer c.m.Unlock()

	return nil
}

func (c *packetConnMock) SetWriteDeadline(t time.Time) error {
	c.m.Lock()
	defer c.m.Unlock()

	return nil
}

func TestUdpStreaming(t *testing.T) {
	data, ms, lg, err := test.Setup()
	if err != nil {
		t.Fatalf("failed to setup the test: %v", err)
	}

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

	err = <-errCh
	if err != nil {
		t.Fatal(err)
	}
}
