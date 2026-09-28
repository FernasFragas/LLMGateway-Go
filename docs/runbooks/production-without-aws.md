# Production, without an AWS account

A companion to [`production-operations.md`](../production-operations.md), not a replacement. That document is the reference: it explains *why* each step exists. This one is the sequence to follow when you have no AWS account, using **GHCR** for the image and a **non-AWS managed Kubernetes cluster** for the runtime.

Only five things in the EKS path are actually AWS. Everything else — the issuer check, ServiceAccount identity, the config, readiness, rotation, the whole operating story — is plain Kubernetes and unchanged.

| Reference doc | Here |
|---|---|
| ECR private registry | GHCR (`ghcr.io`), free with a GitHub account |
| Node role grants the image pull | An `imagePullSecret` — see Step 4, and read it, because the manifests do not ship one |
| ElastiCache with an auth token | Redis in the cluster with `requirepass`, mounted as a file |
| VPC / security group wording | Your provider's equivalent, or nothing on a single-node cluster |
| `AmazonEC2ContainerRegistryReadOnly` | Not applicable |

## Why not Fly.io

Fly runs containers, and this gateway needs a *cluster*. Its callers prove who they are with bound ServiceAccount tokens that the gateway verifies against the cluster's JWKS endpoint — no Kubernetes API server, no tokens to verify, and `auth`, `rbac.yaml`, and every `apps[].subject` become meaningless. Fly is a fine host for a stub upstream if you later want one to demo failover against; it is not a host for this.

## What you need first

- A GitHub account, and a **classic PAT with `write:packages`** (fine-grained tokens work too — grant *Packages: read and write*).
- `kubectl`, `docker` with `buildx`, `jq`.
- Your provider's CLI. This guide uses **DigitalOcean** (`doctl`) because it is the shortest path to a real managed cluster; GKE, AKS, Civo, Scaleway, or k3s on any VM work identically from Step 2 onward.

---

## Step 1 — Create the cluster

```sh
doctl kubernetes cluster create llm --region lon1 --size s-2vcpu-4gb --count 2
kubectl config current-context      # doctl merges and selects it for you
kubectl get nodes
```

**Why two nodes:** not for the quota check — two *replicas* prove that, and Docker Desktop already does (`local-testing.md` §C.7.3). Two nodes buy the things one machine cannot fake: scheduling across failure domains, a node drain taking one replica while the other serves, and whether a PodDisruptionBudget would have mattered. If you do not care about those yet, `--count 1` halves the bill.

**Cheaper options.** GKE and AKS give a free control plane and new-account credits. Oracle Cloud's Always Free ARM capacity runs k3s indefinitely at no cost. A €5/month VM with `curl -sfL https://get.k3s.io | sh -` gives you a real cluster with a real OIDC issuer, which is all this guide needs.

⚠️ **Note your node architecture now.** `kubectl get nodes -o jsonpath='{.items[0].status.nodeInfo.architecture}'`. On any ARM option, Step 2 becomes `--platform linux/arm64`. Getting this backwards is the `exec format error` the reference doc's Step 1 warns about, in the other direction.

**Cost control:** this cluster bills by the hour. Delete it the moment you are done — see Teardown.

---

## Step 2 — Build for the cluster's architecture

```sh
TAG=$(git rev-parse --short HEAD)
docker buildx build --platform linux/amd64 -t llm-gateway:$TAG .
```

Same as the reference doc's Step 1. Swap `amd64` for `arm64` if Step 1 said so.

---

## Step 3 — Push to GHCR, privately

```sh
GH_USER=<your-github-username>        # lowercase — GHCR rejects capitals in the path
REPO=ghcr.io/$GH_USER/llm-gateway

echo $CR_PAT | docker login ghcr.io -u $GH_USER --password-stdin

docker tag llm-gateway:$TAG $REPO:$TAG
docker push $REPO:$TAG
```

**New GHCR packages are private by default**, and a package's visibility is independent of its source repository's — a public repo does not publish your image. Confirm at `github.com/users/<you>/packages`, then prove it the way the reference doc does, by pulling as nobody:

```sh
docker logout ghcr.io
docker pull $REPO:$TAG           # expect: denied / unauthorized
docker login ghcr.io -u $GH_USER --password-stdin <<< "$CR_PAT"
```

**Why prove it rather than read a setting:** a setting describes intent; the pull tests the thing itself.

---

## Step 4 — Give the cluster permission to pull

This step has no equivalent in the EKS path, and skipping it is the most likely way this guide fails.

```sh
kubectl create namespace llm --dry-run=client -o yaml | kubectl apply -f -

kubectl -n llm create secret docker-registry ghcr \
  --docker-server=ghcr.io \
  --docker-username=$GH_USER \
  --docker-password=$CR_PAT \
  --dry-run=client -o yaml | kubectl apply -f -
```

Then add one line to `deploy/deployment.yaml`, in the pod spec beside `serviceAccountName`:

```yaml
    spec:
      serviceAccountName: llm-gateway
      imagePullSecrets:
        - name: ghcr
```

**Why:** on EKS the node's IAM role authenticates every pull, so no secret is needed and the shipped manifest has none. Any other cluster pulling from a private registry has no such identity — without this you get `ImagePullBackOff` with a 401, and the fix is never to make the image public.

---

## Step 5 — Point the Deployment at your image

In `deploy/deployment.yaml`:

```yaml
image: ghcr.io/<your-github-username>/llm-gateway:<the-sha-from-step-2>
imagePullPolicy: IfNotPresent
```

Keep `IfNotPresent` — with an immutable SHA tag it is correct. Never a moving tag like `latest`.

---

## Step 6 — Redis, with a password

The reference doc's Step 4 provisions a managed cache. In-cluster with authentication exercises the same code path — the client's `AUTH` handshake, and a password read from a file rather than config — at no cost.

```sh
kubectl -n llm create secret generic redis-auth \
  --from-literal=password="$(openssl rand -base64 24)" \
  --dry-run=client -o yaml | kubectl apply -f -
```

Start Redis with it, and mount the same Secret into the gateway. In `deploy/dev/redis.yaml`, add to the Redis container:

```yaml
          args: ["--requirepass", "$(REDIS_PASSWORD)"]
          env:
            - name: REDIS_PASSWORD
              valueFrom:
                secretKeyRef: {name: redis-auth, key: password}
```

And in `deploy/deployment.yaml`, a third volume and mount for the gateway:

```yaml
          volumeMounts:
            - name: redis-auth
              mountPath: /etc/gateway/redis
              readOnly: true
      volumes:
        - name: redis-auth
          secret:
            secretName: redis-auth
```

Then in `deploy/configmap.yaml`:

```yaml
redis:
  addr: redis.llm.svc.cluster.local:6379
  password_path: /etc/gateway/redis/password
```

**Why a path and not a value:** a credential in the ConfigMap is a credential in git. An unreadable password file is a deliberate boot failure — quotas fail open, so a client that connects and is then refused would leave every limit silently unenforced.

> **`tls: true` is skipped here on purpose.** Pod-to-pod traffic inside one cluster does not need it, and a self-signed CA adds steps without teaching anything new. If you want the TLS path exercised, use a managed Redis with encryption in transit and set `tls` and `ca_path` per §1.2 of the reference doc — the gateway supports a private CA precisely for that case.

---

## Step 7 — The OTLP collector

```sh
kubectl apply -f deploy/dev/otel-collector.yaml
```

Unchanged from local. It ships the `debug` exporter, so metrics land in the collector's log — enough to complete this guide. Swap in your vendor's exporter (Grafana Cloud's free tier is the cheapest real backend) when you want dashboards.

---

## Step 8 — Provider keys

```sh
kubectl -n llm create secret generic llm-gateway-provider-keys \
  --from-file=openai=./openai.key \
  --from-file=anthropic=./anthropic.key \
  --dry-run=client -o yaml | kubectl apply -f -
```

Identical to the reference doc's Step 6, and cloud-independent. Fake values are fine and useful — they make providers answer 401, which exercises failover and the out-of-band key refresh without spending tokens.

---

## Step 9 — Set the issuer, the one thing that will fail your boot

```sh
kubectl get --raw /.well-known/openid-configuration | jq -r .issuer
```

On DOKS this prints something like `https://<cluster-id>.k8s.ondigitalocean.com`; on GKE a `container.googleapis.com` URL. Whatever it is, it is **not** `kubernetes.default.svc.cluster.local`, which is what `deploy/configmap.yaml` ships.

Put that exact value in `auth.issuer`. Leave `jwks_url` at `https://kubernetes.default.svc/openid/v1/jwks` — the in-cluster path still serves the keys.

**Why this one gets its own step:** the pod refuses to start on a wrong issuer and names both values in the error, so you cannot get it silently wrong. Read §1.1 of the reference doc for what that check is protecting you from.

---

## Step 10 — Deploy and confirm both gates opened

```sh
kubectl apply -f deploy/
kubectl -n llm rollout status deploy/llm-gateway
kubectl -n llm get pods
```

If a pod sits at `0/1`:

```sh
kubectl -n llm logs deploy/llm-gateway | grep "readiness refused"
```

`jwks` means the RBAC binding is missing or the endpoint is unreachable; `provider-keys` means no credential loaded. If pods are `ImagePullBackOff` instead, go back to Step 4.

---

## Step 11 — Prove it behaves like production

Port-forward, then run the checks from [`local-testing.md`](../local-testing.md) §C.7 — they are cloud-independent and this is where the cluster earns its cost:

```sh
kubectl -n llm port-forward deploy/llm-gateway 18080:8080
```

Worth doing in this order:

1. **Identity** — `kubectl create token rag-api -n llm --audience=llm-gateway`, then a request. A real token gets a domain answer; a garbage one gets 401.
2. **Rate limits across replicas** — 20 concurrent requests as `support-bot` (rps 10) → ten 429, ten 502. This is the check a single-node local cluster cannot make honest: the counter is in Redis, so the limit holds across both pods.
3. **Fail open** — `kubectl -n llm scale deploy/redis --replicas=0`, send traffic, `grep unenforced`. Everything is served, the degradation is logged. `--replicas=1` to restore.
4. **Key rotation without a restart** — re-apply the Secret with a different value and watch the refresh log line. No `rollout restart`.
5. **Zero-drop deploy** — `kubectl -n llm set image ...` with a new SHA tag while traffic is flowing.

---

## Teardown — do this the same day

```sh
doctl kubernetes cluster delete llm
```

**Why it matters more here than on a laptop:** managed clusters bill hourly whether or not you are looking at them. Deleting the cluster removes everything in it; your GHCR package is free and can stay.

---

## What is still different from the EKS path

- **No `IMMUTABLE` tag enforcement.** ECR can refuse to overwrite a tag; GHCR does not offer the equivalent, so "never reuse a tag" stays a discipline rather than a guarantee.
- **No image scanning on push.** Add `trivy image $REPO:$TAG` locally if you want it.
- **Your `imagePullSecrets` edit is local.** It is not in the committed manifests, because on EKS and GKE it would be wrong. Keep it out of the commit, or gate it behind an overlay.
- **Everything in §11 of the reference doc still applies** — no HPA, no PodDisruptionBudget, no NetworkPolicy, no tracing, no circuit breaker. Those are gaps in the repo, not in this path.

## Quick reference

```sh
# build + push
TAG=$(git rev-parse --short HEAD)
docker buildx build --platform linux/amd64 -t llm-gateway:$TAG .
docker tag llm-gateway:$TAG ghcr.io/$GH_USER/llm-gateway:$TAG
docker push ghcr.io/$GH_USER/llm-gateway:$TAG

# prove the image is private
docker logout ghcr.io && docker pull ghcr.io/$GH_USER/llm-gateway:$TAG   # want: denied

# the issuer, every time you make a new cluster
kubectl get --raw /.well-known/openid-configuration | jq -r .issuer

# deploy + watch
kubectl apply -f deploy/ && kubectl -n llm rollout status deploy/llm-gateway
kubectl -n llm logs deploy/llm-gateway | grep "readiness refused"

# stop paying
doctl kubernetes cluster delete llm
```
