# Running LLMGateway-Go locally

Three ways to run the gateway on your machine, cheapest first. Pick by what
you want to exercise:

| Path | Exercises | Needs |
|------|-----------|-------|
| **A. Bare binary** | config load, HTTP edge, auth rejection | Go only |
| **B. Docker container** | the shipped image, nonroot, SIGTERM drain | Docker |
| **C. Kubernetes (Docker Desktop)** | ServiceAccount identity, probes, the JWKS/x509 failure the design predicts | Docker Desktop + Kubernetes |

All three verify the same **behavioural contract**. Until the step‑4 JWKS fix
lands, these are the correct answers — a red `/readyz` is the design working,
not a bug:

| Request | Expected | Why |
|---------|----------|-----|
| `GET /healthz` | **200** | liveness is unconditional |
| `GET /readyz` | **503** | fail‑closed: no signing keys loaded yet |
| `POST /v1/chat` (any/garbage token) | **401** | unknown caller |
| `GET /metrics` | **404** | Prometheus adapter not wired yet |

---

## A. Bare binary — the 30‑second loop

```sh
go run ./cmd/gateway -config config/config.yaml
```

In another terminal:

```sh
curl -s -o /dev/null -w '%{http_code}\n' localhost:8080/healthz   # 200
curl -s -o /dev/null -w '%{http_code}\n' localhost:8080/readyz    # 503
curl -s -o /dev/null -w '%{http_code}\n' -X POST localhost:8080/v1/chat \
  -H 'Authorization: Bearer sk-anything' -H 'Content-Type: application/json' \
  -d '{"model":"gpt-4.1","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}'  # 401
```

Stop with `Ctrl‑C` — you'll see `shutdown initiated` then `gateway stopped`.

> The local `config/config.yaml` points `jwks_url` at `http://localhost:8001`
> (a `kubectl proxy`). With nothing on that port you'll see a
> `jwks refresh failed` warning every interval — that is the
> fail‑closed path logging, and why `/readyz` is 503. Harmless here.

---

## B. Docker container — the shipped image

Build once (multi‑stage; final image is distroless, ~22 MB, runs as nonroot):

```sh
docker build -t llm-gateway:dev .
```

Run it, mounting your local config in (the image ships **no** config — that
arrives at runtime, from a bind mount here and a ConfigMap in Kubernetes):

```sh
docker run --rm -p 8080:8080 \
  -v "$PWD/config:/config" \
  llm-gateway:dev -config /config/config.yaml
```

Same three curls as Path A. Then prove graceful shutdown — Docker sends
`SIGTERM`, which the gateway catches and drains:

```sh
docker ps                      # find the container id / name
docker stop <name>             # returns quickly, not after a 10s SIGKILL
```

Sanity checks on the image itself:

```sh
docker image ls llm-gateway:dev                                   # ~22MB
docker inspect llm-gateway:dev --format '{{.Config.User}}'        # 65532 (nonroot)
```

---

## C. Kubernetes on Docker Desktop

This is the interesting one: it exercises the pod's **ServiceAccount
identity**, wires your `health.Checker` to real readiness/liveness probes, and
reproduces the exact `x509: certificate signed by unknown authority` failure
the architecture predicts (fixed later in TODO step 4).

### C.1 Enable Kubernetes in Docker Desktop

Docker Desktop → **Settings** → **Kubernetes** → tick **Enable Kubernetes** →
**Apply & Restart**. Wait for the Kubernetes indicator (bottom‑left) to turn
green. This creates a single‑node cluster and a kubeconfig context named
`docker-desktop`.

### C.2 Point kubectl at it — **do this first**

Your machine currently defaults to an **EKS** context (an expired AWS SSO
session; every `kubectl` call errors with
`aws ... SSO session ... has expired`). Switch to the local cluster:

```sh
kubectl config get-contexts                 # see what you have
kubectl config use-context docker-desktop   # switch to the local one
kubectl cluster-info                         # should print a localhost API server
```

> Remember to switch back (`kubectl config use-context <your-eks>`) when done,
> or run the local commands with `--context docker-desktop` explicitly.

### C.3 Build the image — then load it into the node

```sh
docker build -t llm-gateway:dev .
docker save llm-gateway:dev | docker exec -i desktop-control-plane ctr -n k8s.io images import -
```

**Why the second command exists.** Newer Docker Desktop versions provision
Kubernetes as a kind‑style node — a container named `desktop-control-plane`
running its **own containerd with its own image store**. A `docker build`
lands only in the Docker CLI's store; the node never sees it. The import
pipes the image into the node's containerd (the `k8s.io` namespace is where
the kubelet looks), which combined with `imagePullPolicy: IfNotPresent`
means the pod uses it and never reaches for a registry.

> **The trap this prevents:** both stores can hold `llm-gateway:dev` at
> *different digests* — your rebuild in one, a stale copy in the other — and
> `IfNotPresent` will happily run the stale one, forever, with no error.
> If a pod's behavior doesn't match your code, compare digests:
>
> ```sh
> docker image ls --digests llm-gateway                                   # what you built
> kubectl get pods -n llm -o jsonpath='{.items[0].status.containerStatuses[0].imageID}'  # what runs
> ```
>
> (On older Docker Desktop versions the node shared the Docker image store
> and no load step existed; if `docker ps` shows no `desktop-control-plane`
> container, you're on that setup and can skip the import. On `kind` itself,
> the equivalent is `kind load docker-image llm-gateway:dev --name llm`.)

### C.4 Apply the manifests

```sh
kubectl apply -f deploy/
```

Server‑side dry‑run to validate against the live API first, if you like:

```sh
kubectl apply -f deploy/ --dry-run=server
```

### C.5 Watch the predicted failure

```sh
kubectl get pods -n llm -w
```

The pod stays **`0/1 Ready`** — forever, on purpose. Look at why:

```sh
kubectl logs -n llm deploy/llm-gateway
```

You'll see, every refresh interval:

```
jwks refresh failed ... x509: certificate signed by unknown authority
```

The API server's TLS cert is signed by the *cluster's* CA, which Go's default
HTTP client has never heard of. `JWKSCache.Ready` stays failed until keys load,
so readiness stays red — **fail‑closed working as designed**. The Service has
no ready endpoints, so it deliberately routes no traffic to an instance that
would 401 everyone.

### C.6 Curl the pod directly

`kubectl port-forward` to the **pod/Deployment** bypasses the Service's
readiness gate, so you can probe even while the pod is `0/1`:

```sh
kubectl port-forward -n llm deploy/llm-gateway 8080:8080
```

Then, in another terminal, the same contract as everywhere else:

```sh
curl -s -o /dev/null -w '%{http_code}\n' localhost:8080/healthz   # 200
curl -s -o /dev/null -w '%{http_code}\n' localhost:8080/readyz    # 503 (no keys)
```

### C.7 Exercise the identity path with a minted token

The caller ServiceAccounts (`agent-service`, `rag-api`, `support-bot`) exist
from `deploy/callers.yaml`. Mint a bound token for one:

```sh
TOKEN=$(kubectl create token agent-service -n llm --audience=llm-gateway)

curl -s -o /dev/null -w '%{http_code}\n' -X POST localhost:8080/v1/chat \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"model":"gpt-4.1","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}'
```

**Before the step‑4 fix:** this returns **401**. The token is genuine, but the
gateway can't verify its signature — the JWKS keys never loaded (the x509
failure above). That is the whole point of reproducing this first.

**After the step‑4 fix** (`auth.NewFetchClient` trusts the cluster CA and
sends the pod's own token): the same pod goes **`1/1 Ready`**, the x509 line
disappears, and this minted token earns a domain error — auth passed end to
end, and the placeholder provider answered honestly with no model behind it.
*Which* error depends on the caller's `failover_policy`: `agent-service`
(`same-model`) gets **503 model_unavailable**, `rag-api` (`any`) gets
**502 upstream_failed** — see the 503‑vs‑502 subsection in
[pod-logs-and-rollout.md](pod-logs-and-rollout.md) for why that distinction
is the core's decision #5 working. A garbage token still gets 401. The
system fails exactly where it should and nowhere else.

### C.8 Observe it all in k9s (optional, but much nicer)

[k9s](https://k9scli.io) is a terminal UI over `kubectl` — live views that
refresh themselves, with one‑key logs / describe / port‑forward. It collapses
most of Path C into a few keystrokes.

Install (macOS):

```sh
brew install k9s
```

Launch it scoped to the local cluster and namespace:

```sh
k9s --context docker-desktop -n llm
```

> If k9s opens on the wrong cluster (your EKS context), press `:`, type `ctx`,
> Enter, then pick `docker-desktop`. `:q` quits k9s at any time; `?` shows the
> full keymap.

Inside k9s, the same work as C.5–C.7 becomes:

| You want | Keys in k9s |
|----------|-------------|
| The pod's live `READY`/`STATUS` (no `-w` needed) | `:` `pods` ↵ — the gateway shows `0/1` `Running`, refreshing on its own |
| Read the logs, hunt the x509 line | highlight the pod, press `l`; `/x509` ↵ to filter, `Esc` to leave |
| Inspect probe failures and pod events | highlight the pod, press `d` (describe) |
| Port‑forward to curl it | highlight the pod, press `Shift‑F`, accept container `8080` → local `8080`; list active forwards with `:` `pf` ↵ |
| Watch cluster events scroll by | `:` `events` ↵ |
| Switch namespace | `:` `ns` ↵, pick one |
| Delete a resource / the namespace | highlight it, `Ctrl‑D` (asks to confirm) |

> Pressing `s` to shell into the pod **fails** — the image is distroless and
> has no shell. That is the Dockerfile's security property seen from the
> driver's seat: nothing for an attacker (or you) to `exec` into.

The failure you're reproducing reads especially clearly here: the pod sits at
`0/1` in red, `d` shows the readiness probe failing, and `l` shows the x509
line on a loop — the whole fail‑closed story on one screen.

#### Killing or deleting a pod in k9s

From the pods view (`:pods` ↵), highlight the pod with the arrow keys, then:

| Key | What it does | When to reach for it |
|-----|--------------|----------------------|
| `Ctrl‑D` | **Delete** — opens a confirm dialog where you can set the grace period; sends `SIGTERM` and lets the pod drain | the normal choice; also how you watch the graceful‑shutdown path (`shutdown initiated` → `gateway stopped` in the logs) |
| `Ctrl‑K` | **Kill** — force‑delete with grace period 0, no confirmation | a pod that's wedged and won't drain; skips the SIGTERM drain, so don't use it to test shutdown |

The catch worth understanding: this Deployment declares `replicas: 2`, and the
pods are **owned by** it (via a ReplicaSet). Deleting one pod doesn't remove
it — Kubernetes immediately schedules a replacement to restore the count. So
`Ctrl‑D` on a pod is effectively "restart this one replica," which is exactly
what you want to:

- recover a single wedged pod without touching the others, or
- force a fresh start — though note a recreated pod pulls the **same** image
  tag, so to ship new code you still need the rebuild + `rollout restart` from
  C.9, not a pod delete.

To make a pod stay gone, you delete its **owner**: highlight the Deployment
(`:deploy` ↵) and `Ctrl‑D`, or delete the whole namespace (`:ns` ↵, highlight
`llm`, `Ctrl‑D`) as in C.10. Watching the delete‑and‑recreate live is a good
way to *see* self‑healing: delete one pod in the pods view and the count
blinks from `2` to `1` and back to `2` on its own.

### C.9 Iterate after a change — reset and re‑test

Once you change code (or a manifest), you have to get that change in front of
the running system. How much you tear down depends on **what** you changed.

**Bare binary / Docker.** No reset ritual — the process holds nothing between
runs. `Ctrl‑C` (or `docker stop <name>`) and re‑run. For Docker you must
rebuild first, because the container runs the image, not your working tree:

```sh
docker build -t llm-gateway:dev . && \
docker run --rm -p 8080:8080 -v "$PWD/config:/config" llm-gateway:dev -config /config/config.yaml
```

**Kubernetes — you changed Go code.** Rebuild, load into the node, then
force new pods. Two traps stack here: `kubectl apply` sees an identical
Deployment spec (same tag) and does **nothing**; and even `rollout restart`
alone reruns whatever image the **node's** store holds — which after a local
rebuild is the *stale* one, because the node (`desktop-control-plane`) has
its own containerd store the `docker build` never touched (C.3). All three
steps, every time:

```sh
docker build -t llm-gateway:dev .
docker save llm-gateway:dev | docker exec -i desktop-control-plane ctr -n k8s.io images import -
kubectl rollout restart deploy/llm-gateway -n llm
kubectl rollout status  deploy/llm-gateway -n llm   # waits for the new pod
kubectl get pods -n llm -w
```

> This is exactly the step‑4 verify loop: after `auth.NewFetchClient` lands,
> rebuild + `rollout restart` and watch the *same* Deployment's new pod go
> `1/1 Ready` with the x509 line gone.

**Kubernetes — you changed a manifest or config.** `kubectl apply -f deploy/`
is enough for the API objects themselves; it's declarative, so re‑applying
converges the cluster to the files. Two catches:

- A **ConfigMap** change updates the object, but a pod reads `config.yaml`
  only at boot — follow it with a `kubectl rollout restart` so a new pod loads
  the new config.
- A field that is **immutable** (rare here — e.g. some `Service`/selector
  edits) will make `apply` error. That's your cue for the full reset below.

**Full reset — start from a clean slate.** When the cluster is in a confused
state, or you want to prove the manifests bring the world up from nothing,
nuke the namespace and re‑apply. Deleting `llm` takes everything in it —
pods, SAs, ConfigMap, Service — with it:

```sh
kubectl delete namespace llm        # or: kubectl delete -f deploy/
kubectl get ns llm                  # wait until this reports NotFound
docker build -t llm-gateway:dev .   # only if code changed
kubectl apply -f deploy/
kubectl get pods -n llm -w
```

In k9s: `:ns` ↵, highlight `llm`, `Ctrl‑D` to delete; for the code‑only loop,
highlight the pod and `Ctrl‑D` — the Deployment recreates it, but that pulls
the *same* image, so still run `docker build` + `:rollout` (or the
`rollout restart` above) to actually ship new code.

### C.10 Clean up

```sh
kubectl delete -f deploy/          # or: kubectl delete namespace llm
kubectl config use-context <your-eks-context>   # restore your default
```

In k9s you can do the same without leaving the TUI: highlight the `llm`
namespace in the `:ns` view and press `Ctrl‑D`.

> This removes the running workload but **leaves the built images behind** —
> both the Docker CLI's `llm-gateway:dev` and the node's own copy. And if you
> reach for `kubectl delete namespace llm`, it also **orphans the
> ClusterRoleBinding** (it is cluster‑scoped, not in the namespace). For a
> from‑nothing teardown that leaves no trace, use C.11.

### C.11 Complete teardown — every object and both image stores

Use this when you want the machine back exactly as it was before Path C:
no `llm` namespace, no cluster‑scoped leftovers, and **neither** image store
holding `llm-gateway:dev`. Three things were created, so three things get
removed — in this order.

**1. Kubernetes objects — including the cluster‑scoped binding.** Delete by
manifest, not by namespace: `deploy/rbac.yaml` is a **ClusterRoleBinding**
(`llm-gateway-issuer-discovery`), which lives *outside* the namespace, so
`kubectl delete namespace llm` would leave it dangling. `kubectl delete -f
deploy/` removes both the namespace (which cascades every namespaced object —
Deployment, pods, SAs, ConfigMap, Service) **and** that binding.

```sh
kubectl delete -f deploy/ --ignore-not-found   # namespace + ClusterRoleBinding, cascades the rest
kubectl get ns llm                             # wait until: NotFound
kubectl get clusterrolebinding llm-gateway-issuer-discovery   # NotFound too
```

> If you had already run `kubectl delete namespace llm`, the namespaced
> objects are gone but the binding is not. Remove the leftover directly:
>
> ```sh
> kubectl delete clusterrolebinding llm-gateway-issuer-discovery --ignore-not-found
> ```

**2. The image in the node's containerd store.** This is the copy the
`docker save … | ctr … import` step (C.3) pushed into `desktop-control-plane`
— a *separate* store from the Docker CLI's, so `docker image rm` never
touches it. List first to confirm the exact ref (`ctr` normalizes it to a
fully‑qualified name), then remove:

```sh
docker exec desktop-control-plane ctr -n k8s.io images ls | grep llm-gateway
docker exec desktop-control-plane ctr -n k8s.io images rm docker.io/library/llm-gateway:dev
```

> If `docker ps` shows no `desktop-control-plane` container, you're on the
> older Docker Desktop that shares the Docker image store — there is no
> separate node copy, so skip this step (step 3 removes the only copy). On
> `kind` proper the node is `llm-control-plane`; swap the container name.

**3. The image in the Docker CLI store.** The one your `docker build`
produced:

```sh
docker image rm llm-gateway:dev
```

**4. Restore your default context** (Path C left you on `docker-desktop`):

```sh
kubectl config use-context <your-eks-context>
```

**Verify nothing survived:**

```sh
kubectl get ns llm                                             # NotFound
kubectl get clusterrolebinding llm-gateway-issuer-discovery    # NotFound
docker image ls llm-gateway                                    # empty
docker exec desktop-control-plane ctr -n k8s.io images ls | grep llm-gateway   # no output
```

> Turning Kubernetes off entirely (Docker Desktop → **Settings** →
> **Kubernetes** → untick **Enable Kubernetes**) or hitting **Reset Kubernetes
> Cluster** tears down the whole single‑node cluster in one move — a bigger
> hammer than this, and it takes the node's containerd store (step 2) with it,
> but it also destroys any *other* namespaces you were running. The four steps
> above remove only what this project created.

---

## Troubleshooting

| Symptom | Cause | Fix |
|---------|-------|-----|
| `aws ... SSO session ... expired` on every kubectl call | still pointed at EKS | `kubectl config use-context docker-desktop` (C.2) |
| `kubectl` hangs / `connection refused` to API server | Kubernetes not finished starting | wait for the green indicator in Docker Desktop |
| Pod `ImagePullBackOff` / `ErrImagePull` | k8s can't see the local image | rebuild with `docker build -t llm-gateway:dev .`; confirm context is `docker-desktop`; as a fallback set the Deployment's `imagePullPolicy: Never` |
| Pod `0/1 Ready` with the x509 log line | **expected** pre‑fix | this is the TODO step‑3 failure you're reproducing |
| Rebuilt the image but the pod still runs old code | `apply` saw no spec change; same tag isn't re‑pulled on its own | load the image into the node, then `kubectl rollout restart deploy/llm-gateway -n llm` (C.9) |
| Pod's `imageID` digest ≠ `docker image ls --digests` | the node's own store holds a stale `llm-gateway:dev`; `IfNotPresent` runs it silently | the `docker save … \| docker exec … ctr … import` step (C.3), then rollout restart |
| Edited a ConfigMap but behaviour is unchanged | the pod read `config.yaml` at boot only | `kubectl rollout restart` after the apply (C.9) |
| Minted token returns 401 | **expected** pre‑fix — keys never loaded | becomes 503/502 (by `failover_policy`) after the step‑4 JWKS fix — see pod-logs-and-rollout.md |
| `CreateContainerConfigError` about read‑only FS | app tried to write outside stdout | the gateway shouldn't; check `readOnlyRootFilesystem` volume mounts if you changed them |
| Port 8080 already in use | the bare binary or a container is still running | stop it, or forward to a different local port: `... 18080:8080` |
| k9s shows the wrong cluster / no `llm` namespace | opened on the EKS context | inside k9s: `:ctx` ↵ → `docker-desktop`; or launch with `k9s --context docker-desktop` |

## Quick reference

```sh
# Bare binary
go run ./cmd/gateway -config config/config.yaml

# Docker
docker build -t llm-gateway:dev .
docker run --rm -p 8080:8080 -v "$PWD/config:/config" llm-gateway:dev -config /config/config.yaml

# Kubernetes (Docker Desktop)
kubectl config use-context docker-desktop
docker build -t llm-gateway:dev .
docker save llm-gateway:dev | docker exec -i desktop-control-plane ctr -n k8s.io images import -
kubectl apply -f deploy/
kubectl get pods -n llm -w
kubectl port-forward -n llm deploy/llm-gateway 8080:8080
kubectl create token agent-service -n llm --audience=llm-gateway

# Re‑test after a code change (rebuild, load into the node, force new pods — same tag won't self‑update)
docker build -t llm-gateway:dev .
docker save llm-gateway:dev | docker exec -i desktop-control-plane ctr -n k8s.io images import -
kubectl rollout restart deploy/llm-gateway -n llm
kubectl rollout status  deploy/llm-gateway -n llm

# Full reset — bring the world up from nothing
kubectl delete namespace llm        # wait for NotFound
kubectl apply -f deploy/

# Complete teardown — every object + both image stores (C.11)
kubectl delete -f deploy/ --ignore-not-found                                    # namespace + cluster-scoped ClusterRoleBinding
docker exec desktop-control-plane ctr -n k8s.io images rm docker.io/library/llm-gateway:dev  # node's containerd store
docker image rm llm-gateway:dev                                                 # Docker CLI store
kubectl config use-context <your-eks-context>

# Kubernetes, observed in k9s (TUI) instead of raw kubectl
brew install k9s
k9s --context docker-desktop -n llm
#   :pods ↵     live pod list        l  logs (/x509 to filter)
#   d  describe (probe failures)     Shift-F  port-forward 8080→8080
#   :ns ↵  switch namespace          Ctrl-D  delete    :q  quit
```
