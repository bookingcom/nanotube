package k8s

import (
	"bytes"
	"context"
	"fmt"
	"github.com/bookingcom/nanotube/pkg/ratelimiter"
	"net"
	"strings"
	"sync"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/in"
	"github.com/bookingcom/nanotube/pkg/metrics"
	"github.com/containerd/containerd"
	"github.com/containerd/containerd/cio"
	"github.com/containerd/containerd/namespaces"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
	"github.com/pkg/errors"
	"go.uber.org/zap"
)

// Cont represents a container.
type Cont struct {
	ID           string
	Name         string
	IsContainerd bool

	Q    chan<- [][]byte
	Cfg  *conf.Main
	Port uint16

	GlobalStop <-chan struct{} // used to stop operation globally (propagated from above)
	OwnStop    chan struct{}   // used to stop operation independently
	Wg         *sync.WaitGroup

	Rls *ratelimiter.Set

	Lg *zap.Logger
	Ms *metrics.Prom
}

// NewCont is a constructor.
func NewCont(id string, name string, isContainerd bool, q chan<- [][]byte, rls *ratelimiter.Set, stop <-chan struct{}, wg *sync.WaitGroup, cfg *conf.Main, lg *zap.Logger, ms *metrics.Prom) *Cont {
	return &Cont{
		ID:           id,
		Name:         name,
		IsContainerd: isContainerd,

		Q:    q,
		Cfg:  cfg,
		Port: cfg.K8sInjectPortTCP,

		GlobalStop: stop,
		OwnStop:    make(chan struct{}),
		Wg:         wg,

		Rls: rls,

		Lg: lg.With(zap.String("ID", id), zap.String("name", name), zap.Bool("isContainerd", isContainerd)),
		Ms: ms,
	}
}

// StartForwarding starts forwarding Graphite line data from container.
func (c *Cont) StartForwarding() {
	c.Lg.Info("Initializing forwarding...")

	var listener net.Listener
	if c.IsContainerd {
		pid, err := CointainerdPidFromID(c.ID)
		if err != nil {
			c.Lg.Error("could not get pid for container by ID", zap.Error(err))
		}
		listener, err = OpenTCPTunnelByPID(pid, c.Port)
		if err != nil {
			c.Lg.Error("error opening TCP tunnel into container", zap.Error(err))
		}
	} else {
		var err error
		pid, err := DockerPIDFromID(c.ID)
		if err != nil {
			c.Lg.Error("could not get pid for container by ID", zap.Error(err))
		}
		listener, err = OpenTCPTunnelByPID(pid, c.Port)
		if err != nil {
			c.Lg.Error("error opening TCP tunnel into container", zap.Error(err))
		}
	}

	c.Wg.Add(1)
	var rateLimiters []ratelimiter.RateLimiter
	if c.Rls != nil {
		if c.Rls.GlobalRateLimiter() != nil {
			rateLimiters = append(rateLimiters, c.Rls.GlobalRateLimiter())
		}
		if c.Cfg.RateLimiterContainerRecordLimit > 0 {
			rateLimiters = append(rateLimiters, c.Rls.GetOrCreateContainerRateLimiterWithID(c.ID, c.Cfg))
		}
	}
	go in.AcceptAndListenTCPBuf(listener, c.Q, c.OwnStop, rateLimiters, c.Cfg, c.Wg, c.Ms, c.Lg)

	go func() {
		select {
		case <-c.GlobalStop:
			close(c.OwnStop)
		case <-c.OwnStop:
			// prevent goroutine leak
		}
	}()
}

// StopForwarding stops the forwarding.
func (c *Cont) StopForwarding() {
	c.Lg.Info("Stopping forwarding...")
	close(c.OwnStop)
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

// DockerPIDFromID gets Docker container host PID using Docker API.
func DockerPIDFromID(id string) (pid uint32, retErr error) {
	pid = 0

	client, err := client.NewClientWithOpts(client.WithAPIVersionNegotiation())
	if err != nil {
		retErr = errors.Wrap(err, "error creating docker daemon client")
		return
	}

	defer func() {
		closeErr := client.Close()
		if closeErr != nil {
			retErr = errors.Wrapf(retErr, "error while closing the docker daemon client %v", closeErr)
		}
	}()

	container, err := client.ContainerInspect(context.Background(), id)
	if err != nil {
		retErr = errors.Wrap(err, "error inspecting docker container")
		return
	}

	pid = uint32(container.ContainerJSONBase.State.Pid)
	return
}

func getLocalContainers(cfg *conf.Main) (res map[string]contInfo, retErr error) {
	res = make(map[string]contInfo)

	client, err := client.NewClientWithOpts(client.WithAPIVersionNegotiation())
	if err != nil {
		retErr = errors.Wrap(err, "error creating docker daemon client")
		return
	}

	defer func() {
		closeErr := client.Close()
		if closeErr != nil {
			retErr = errors.Wrapf(retErr, "error while closing the docker daemon client %v", closeErr)
		}
	}()

	listOpts := types.ContainerListOptions{}
	listOpts.Filters = filters.NewArgs(filters.Arg("label", "io.kubernetes.container.name=POD"), filters.Arg("label", fmt.Sprintf("%s=%s", cfg.K8sSwitchLabelKey, cfg.K8sSwitchLabelVal)))
	containers, err := client.ContainerList(context.Background(), listOpts)
	if err != nil {
		retErr = errors.Wrap(err, "error getting list of containers")
		return
	}

	for _, c := range containers {
		res[c.ID] = contInfo{c.ID, c.Names[0], false}
	}

	return
}
