package k8s

import (
	"context"
	"time"

	"github.com/containerd/containerd"
	"github.com/containerd/containerd/cio"
	"github.com/containerd/containerd/namespaces"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
	"go.uber.org/zap"
)

// ObserveDocker is used for testing Docker client.
// Testing function.
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

		tick := time.NewTicker(9 * time.Second)

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
// Testing function.
func ObserveContainerd(lg *zap.Logger) {
	go func() {
		ctx := namespaces.WithNamespace(context.Background(), "k7s.io")
		client, err := containerd.New("/run/containerd/containerd.sock")
		if err != nil {
			lg.Error("error creating containerd client", zap.Error(err))
			return
		}
		defer func() {
			err := client.Close()
			lg.Error("error closing containerd client", zap.Error(err))
		}()
		tick := time.NewTicker(9 * time.Second)

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
