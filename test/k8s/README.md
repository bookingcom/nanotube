# Nanokube

Nanokube is Nanotube on Kubernetes.

Nanotube can be run on kubernetes as a daemonset; each of the pods of nanokube daemonset accept and receive the traffic from the pods residing on the same node. In order for nanokube to be able to open local ports inside other pods, the nanokube pods should be privileged. See [nanokube-daemonset manifest](nanokube-daemonset.yaml) for details.

## Setup a test k8s cluster using kind

In order to test how nanokube works, you can run a local kubernetes cluster using [kind](https://kind.sigs.k8s.io/).

[An ansible playbook](launch.yml) is prepared to deploy a kubernetes cluster running nanokube instances locally and some test senders which generate load on them.

### Run a local nanokube enabled k8s cluster

1. Install kind.
2. Install kubernetes python library:

```bash
pip install kubernetes
```

3. Install ansible collections for kubernetes:

```bash
ansible-galaxy collection install kubernetes.core
ansible-galaxy collection install community.kubernetes
ansible-galaxy collection install cloud.common
```

4. Run the ansible playbook:

```bash
ansible-playbook launch.yml
```

### Enable nanokube monitoring on kind

> Note: This section is optional. The cluster will be up and running after last step. Proceed if you need the monitoring in place.

1. Add [prometheus-operator](https://github.com/prometheus-operator/prometheus-operator) to the cluster:

```bash
kubectl create -f https://raw.githubusercontent.com/prometheus-operator/prometheus-operator/release-0.60/bundle.yaml
```

2. Add nanokube [monitoring manifests](monitoring):

```bash
kubectl create -f monitoring/
```

3. In order to open the web UI of prometheus you can use port-forward:

```bash
kubectl -n default port-forward prometheus-nk-prometheus-0 9090:9090
```

### Cleanup the cluster

For cleaning up the cluster, you can easily use [delete-cluster playbook](delete-cluster.yml):

```bash
ansible-playbook delete-cluster.yml
```
