package k8s

import (
	"context"
	"fmt"
	"math/rand"
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

// ObserveViaK8sAPI is a function to observe and check for labeled pods via k8s API server.
// Non-blocking. Dones wg on finish.
func ObserveViaK8sAPI(q chan<- [][]byte, cfg *conf.Main, stop <-chan struct{}, wg *sync.WaitGroup, lg *zap.Logger, ms *metrics.Prom) {
	var contWG sync.WaitGroup
	go func() {
		cs := map[string]*Cont{}

		tick := time.NewTicker(time.Second * time.Duration(cfg.K8sContainerUpdPeriodSec))
		defer tick.Stop()
		rnd := rand.New(rand.NewSource(time.Now().UnixNano()))

		ms.K8sCurrentForwardedContainers.Set(0)

		for {
			<-tick.C
			time.Sleep(time.Second * time.Duration(rnd.Intn(cfg.K8sObserveJitterRangeSec+1)))

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

// ObserveLocal is a function to observe and check for labeled pods via the local Docker service.
// Non-blocking. Dones wg on finish.
func ObserveLocal(q chan<- [][]byte, cfg *conf.Main, stop <-chan struct{}, wg *sync.WaitGroup, lg *zap.Logger, ms *metrics.Prom) {
	var contWG sync.WaitGroup
	go func() {
		cs := map[string]*Cont{}

		tick := time.NewTicker(time.Second * time.Duration(cfg.K8sContainerUpdPeriodSec))
		defer tick.Stop()
		rnd := rand.New(rand.NewSource(time.Now().UnixNano()))

		ms.K8sCurrentForwardedContainers.Set(0)

		for {
			<-tick.C
			time.Sleep(time.Second * time.Duration(rnd.Intn(cfg.K8sObserveJitterRangeSec+1)))

			err := updateWatchedContainersLocal(q, stop, &contWG, cfg, cs, lg, ms)
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

func updateWatchedContainersLocal(q chan<- [][]byte, stop <-chan struct{}, wg *sync.WaitGroup, cfg *conf.Main, cs map[string]*Cont, lg *zap.Logger, ms *metrics.Prom) error {
	freshCs, err := getLocalContainers(cfg)
	if err != nil {
		return errors.Wrap(err, "getting containers via k8s API failed")
	}

	// check intersection of fresh and old containers
	for id, c := range cs {
		if _, ok := freshCs[id]; !ok {
			// if container disappeared, stop forwarding from it
			c.StopForwarding()
			ms.K8sCurrentForwardedContainers.Dec()
			delete(cs, id)
		}
	}

	for id, c := range freshCs {
		if _, ok := cs[id]; !ok {
			cs[id] = NewCont(id, c.Name, c.IsContainerd, q, stop, wg, cfg, lg, ms)
			ms.K8sPickedUpContainers.Inc()
			ms.K8sCurrentForwardedContainers.Inc()
			go cs[id].StartForwarding()
		}
	}

	return nil
}

func updateWatchedContainers(q chan<- [][]byte, stop <-chan struct{}, wg *sync.WaitGroup, cfg *conf.Main, cs map[string]*Cont, lg *zap.Logger, ms *metrics.Prom) error {
	freshCs, err := getContainers(cfg)
	if err != nil {
		return errors.Wrap(err, "getting containers via k8s API failed")
	}

	// check intersection of fresh and old containers
	for id, c := range cs {
		if _, ok := freshCs[id]; !ok {
			// if container disappeared, stop forwarding from it
			c.StopForwarding()
			if err != nil {
				return errors.Wrap(err, "failed to stop forwarding from container")
			}
			ms.K8sCurrentForwardedContainers.Dec()
			delete(cs, id)
		}
	}

	for id, c := range freshCs {
		if _, ok := cs[id]; !ok {
			cs[id] = NewCont(id, c.Name, c.IsContainerd, q, stop, wg, cfg, lg, ms)
			ms.K8sPickedUpContainers.Inc()
			ms.K8sCurrentForwardedContainers.Inc()
			go cs[id].StartForwarding()
		}
	}

	return nil
}

type contInfo struct {
	ID           string
	Name         string
	IsContainerd bool
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

	listOpts.LabelSelector = fmt.Sprintf("%s=%s", cfg.K8sSwitchLabelKey, cfg.K8sSwitchLabelVal)

	pods, err := clientset.CoreV1().Pods("").List(
		context.TODO(),
		listOpts,
	)
	if err != nil {
		return nil, errors.Wrapf(err, "could not list pods via k8s API")
	}

	cs := map[string]contInfo{}
	for _, pod := range pods.Items {
		for _, contStatus := range pod.Status.ContainerStatuses {
			// contStatus looks like docker://<id>
			if contStatus.ContainerID == "" {
				continue
			} // empty id is possible during ops
			parts := strings.Split(contStatus.ContainerID, "://")
			if len(parts) != 2 {
				return nil, errors.Wrap(
					fmt.Errorf("container id: %s, should look like docker://<id>", contStatus.ContainerID),
					"error when splitting container id")
			}

			if parts[0] == "containerd" {
				cs[parts[1]] = contInfo{parts[1], contStatus.Name, true}
			} else if parts[0] == "docker" {
				cs[parts[1]] = contInfo{parts[1], contStatus.Name, false}
			} else {
				return nil, errors.Wrapf(
					fmt.Errorf("container type in id: %s, should be either containerd or docker", parts[0]),
					"error when parsing container id: %s", contStatus.ContainerID)
			}
		}
	}

	return cs, nil
}
