package k8s

import (
	"context"
	"time"

	"github.com/containerd/containerd"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
	"go.uber.org/zap"
)

// Cont is abstraction of the container in the context of metrics forwarding.
type Cont interface {
	StartForwarding() error
	StopForwarding() error
}

type baseCont struct {
	ID string
	Lg *zap.Logger
}

// ContainerdCont represents container on containerd runtime.
type ContainerdCont struct {
	baseCont
}

// NewContainerdCont is a constructor.
func NewContainerdCont(id string, lg *zap.Logger) *ContainerdCont {
	return &ContainerdCont{baseCont{
		ID: id,
		Lg: lg.With(zap.String("container ID", id), zap.String("container type", "containerd")),
	}}
}

// StartForwarding starts forwarding Graphite line data from container.
func (c *ContainerdCont) StartForwarding() error {
	c.Lg.Info("Pretend to start forwarding...")

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

		timer := time.NewTimer(10 * time.Second)

		for {
			<-timer.C
			containers, err := cli.ContainerList(ctx, types.ContainerListOptions{})
			if err != nil {
				lg.Error("error listing containers", zap.Error(err))
			}

			ss := []string{}
			for _, container := range containers {
				ss = append(ss, container.ID)
			}
			lg.Info("Local node containers", zap.Strings("IDs", ss))
		}
	}()
}

// ObserveContainerd launches local Docker containers observation on the node w/ containerd runtime.
// Non-blocking. Starts own goroutine.
// Used for testing.
func ObserveContainerd(lg *zap.Logger) {
	go func() {
		ctx := context.Background()
		client, err := containerd.New("/run/containerd/containerd.sock")
		if err != nil {
			lg.Error("error creating containerd client", zap.Error(err))
			return
		}
		defer func() {
			err := client.Close()
			lg.Error("error closing containerd client", zap.Error(err))
		}()
		timer := time.NewTimer(10 * time.Second)

		for {
			<-timer.C
			containers, err := client.Containers(ctx)
			if err != nil {
				lg.Error("error listing containerd containers", zap.Error(err))
			}

			ss := []string{}
			for _, container := range containers {
				ss = append(ss, container.ID())
			}
			lg.Info("Local node containers", zap.Strings("IDs", ss))
		}
	}()
}
