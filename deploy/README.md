# `deploy/` — the manifest set

```sh
kubectl apply -f deploy/
```

Namespace, caller ServiceAccounts, config, RBAC, Service, and the Deployment.
Applied to a cluster that already provides the three dependencies below, that
is the whole install.

On a laptop nothing provides them, so `deploy/dev/` does — see
[`docs/local-testing.md`](../docs/local-testing.md).

```sh
kubectl apply -f deploy/ -f deploy/dev/
```

---

## What the platform provides

`deploy/` ships the gateway and nothing it depends on. Each dependency below
is named by a key in `configmap.yaml`, and pointing that key somewhere else is
the entire integration — no manifest in this directory changes.

| Dependency | Config key | Absent → |
|---|---|---|
| **Redis** — quota store for both rate currencies | `redis.addr` | limits go **unenforced**; requests still served |
| **OTLP collector** — metrics, traces, logs | `telemetry.otlp_endpoint` | no telemetry leaves the pod |
| **Provider keys** — one Secret, one data key per provider | `secret_source.providers[].path` | pod stays **not ready**; serves nothing |

The three failure columns are deliberately different, and the difference is
the design. Quotas fail **open** because an unenforced limit costs money and a
closed one costs availability. Credentials fail **closed** because a pod with
no key can only answer 401, and a pod that answers 401 should not be in the
Service's endpoints. That asymmetry is why quotas and keys never share a
store.

### Redis is external infrastructure

The gateway does not ship a Redis, and that is a decision rather than an
omission. The quota store holds fixed-window counters with a TTL measured in
seconds — disposable state that any managed Redis serves better than a pod
this repo would have to keep patched. Losing it costs one unenforced window,
which is exactly why running without one is survivable and why owning one
is not worth it.

Point `redis.addr` at what you already run:

```yaml
redis:
  addr: my-cache.abc123.ng.0001.euw1.cache.amazonaws.com:6379
```

Connections are pooled small on purpose (8, against a budget of ~3 operations
per request), so the instance can be the smallest one on offer.

When it is unreachable the gateway logs `rate limiter down, quota unenforced`
and `token limiter down, budget unenforced`, once per admitted request, and
keeps serving. Alert on those lines: they are the only signal that limits have
stopped meaning anything.

### Credentials are created out of band

`deploy/secret.yaml.example` is the template and the command. It is not a
`.yaml`, so the directory apply cannot sweep it up — a placeholder Secret
inside `deploy/` would overwrite a live key with `REPLACE_ME` on the next
`kubectl apply -f deploy/`.

---

## Before the first apply

- **`deployment.yaml` names `llm-gateway:dev` with `imagePullPolicy:
  IfNotPresent`.** That pairs with an image loaded straight into the node.
  Anywhere else, set a registry reference and a real tag.
- **`rbac.yaml` is cluster-scoped and must be.** The JWKS discovery endpoints
  are `nonResourceURLs`, which Kubernetes grants only through a
  ClusterRoleBinding; a namespaced RoleBinding against the same ClusterRole
  silently drops the rules and the gateway gets 403. It also outlives
  `kubectl delete namespace llm` — reset with `kubectl delete -f deploy/`.
- **`configmap.yaml` carries example apps and routes.** `apps` names the
  ServiceAccount subjects allowed to call, and `routes` the models served.
  Both are yours to replace; `callers.yaml` creates the SAs the example apps
  name.
- **`server.write_timeout` must exceed the largest app `total_deadline`**, or
  the server kills completions the core still considers in budget.
