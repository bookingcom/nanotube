package k8s

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/metrics"
	"github.com/pkg/errors"
	"go.uber.org/zap"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// ObserveK8s is a stub for function to observe and check for labeled pods via k8s API server.
// Non-blocking. Starts own goroutine.
func ObserveK8s(cfg *conf.Main, stop <-chan struct{}, lg *zap.Logger, ms *metrics.Prom) {
	cs := map[string]Cont{}

	go func() {
		tick := time.NewTicker(time.Second * time.Duration(cfg.K8sContainerUpdPeriodSec))
		defer tick.Stop()

		for {
			<-tick.C
			err := updateWatchedContainers(cfg, cs, lg)
			if err != nil {
				lg.Error("error updating watched containers", zap.Error(err))
			}
		}
	}()
}

func updateWatchedContainers(cfg *conf.Main, cs map[string]Cont, lg *zap.Logger) error {
	conf, err := rest.InClusterConfig()
	if err != nil {
		return errors.Wrapf(err, "error getting in-cluster config")
	}

	// client is built from scratch every time to prevent problems with connection keep-alive and config changes
	clientset, err := kubernetes.NewForConfig(conf)
	if err != nil {
		return errors.Wrapf(err, "could not build k8s clientset from config")
	}

	node := os.Getenv("OWN_NODE_NAME")

	listOpts := metav1.ListOptions{
		FieldSelector: fmt.Sprintf("spec.nodeName=%s", node),
	}
	if cfg.K8sSwitchLabel != "" {
		listOpts.LabelSelector = fmt.Sprintf("%s=yes", cfg.K8sSwitchLabel)
	}

	// TODO: Try to use Watch.
	// TODO: Check timeouts etc.
	pods, err := clientset.CoreV1().Pods("default").List(
		context.TODO(),
		listOpts,
	)
	if err != nil {
		return errors.Wrapf(err, "could not list pods")
	}

	freshContainerIDs := map[string]bool{} // id -> is containerd?
	contNames := map[string]string{}       // TODO may leak mem
	for _, pod := range pods.Items {
		for _, contStatus := range pod.Status.ContainerStatuses {
			// contStatus looks like docker://<id>
			parts := strings.Split(contStatus.ContainerID, "://")
			if len(parts) != 2 {
				return errors.Wrap(
					fmt.Errorf("container id: %s, should look like docker://<id>", contStatus.ContainerID),
					"error when splitting container status")
			}
			contNames[parts[1]] = contStatus.Name
			if parts[0] == "containerd" {
				freshContainerIDs[parts[1]] = true
			} else if parts[0] == "docker" {
				freshContainerIDs[parts[1]] = false
			} else {
				return errors.Wrapf(
					fmt.Errorf("container type in id: %s, should be either containerd or docker", parts[0]),
					"error when parsing container id: %s", contStatus.ContainerID)
			}
		}
	}

	// TODO: Forwarding setups are potentially long and blocking.
	// Note: Chances are low, but Docker and containerd IDs can potentially collide.

	// check intersection of fresh and old containers
	for id, c := range cs {
		if _, ok := freshContainerIDs[id]; !ok {
			// if container disappeared, stop forwarding from it
			err := c.StopForwarding()
			if err != nil {
				return errors.Wrap(err, "failed to stop forwarding from container")
			}
			delete(cs, id)
		}
	}

	for id, typ := range freshContainerIDs {
		if _, ok := cs[id]; !ok {
			// if new container appears, start forwarding from it
			if typ {
				// true means containerd
				cs[id] = NewContainerdCont(id, contNames[id], lg, cfg.K8sInjectPortTCP)
			} else {
				// false means docker
				cs[id] = NewDockerCont(id, contNames[id], lg)
			}

			err := cs[id].StartForwarding()
			if err != nil {
				return errors.Wrap(err, "failed to start forwarding from container")
			}
		}
	}

	return nil
}
