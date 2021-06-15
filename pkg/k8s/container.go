package k8s

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/in"
	"github.com/bookingcom/nanotube/pkg/metrics"
	"github.com/containerd/containerd"
	"github.com/containerd/containerd/cio"
	"github.com/containerd/containerd/namespaces"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
	"github.com/pkg/errors"
	"go.uber.org/zap"
)

// Cont is abstraction of the container in the context of metrics forwarding.
type Cont interface {
	StartForwarding() error
	StopForwarding() error
}

type baseCont struct {
	ID   string
	Name string
	Lg   *zap.Logger
	Ms   *metrics.Prom
	Port uint16
	Q    chan string
	Cfg  *conf.Main

	// linter false positive
	listener *net.TCPListener // nolint:structcheck
	stop     chan struct{}
}

// ContainerdCont represents container on containerd runtime.
type ContainerdCont struct {
	baseCont
}

// NewContainerdCont is a constructor.
func NewContainerdCont(id string, name string, lg *zap.Logger, port uint16, q chan string, cfg *conf.Main, ms *metrics.Prom) *ContainerdCont {
	return &ContainerdCont{baseCont{
		ID:   id,
		Name: name,
		Lg:   lg.With(zap.String("ID", id), zap.String("name", name), zap.String("type", "containerd")),
		Ms:   ms,
		Q:    q,
		Cfg:  cfg,
		Port: port,
	}}
}

// StartForwarding starts forwarding Graphite line data from container.
func (c *ContainerdCont) StartForwarding() error {
	c.Lg.Info("Forward start...")

	pid, err := CointainerdPidFromID(c.ID)
	if err != nil {
		return errors.Wrap(err, "could not get pid for container by ID")
	}
	listener, err := OpenTCPTunnelToContainerd(pid, c.Port)
	if err != nil {
		return errors.Wrap(err, "error opening TCP tunnel into container")
	}

	c.listener = listener
	c.stop = make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go in.AcceptAndListenTCP(listener, c.Q, c.stop, c.Cfg, &wg, c.Ms, c.Lg)

	return nil
}

// StopForwarding stops the forwarding.
func (c *ContainerdCont) StopForwarding() error {
	c.Lg.Info("Stopping forwarding...")
	close(c.stop)
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

// DockerCont is a container on docker runtime.
type DockerCont struct {
	baseCont
}

// NewDockerCont is a constructor.
func NewDockerCont(id string, name string, lg *zap.Logger) *DockerCont {
	return &DockerCont{baseCont{
		ID:   id,
		Name: name,
		Lg:   lg.With(zap.String("ID", id), zap.String("name", name), zap.String("type", "docker")),
	}}
}

// StartForwarding starts forwarding Graphite line data from container.
func (c *DockerCont) StartForwarding() error {
	c.Lg.Info("Forward start...")

	listener, err := OpenTCPTunnelToDocker(c.ID, c.Port)
	if err != nil {
		return errors.Wrap(err, "error opening TCP tunnel into container")
	}
	c.listener = listener
	c.stop = make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go in.AcceptAndListenTCP(listener, c.Q, c.stop, c.Cfg, &wg, c.Ms, c.Lg)

	return nil
}

// StopForwarding stops the forwarding.
func (c *DockerCont) StopForwarding() error {
	c.Lg.Info("Stopping forwarding...")
	close(c.stop)

	return nil
}

// ObserveDocker is used for testing Docker client.
func ObserveDocker(lg *zap.Logger) {
	go func() {
		ctx := context.Background()
		cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
		if err != nil {
			lg.Error("error creating docker client", zap.Error(err))
		}
		defer func() {
			err := cli.Close()
			lg.Error("error closing docker client", zap.Error(err))
		}()

		tick := time.NewTicker(10 * time.Second)

		for {
			containers, err := cli.ContainerList(ctx, types.ContainerListOptions{})
			if err != nil {
				lg.Error("error listing containers", zap.Error(err))
			}

			ss := []string{}
			for _, container := range containers {
				ss = append(ss, container.ID)
			}
			lg.Info("Local node containers", zap.Strings("IDs", ss))
			<-tick.C
		}
	}()
}

// ObserveContainerd launches local Docker containers observation on the node w/ containerd runtime.
// Non-blocking. Starts own goroutine.
// Used for testing.
func ObserveContainerd(lg *zap.Logger) {
	go func() {
		ctx := namespaces.WithNamespace(context.Background(), "k8s.io")
		client, err := containerd.New("/run/containerd/containerd.sock")
		if err != nil {
			lg.Error("error creating containerd client", zap.Error(err))
			return
		}
		defer func() {
			err := client.Close()
			lg.Error("error closing containerd client", zap.Error(err))
		}()
		tick := time.NewTicker(10 * time.Second)

		for {
			containers, err := client.Containers(ctx)
			if err != nil {
				lg.Error("error listing containerd containers", zap.Error(err))
			}

			for _, container := range containers {
				tsk, err := container.Task(ctx, cio.NewAttach())
				if err != nil {
					lg.Error("error getting task of container", zap.Error(err), zap.String("container ID", container.ID()))
				}
				lg.Info("container info", zap.String("ID", container.ID()), zap.Uint32("pid", tsk.Pid()))
			}

			<-tick.C
		}
	}()
}
