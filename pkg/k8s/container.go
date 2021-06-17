package k8s

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"strings"
	"sync"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/in"
	"github.com/bookingcom/nanotube/pkg/metrics"
	"github.com/containerd/containerd"
	"github.com/containerd/containerd/cio"
	"github.com/containerd/containerd/namespaces"
	"github.com/pkg/errors"
	"go.uber.org/zap"
)

// TODO: Move debug logging to appropriate zap.Debug level.

// Cont represents a container.
type Cont struct {
	ID           string
	Name         string
	Lg           *zap.Logger
	Ms           *metrics.Prom
	Port         uint16
	IsContainerd bool
	Q            chan<- string
	Cfg          *conf.Main
	Listener     *net.TCPListener // nolint:structcheck

	GlobalStop <-chan struct{} // used to stop operation globally (propagated from above)
	OwnStop    chan struct{}   // used to stop operation independently
	Wg         *sync.WaitGroup
}

// NewContainerdCont is a constructor.
func NewContainerdCont(id string, name string, port uint16, q chan<- string, stop <-chan struct{}, wg *sync.WaitGroup, cfg *conf.Main, lg *zap.Logger, ms *metrics.Prom) *Cont {
	return &Cont{
		ID:           id,
		Name:         name,
		Lg:           lg.With(zap.String("ID", id), zap.String("name", name), zap.String("type", "containerd")),
		Ms:           ms,
		Q:            q,
		GlobalStop:   stop,
		Cfg:          cfg,
		Port:         port,
		IsContainerd: true,
		Wg:           wg,
	}
}

// NewDockerCont is a constructor.
func NewDockerCont(id string, name string, port uint16, q chan<- string, stop <-chan struct{}, wg *sync.WaitGroup, cfg *conf.Main, lg *zap.Logger, ms *metrics.Prom) *Cont {
	return &Cont{
		ID:           id,
		Name:         name,
		Lg:           lg.With(zap.String("ID", id), zap.String("name", name), zap.String("type", "docker")),
		Ms:           ms,
		Q:            q,
		GlobalStop:   stop,
		Cfg:          cfg,
		Port:         port,
		IsContainerd: false,
		Wg:           wg,
	}
}

// StartForwarding starts forwarding Graphite line data from container.
func (c *Cont) StartForwarding() error {
	c.Lg.Info("Forward start...")

	if c.IsContainerd {

		pid, err := CointainerdPidFromID(c.ID)
		if err != nil {
			return errors.Wrap(err, "could not get pid for container by ID")
		}
		listener, err := OpenTCPTunnelToContainerd(pid, c.Port)
		if err != nil {
			return errors.Wrap(err, "error opening TCP tunnel into container")
		}

		c.Listener = listener
		c.OwnStop = make(chan struct{})
	} else {

		listener, err := OpenTCPTunnelToDocker(c.ID, c.Port)
		if err != nil {
			return errors.Wrap(err, "error opening TCP tunnel into container")
		}

		// TODO: Move
		c.Listener = listener
		// TODO: Move
		c.OwnStop = make(chan struct{})
	}

	c.Wg.Add(1)
	go in.AcceptAndListenTCP(c.Listener, c.Q, c.OwnStop, c.Cfg, c.Wg, c.Ms, c.Lg)

	go func() {
		select {
		case <-c.GlobalStop:
			close(c.OwnStop)
		case <-c.OwnStop:
			// prevent goroutine leak
		}
	}()

	return nil
}

// StopForwarding stops the forwarding.
func (c *Cont) StopForwarding() error {
	c.Lg.Info("Stopping forwarding...")
	close(c.OwnStop)
	return nil
}

// CointainerdPidFromID returns PID for container
func CointainerdPidFromID(id string) (pid uint32, retErr error) {
	pid = 0
	retErr = nil

	ctx := namespaces.WithNamespace(context.Background(), "k8s.io")
	client, err := containerd.New("/run/containerd/containerd.sock")
	if err != nil {
		return 0, errors.Wrap(err, "error creating containerd client")
	}
	defer func() {
		err := client.Close()
		if retErr != nil {
			retErr = errors.Wrap(retErr, fmt.Sprintf("error closing containerd client: %v", err))
		} else {
			retErr = errors.Wrap(err, "error closing containerd client")
		}
	}()

	containers, err := client.Containers(ctx)
	if err != nil {
		retErr = errors.Wrap(err, "error listing containerd containers")
		return
	}

	for _, container := range containers {
		if container.ID() == id {
			in := strings.NewReader("")
			var outBuf bytes.Buffer
			var errBuf bytes.Buffer
			tsk, err := container.Task(ctx, cio.NewAttach(cio.WithStreams(in, &outBuf, &errBuf)))
			if err != nil {
				retErr = errors.Wrap(err, "error getting task of container")
				return
			}
			pid = tsk.Pid()
			err = tsk.CloseIO(ctx)
			if err != nil {
				retErr = errors.Wrap(err, "error closing container task")
				return
			}
		}
	}

	if pid == 0 {
		retErr = errors.New("could not find pid for container; no container with such id found")
	}

	return
}
