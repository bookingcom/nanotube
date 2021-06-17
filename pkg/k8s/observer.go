package k8s

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/metrics"
	"github.com/pkg/errors"
	"go.uber.org/zap"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// Observe is a function to observe and check for labeled pods via k8s API server.
// Non-blocking. Dones wg on finish.
func Observe(q chan<- string, cfg *conf.Main, stop <-chan struct{}, wg *sync.WaitGroup, lg *zap.Logger, ms *metrics.Prom) {
	var contWG sync.WaitGroup
	go func() {
		cs := map[string]*Cont{}

		tick := time.NewTicker(time.Second * time.Duration(cfg.K8sContainerUpdPeriodSec))
		defer tick.Stop()

		for {
			<-tick.C
			err := updateWatchedContainers(q, stop, &contWG, cfg, cs, lg, ms)
			if err != nil {
				lg.Error("error updating watched containers", zap.Error(err))
			}
		}
	}()

	go func() {
		<-stop        // when system is halted...
		contWG.Wait() // ...containers are halted too. Wait for them to finish...
		wg.Done()     // signal that all containers are done.
	}()
}

func updateWatchedContainers(q chan<- string, stop <-chan struct{}, wg *sync.WaitGroup, cfg *conf.Main, cs map[string]*Cont, lg *zap.Logger, ms *metrics.Prom) error {
	// TODO: Forwarding setups are potentially long and blocking.
	// Note: Chances are low, but Docker and containerd IDs can potentially collide.

	freshCs, err := getContainers(cfg)
	if err != nil {
		return errors.Wrap(err, "getting containers via k8s API failed")
	}

	// check intersection of fresh and old containers
	for id, c := range cs {
		if _, ok := freshCs[id]; !ok {
			// if container disappeared, stop forwarding from it
			err := c.StopForwarding()
			if err != nil {
				return errors.Wrap(err, "failed to stop forwarding from container")
			}
			delete(cs, id)
		}
	}

	for id, c := range freshCs {
		if _, ok := cs[id]; !ok {
			// if new container appears, start forwarding from it
			if c.IsContainerd {
				cs[id] = NewContainerdCont(id, c.Name, cfg.K8sInjectPortTCP, q, stop, wg, cfg, lg, ms)
			} else {
				cs[id] = NewDockerCont(id, c.Name, cfg.K8sInjectPortTCP, q, stop, wg, cfg, lg, ms)
			}

			err := cs[id].StartForwarding()
			if err != nil {
				return errors.Wrap(err, "failed to start forwarding from container")
			}
		}
	}

	return nil
}

type contInfo struct {
	IsContainerd bool
	Name         string
	ID           string
}

func getContainers(cfg *conf.Main) (map[string]contInfo, error) {
	conf, err := rest.InClusterConfig()
	if err != nil {
		return nil, errors.Wrapf(err, "error getting in-cluster config")
	}

	// client is built from scratch every time to prevent problems with connection keep-alive and config changes
	clientset, err := kubernetes.NewForConfig(conf)
	if err != nil {
		return nil, errors.Wrapf(err, "could not build k8s clientset from config")
	}

	node := os.Getenv("OWN_NODE_NAME")

	listOpts := metav1.ListOptions{
		FieldSelector: fmt.Sprintf("spec.nodeName=%s", node),
	}
	if cfg.K8sSwitchLabelKey != "" {
		listOpts.LabelSelector = fmt.Sprintf("%s=%s", cfg.K8sSwitchLabelKey, cfg.K8sSwitchLabelVal)
	}

	// TODO: Try to use Watch.
	// TODO: Check timeouts etc.
	pods, err := clientset.CoreV1().Pods("default").List(
		context.TODO(),
		listOpts,
	)
	if err != nil {
		return nil, errors.Wrapf(err, "could not list pods via k8s API")
	}

	cs := map[string]contInfo{}
	for _, pod := range pods.Items {
		// TODO Add fine-graining by container. Currently we open ports in all containers.
		for _, contStatus := range pod.Status.ContainerStatuses {
			// contStatus looks like docker://<id>
			if contStatus.ContainerID == "" { continue } // empty id is possible during ops
			parts := strings.Split(contStatus.ContainerID, "://")
			if len(parts) != 2 {
				return nil, errors.Wrap(
					fmt.Errorf("container id: %s, should look like docker://<id>", contStatus.ContainerID),
					"error when splitting container id")
			}

			if parts[0] == "containerd" {
				cs[parts[1]] = contInfo{true, contStatus.Name, parts[1]}
			} else if parts[0] == "docker" {
				cs[parts[1]] = contInfo{false, contStatus.Name, parts[1]}
			} else {
				return nil, errors.Wrapf(
					fmt.Errorf("container type in id: %s, should be either containerd or docker", parts[0]),
					"error when parsing container id: %s", contStatus.ContainerID)
			}
		}
	}

	return cs, nil
}
