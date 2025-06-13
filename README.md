# GKE Setup

### Prerequisites

* gcloud (with GCP account)
* kubectl

##### Kubernetes

Log into GCP (my demo was using GKE, you might prefer to use another cloud).

```sh
gcloud auth login
```

Create the GKE cluster

```sh
# Set the name of the cluster.
export CLUSTER_NAME="crdb-resilience-testing"

# Get second from latest version (to allow for cluster upgrade testing later).
GKE_VERSION=$(gcloud container get-server-config --zone=europe-west2-a --format="value(validMasterVersions)" | tr ';' '\n' | sed -n '2p')

# Install GKE across three AZs, with 3 nodes per AZ.
gcloud container clusters create ${CLUSTER_NAME} \
--cluster-version ${GKE_VERSION} \
--node-locations europe-west2-a,europe-west2-b,europe-west2-c \
--num-nodes 1 \
--machine-type n1-standard-8

# Use the newly created GKE cluster with kubectl.
gcloud container clusters get-credentials ${CLUSTER_NAME}
```

##### CockroachDB

Install CockroachDB (older version to allow for updates)

```sh
kubectl apply -f cockroachdb/manifests/v25.2.0.yaml --wait
kubectl wait --for=jsonpath='{.status.phase}'=Running pods --all -n crdb --timeout=300s
kubectl exec -it -n crdb cockroachdb-0 -- /cockroach/cockroach init --insecure
```

Confirm that the nodes are spread across the available Kubernetes nodes

```sh
kubectl get pods -n crdb -o custom-columns=NAME:.metadata.name,HOST:.spec.nodeName
```

Fetch public IP of external load balancer (wait until the external IP is acailable)

```sh
CRDB_IP=$(kubectl get service cockroachdb-public -n crdb -o jsonpath='{.status.loadBalancer.ingress[0].ip}')
```

Test connections

```sh
cockroach sql --url "postgres://root@${CRDB_IP}:26257?sslmode=disable"

open "http://${CRDB_IP}:8080"
```

Create objects

```sh
cockroach sql --url "postgres://root@${CRDB_IP}:26257?sslmode=disable" -f cockroachdb/data/create.sql
```

Settings

```sql
-- Enable Leader Leases.
SET CLUSTER SETTING kv.raft.leader_fortification.fraction_enabled = 1;

-- Config for WAL Failover (as per https://www.cockroachlabs.com/docs/stable/wal-failover).
SET CLUSTER SETTING storage.max_sync_duration = '40s';

-- Config for client connections (greater than the sum of client's MaxConnLifetime and MaxConnLifetimeJitter).
SET CLUSTER SETTING server.shutdown.connections.timeout = '25s';

-- Aide in smooth rolling upgrades.
SET CLUSTER SETTING server.shutdown.initial_wait = '15s';



SHOW CLUSTER SETTING kv.raft.leader_fortification.fraction_enabled;
SHOW CLUSTER SETTING storage.max_sync_duration;
SHOW CLUSTER SETTING server.shutdown.connections.timeout;
SHOW CLUSTER SETTING server.shutdown.initial_wait;
```

##### Chaos Mesh

Install Chaos Mesh

```sh
curl -sSL https://mirrors.chaos-mesh.org/v2.7.0/install.sh | bash -s -- -r containerd
```

Security

```sh
# * * * UPDATE _rbac.yaml WITH YOUR ACCOUNT VALUES * * *
kubectl apply -f chaos_mesh/manifests/_rbac.yaml

# Run once per Google account.
# * * * UPDATE create_service_account.sh WITH YOUR ACCOUNT VALUES * * *
sh chaos_mesh/create_service_account.sh

SECRET=$(cat k8s-admin-sa-key.json | base64 | tr -d '\n') \
yq '.stringData.service_account = env(SECRET)' chaos_mesh/manifests/_secret.yaml \
> chaos_mesh/manifests/modified/_secret.yaml

kubectl apply -f chaos_mesh/manifests/modified/_secret.yaml
```

### Demo

Start workload

```sh
go run workload/main.go --url "postgres://root@${CRDB_IP}:26257?sslmode=disable"
```

Start polling containers (if you want to see pods going up and down)

```sh
see kubectl get pods -n crdb -o custom-columns="NAME:.metadata.name,READY:.status.containerStatuses[0].ready,STATUS:.status.phase,RESTARTS:.status.containerStatuses[0].restartCount,IMAGE:.spec.containers[0].image"
```

Perform rolling upgrades

```sh
kubectl apply -f cockroachdb/manifests/v25.2.1.yaml
```

Get pod names

```sh
pods=($(kubectl get pods -n crdb --field-selector=status.phase=Running -o custom-columns=NAME:.metadata.name --no-headers | sort))
```

Perform pod failures

```sh
# CockroachDB
for pod in "${pods[@]}"; do
  yq ".metadata.name = \"crdb\" | .spec.selector.namespaces = [\"crdb\"] | .spec.selector.labelSelectors[\"statefulset.kubernetes.io/pod-name\"] = \"${pod}\"" chaos_mesh/manifests/pod_failure.yaml > chaos_mesh/manifests/modified/crdb.yaml

  kubectl apply -f chaos_mesh/manifests/modified/crdb.yaml
  echo "Running pod failure against ${pod} for 30 second(s)"
  sleep 10

  kubectl delete -f chaos_mesh/manifests/modified/crdb.yaml
  echo "Recovering for 30 second(s)"
  sleep 30
done
```

Perform pod kills

```sh
# CockroachDB
for pod in "${pods[@]}"; do
  yq ".metadata.name = \"crdb\" | .spec.selector.namespaces = [\"crdb\"] | .spec.selector.labelSelectors[\"statefulset.kubernetes.io/pod-name\"] = \"${pod}\"" chaos_mesh/manifests/pod_kill.yaml > chaos_mesh/manifests/modified/crdb.yaml

  kubectl apply -f chaos_mesh/manifests/modified/crdb.yaml
  echo "Running pod kill against ${pod} for 30 second(s)"
  sleep 10

  kubectl delete -f chaos_mesh/manifests/modified/crdb.yaml
  echo "Recovering for 30 second(s)"
  sleep 30
done
```

Perform symmetric network partitions (short run)

```sh
# CockroachDB
i=0
for pod in "${pods[@]}"; do
  j=$(( (i + 1) % ${#pods[@]} ))
  
  source_pod=${pods[@]:$i:1}
  target_pod=${pods[@]:$j:1}

  yq ".metadata.name = \"crdb\" | .spec.selector.namespaces = [\"crdb\"] | .spec.selector.labelSelectors[\"statefulset.kubernetes.io/pod-name\"] = \"${source_pod}\" | .spec.target.selector.namespaces = [\"crdb\"] | .spec.target.selector.labelSelectors[\"statefulset.kubernetes.io/pod-name\"] = \"${target_pod}\"" chaos_mesh/manifests/network_partition_sym.yaml > chaos_mesh/manifests/modified/crdb.yaml

  kubectl apply -f chaos_mesh/manifests/modified/crdb.yaml
  echo "Creating symmetric partition between ${source_pod} and ${target_pod}"
  sleep 30

  kubectl delete -f chaos_mesh/manifests/modified/crdb.yaml
  echo "Recovering for 30 second(s)"
  sleep 30
done
```

Perform asymmetric network partitions (complete run)

```sh
# CockroachDB
i=0
for pod in "${pods[@]}"; do
  j=$(( (i + 1) % ${#pods[@]} ))
  
  source_pod=${pods[@]:$i:1}
  target_pod=${pods[@]:$j:1}

  yq ".metadata.name = \"crdb\" | .spec.selector.namespaces = [\"crdb\"] | .spec.selector.labelSelectors[\"statefulset.kubernetes.io/pod-name\"] = \"${source_pod}\" | .spec.target.selector.namespaces = [\"crdb\"] | .spec.target.selector.labelSelectors[\"statefulset.kubernetes.io/pod-name\"] = \"${target_pod}\"" chaos_mesh/manifests/network_partition_asym.yaml > chaos_mesh/manifests/modified/crdb.yaml

  kubectl apply -f chaos_mesh/manifests/modified/crdb.yaml
  echo "Creating asymmetric partition between ${source_pod} and ${target_pod}"
  sleep 30

  kubectl delete -f chaos_mesh/manifests/modified/crdb.yaml
  echo "Recovering for 30 second(s)"
  sleep 30
done 
```

Perform disk stalls

```sh
# CockroachDB
for pod in "${pods[@]}"; do
  yq ".metadata.name = \"crdb\" | .spec.selector.namespaces = [\"crdb\"] | .spec.selector.labelSelectors[\"statefulset.kubernetes.io/pod-name\"] = \"${pod}\"" chaos_mesh/manifests/disk_latency.yaml > chaos_mesh/manifests/modified/crdb.yaml

  kubectl apply -f chaos_mesh/manifests/modified/crdb.yaml
  echo "Stalling disk for ${pod}"
  sleep 30

  kubectl delete -f chaos_mesh/manifests/modified/crdb.yaml
  echo "Recovering for 30 seconds"
  sleep 30
done
```

### Teardown

Perform rolling downgrade

```sh
kubectl apply -f cockroachdb/manifests/v25.1.5.yaml
```

Chaos Mesh

```sh
curl -sSL https://mirrors.chaos-mesh.org/v2.7.2/install.sh | bash -s -- --template | kubectl delete -f -
```

CockroachDB

```sh
kubectl delete -n crdb -f cockroachdb/manifests/v25.2.0.yaml --wait

# Or the latest version.
kubectl delete -n crdb -f cockroachdb/manifests/v25.2.1.yaml --wait
```

GKE cluster

```sh
gcloud container clusters delete ${CLUSTER_NAME} --quiet
```
