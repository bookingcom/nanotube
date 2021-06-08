package k8s

import (
	"context"
	"net"
	"time"

	"github.com/containerd/containerd"
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
	Lg   *zap.Logger
	Port uint16

	listener *net.TCPListener
}

// ContainerdCont represents container on containerd runtime.
type ContainerdCont struct {
	baseCont
}

// NewContainerdCont is a constructor.
func NewContainerdCont(id string, lg *zap.Logger, port uint16) *ContainerdCont {
	return &ContainerdCont{baseCont{
		ID:   id,
		Lg:   lg.With(zap.String("container ID", id), zap.String("container type", "containerd")),
		Port: port,
	}}
}

// StartForwarding starts forwarding Graphite line data from container.
func (c *ContainerdCont) StartForwarding() error {
	c.Lg.Info("Pretend to start forwarding...")

	listener, err := OpenTCPTunnel(c.ID, c.Port)
	if err != nil {
		return errors.Wrap(err, "error opening TCP tunnel into container")
	}
	c.listener = listener

	go func() {
		conn, err := c.listener.AcceptTCP()
		if err != nil {
			c.Lg.Error("error accepting connection from inside the pod", zap.Error(err))
		}
		c.Lg.Info("accepted TCP connection")
		var buf []byte
		nb, err := conn.Read(buf)
		if err != nil {
			c.Lg.Error("erroror reading from conn", zap.Error(err))
		}
		c.Lg.Info("read from container", zap.Int("n bytes", nb), zap.ByteString("content", buf))

	}()

	return nil
}

// StopForwarding stops the forwarding.
func (c *ContainerdCont) StopForwarding() error {
	c.Lg.Info("Pretend to stop forwarding...")

	return nil
}

// DockerCont is a container on docker runtime.
type DockerCont struct {
	baseCont
}

// NewDockerCont is a constructor.
func NewDockerCont(id string, lg *zap.Logger) *DockerCont {
	return &DockerCont{baseCont{
		ID: id,
		Lg: lg.With(zap.String("container ID", id), zap.String("container type", "docker")),
	}}
}

// StartForwarding starts forwarding Graphite line data from container.
func (c *DockerCont) StartForwarding() error {
	c.Lg.Info("Pretend to start forwarding...")

	return nil
}

// StopForwarding stops the forwarding.
func (c *DockerCont) StopForwarding() error {
	c.Lg.Info("Pretend to stop forwarding...")

	return nil
}

// ObserveDocker is used for testing Docker client.
func ObserveDocker(lg *zap.Logger) {
	go func() {
		// TODO Connection is never refreshed. This may cause problems. What are client guarantees?
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

			ss := []string{}
			for _, container := range containers {
				ss = append(ss, container.ID())
			}
			lg.Info("Local node containers", zap.Strings("IDs", ss))
			<-tick.C
		}
	}()
}
