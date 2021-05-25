package k8s

import (
	"context"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/metrics"
	"github.com/pkg/errors"
	"go.uber.org/zap"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// Observe is a stub for function to observe and check for labeled pods via k8s API server.
func Observe(cfg *conf.Main, lg *zap.Logger, ms *metrics.Prom) error {
	conf, err := rest.InClusterConfig()
	if err != nil {
		return errors.Wrapf(err, "error getting in-cluster config")
	}

	clientset, err := kubernetes.NewForConfig(conf)
	if err != nil {
		return errors.Wrapf(err, "could not build k8s clientset from config")
	}

	pods, err := clientset.CoreV1().Pods("default").List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return errors.Wrapf(err, "could not list pods")
	}

	lg.Info("number of pods", zap.Int("N", len(pods.Items)))

	return nil
}
