build-from-scratch: kind-create-cluster use-kind-cluster launch-nt launch-sender check-pods

#####

launch-nt: build-nt-image load-nt-image apply-nt-daemonset

build-nt-image:
	docker build --no-cache --tag nt -f k8s/nt.Dockerfile .

load-nt-image:
	kind load docker-image nt --name nttest

apply-nt-daemonset:
	kubectl apply -f k8s/kube-nt-daemonset.yaml

launch-sender: build-sender-image load-sender-image apply-sender-deployment

build-sender-image:
	docker build --no-cache --tag sender -f k8s/sender.Dockerfile .

load-sender-image:
	kind load docker-image sender --name nttest

apply-sender-deployment:
	kubectl apply -f k8s/sender-deployment.yaml

#####

kind-create-cluster:
	kind create cluster --config k8s/kind-cluster.yaml

use-kind-cluster:
	kubectl cluster-info --context kind-nttest

#####

clean-up:
	kind delete cluster --name nttest

check-pods:
	kubectl get pods
