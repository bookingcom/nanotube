package k8s

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/metrics"
	"github.com/containerd/containerd"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
	"github.com/pkg/errors"
	"go.uber.org/zap"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// ObserveK8s is a stub for function to observe and check for labeled pods via k8s API server.
// Non-blocking. Starts own goroutine.
func ObserveK8s(cfg *conf.Main, lg *zap.Logger, ms *metrics.Prom) error {

	conf, err := rest.InClusterConfig()
	if err != nil {
		return errors.Wrapf(err, "error getting in-cluster config")
	}

	clientset, err := kubernetes.NewForConfig(conf)
	if err != nil {
		return errors.Wrapf(err, "could not build k8s clientset from config")
	}

	// nodesList, err := clientset.CoreV1().Nodes().List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return errors.Wrapf(err, "could not obtain nodes list")
	}
	// node := nodesList.Items[0].GetName()
	node := os.Getenv("OWN_NODE_NAME")
	lg.Info("own node name", zap.String("OWN_NODE_NAME", node))

	// labelSel := metav1.LabelSelector{
	// 	MatchLabels: map[string]string{"use_graphite_line": "yes"},
	// }
	// fieldSel := fields.OneTermEqualSelector("spec.nodeName", node)
	// TODO: Try to use Watch.
	pods, err := clientset.CoreV1().Pods("default").List(
		context.TODO(),
		metav1.ListOptions{

			LabelSelector: "use_graphite_line=yes",
			FieldSelector: fmt.Sprintf("spec.nodeName=%s", node),
		},
	)
	if err != nil {
		return errors.Wrapf(err, "could not list pods")
	}

	podIDs := []string{}
	for _, pod := range pods.Items {
		podIDs = append(podIDs, string(pod.GetUID()))
	}

	lg.Info("list of pods", zap.String("node", node), zap.Strings("pod UIDs", podIDs))
	

	return nil
}

// ObserveDocker launches local Docker containers observation on the node w/ docker runtime.
// Non-blocking. Starts own goroutine.
func ObserveDocker(lg *zap.Logger) {
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

	go func() {
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
func ObserveContainerd(lg *zap.Logger) {
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

	go func() {
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
