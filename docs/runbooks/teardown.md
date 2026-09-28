# Teardown — remove everything this project created

Run top to bottom. Each step names what it removes and why the previous step didn't.

Longer explanations live in [`local-testing.md`](../local-testing.md) §C.10.

---

## 1. Stop what's still running

```sh
pkill -f "port-forward deploy/llm-gateway"
pkill -f "kubectl proxy"
docker stop llm-gateway 2>/dev/null                     # `make docker-run`
docker ps --filter ancestor=redis:7-alpine -q | xargs -r docker stop
```

Background port-forwards survive the pods they pointed at and keep a port bound. The local Redis from the bare-binary path is started unnamed, so it is found by image rather than by name.

## 2. Kubernetes objects

```sh
kubectl delete -f deploy/ -f deploy/dev/ --ignore-not-found
```

**Not** `kubectl delete namespace llm`. Three things live outside that namespace: the `ClusterRoleBinding`, the `observability` namespace, and the image. Deleting only the namespace leaves a binding that silently starts working again if you recreate the namespace under the same names — so the RBAC step never gets re-tested.

If you already deleted the namespace:

```sh
kubectl delete clusterrolebinding llm-gateway-issuer-discovery --ignore-not-found
kubectl delete namespace observability --ignore-not-found
```

## 3. The image, from both stores

```sh
docker exec desktop-control-plane crictl rmi llm-gateway:dev   # the node's store
docker image rm llm-gateway:dev                                # the Docker CLI's store
```

Two stores, two commands. The Kubernetes node has its own containerd store, so `docker image rm` does not touch what the kubelet runs. Use `crictl rmi`, not `ctr images rm` — containerd registers the image under both a tag and a digest, and `ctr` removes only the name you give it.

## 4. Test containers

```sh
docker ps -a --filter ancestor=redis:7-alpine
docker ps -a --filter name=reaper
```

`make test-integration` starts Redis containers via testcontainers. Its reaper removes them when the test binary exits, but a killed run (`Ctrl-C`, a panic) can leave one behind.

## 5. Local files

```sh
make clean                       # bin/, coverage.out, coverage.html
rm -f gateway                    # a root-level build; make clean misses it
rm -f config/config.yaml         # your operator-local config
rm -rf secrets/ keys-provider/   # local credential files
```

`config/config.yaml`, `secrets/`, `gateway`, and the build output are all gitignored, so `git status` will not remind you they exist. **`keys-provider/` is not** — it holds credential files and nothing stops you committing it.

## 6. Restore your context

```sh
kubectl config use-context <your-normal-context>
```

Otherwise the next unrelated `kubectl` command runs against Docker Desktop.

---

## Verify nothing is left

Every command below should print nothing or `NotFound`:

```sh
kubectl get ns llm observability
kubectl get clusterrolebinding llm-gateway-issuer-discovery
docker exec desktop-control-plane ctr -n k8s.io images ls | grep llm-gateway
docker image ls | grep llm-gateway
docker ps -a | grep -E "llm-gateway|redis"
```

---

## If you deployed to a cloud cluster

`kubectl delete -f deploy/` removes the workload but nothing the platform provides — those were created outside this repo and are billed until deleted:

- the **provider-keys Secret**, if it lives in a secret manager rather than only in the cluster
- the **managed Redis** instance and its security group rule
- the **image repository** (ECR/GCR/ACR) and its stored tags
- the **OTel collector** and whatever backend it exports to
- any **NetworkPolicy, HPA, or PodDisruptionBudget** you added — none ship in `deploy/`

Delete the cluster itself only if it exists for this project; `deploy/` never created it.
