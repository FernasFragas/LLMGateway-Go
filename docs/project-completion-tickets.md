# GW-100 — Conclude LLMGateway-Go: close every gap between "compiles and passes" and "runs complete in a cluster"

**Why:**
The gateway's core is finished — auth, routing, failover, limiters, refresh loops, and the decorator observability architecture are implemented and tested. What remains is the last mile: metrics that are promised but not exported, one config field that is loaded but never enforced, deploy manifests that don't yet deliver the provider keys the code expects, two design decisions that were deferred, and documentation that has drifted behind the code. Closing these makes the project honestly presentable as a finished portfolio piece.

**What:**
Parent ticket tracking all remaining work. Each subticket below is self-contained and independently mergeable; the Dependencies section of each says what must land first. ADR subtickets exist for the decisions that are still open — per house rule, the ADR is written and Accepted before the Go side is implemented.

**How:**
Work the ADRs first (GW-101, GW-104), since two implementation tickets are blocked on them. Everything else can proceed in parallel. Delete `TODO.md` when GW-110 lands — this file replaces it.

**Additional Context:**
Repo state as of 2026-08-02: quality debt from the refresher review is already fixed (package rename to `providerkeys`, error-prefix unification, `var _` sweep, dead `now` field). Streaming and the JOSE `crit` header are explicitly out of scope — `openapi.yaml` declares `stream` a rejected non-goal, and `crit` is a spec-conformance gap Kubernetes tokens never exercise.

**Dependencies:**
None — this is the root.

---

## GW-101 — ADR-002: Metrics exposition format (Prometheus vs OTLP)

**Why:**
`api.Deps.Metrics` is wired as `nil` in main and `GET /metrics` 404s. The in-memory counters (`RequestMetrics`, `PanicCounter`, the `metrics/gateway` and `metrics/providerkeys` decorators) exist and are tested, but nothing exports them. Before an exporter can be built, the format is a real decision: Prometheus text exposition (pull, ubiquitous, zero extra infra) vs OTLP push (vendor-neutral, needs a collector). This is preference-shaped and deserves a record, not a silent pick.

**What:**
Write `docs/ADRs/ADR-002-metrics-exposition.md` following `ADR-STRUCTURE.md`: open as Proposed, weigh Prometheus pull vs OTLP push (and the hybrid: Prometheus now, OTLP bridge later), decide, Accept.

**How:**
Start from the constraint already in the code: the counters are plain atomics behind accessor methods (`Attempts()`, `Failures()`, `FailuresByKind(kind)`), deliberately not tied to any metrics SDK. The ADR should decide whether that stays (hand-rolled `/metrics` text writer), or whether the counters get re-backed by a client library. Recommendation to evaluate first: Prometheus text format with a small hand-rolled handler — no new dependency, matches the "adapters at the edge" architecture, and the counters' accessors are already exactly the read surface a scrape needs.

**Additional Context:**
Main's comment at the `newServer` call documents the current state: "the OTLP/Prometheus provider and the /metrics handler are not built yet." The `deployment.yaml` has no scrape annotations yet either — whichever way the ADR goes, GW-102 picks that up.

**Dependencies:**
None. Blocks GW-102 and GW-103.

---

## GW-102 — Implement `/metrics` and wire the existing metrics decorators in main

**Why:**
Observability is half-delivered: every metrics decorator exists and is tested, but none is composed in `run()`, so a running gateway exposes no numbers at all. The logging half of every seam is wired; the counting half is dark.

**What:**
Build the exporter decided in ADR-002 as an adapter (own package, e.g. `internal/metrics/<format>`), pass its handler as `api.Deps.Metrics`, and wire the dormant decorators in main: `metrics/gateway.*` around the gateway ports (inside the existing `gwlogs` wrappers — logging outermost, per the `newServer` comment), and `metrics/providerkeys.NewRefresher` into the `RefreshVia` chain alongside `keyslogs.NewRefresher`.

**How:**
In `newProviderClient`, the seam is one line: `keys.RefreshVia(keyslogs.NewRefresher(keysmetrics.NewRefresher(keys), log))` — metrics innermost so the log line and the counter observe the same error. For the gateway ports, mirror how `gwlogs` wraps each dep in `gateway.New(...)`. The exporter handler then needs read access to every counter instance main created — main constructs them, so main hands them to the exporter; no globals.

**Additional Context:**
`internal/api/server.go:178` already routes `GET /metrics` when `Deps.Metrics` is non-nil — the edge is ready, only the adapter and wiring are missing. Update `deployment.yaml` with scrape annotations (or the collector sidecar) per the ADR.

**Dependencies:**
GW-101 (ADR-002 Accepted). Internal only.

---

## GW-103 — Record the promised provider-keys metrics: staleness gauge and trigger counter

**Why:**
`internal/providerkeys/doc.go` makes two observability promises the tree doesn't keep: "fail static is only defensible because the staleness is visible" (a gauge reading `Cache.Age`) and "it reports whether a refresh was actually started, which is what the counter records" (`TriggerRefresh`'s bool). Neither is recorded anywhere. Fail-static without visible staleness is the exact silent degradation the design forbids.

**What:**
A staleness gauge per provider, sampled from `Cache.Age(provider)` at scrape time (it's already a point-in-time read — no background sampler needed), and a counter for started out-of-band refreshes, incremented where main's router callback already receives `TriggerRefresh`'s bool.

**How:**
The gauge belongs in the exporter's collect path: iterate `keys.Providers()`, call `Age`, emit `provider_key_age_seconds{provider=...}` (and skip providers that never loaded — `Age`'s second return). The trigger counter can live in `metrics/providerkeys` next to `Refresher` and be bumped in the existing callback closure in `newProviderClient`. Keep both out of the cache itself — the observability-decorator rule applies.

**Additional Context:**
`Cache.Age` is already pinned by `TestAgeIsUnsetUntilTheFirstLoad`; this ticket only adds the recording, no cache changes.

**Dependencies:**
GW-102 (needs the exporter and wiring in place). Internal only.

---

## GW-104 — ADR-003: `tokens_per_minute` — enforce it or delete it

**Why:**
`config.AppLimits.TokensPerMinute` is parsed (`config.go:47,174`) and enforced nowhere — `newLimiters` builds only the RPS and max-in-flight currencies. A config key that operators can set and that silently does nothing is worse than either alternative. This is a real design decision: token budgets can only be settled *after* a completion returns (usage isn't known up front), so enforcement means a debit-after / reject-before model with different semantics than the RPS limiter — or the honest admission that two currencies are enough for this gateway.

**What:**
Write `docs/ADRs/ADR-003-token-budget-limiting.md`: enforce (design the debit-after model against the Redis limiter, including what happens on limiter outage — presumably fail-open like RPS) or remove the field (strict config loader means removal is a clean break: configs carrying the key fail loudly at boot).

**How:**
Start from the architecture brief's budget assumptions (~3 Redis ops per request) — a token currency adds writes per completion. If enforcing: the natural seam is the existing `UsageRecorder` port (it already sees per-completion usage) feeding the same Redis client. If removing: delete the field from `AppLimits` (both structs), `config.example.yaml`, and add a loader test proving an old config with the key is rejected with a clear message.

**Additional Context:**
`TODO.md` §8 settled zero-semantics for the *other* two fields but never resolved this one. Whichever way this goes, GW-105 implements it.

**Dependencies:**
None. Blocks GW-105.

---

## GW-105 — Implement ADR-003's outcome (token limiter or field removal)

**Why:**
Closes the gap between config surface and behavior — after this, every key in `config.example.yaml` does what it says.

**What:**
Either the token-budget limiter (Redis-backed, fail-open, zero-means-unmetered like the other currencies) or the field deletion with a loader rejection test, per ADR-003.

**How:**
If enforcing: extend `internal/redis` with the token currency, wire in `newLimiters`, decorate with logs (and metrics per GW-102's pattern), spec-style tests mirroring the existing limiter tests. If removing: mechanical — struct fields, example config, docs, one new `config_test.go` case.

**Additional Context:**
Zero-semantics precedent: `GlobalMaxInFlight` and the per-app limits treat 0 as unmetered; a token limiter must follow the same convention (see GW-107).

**Dependencies:**
GW-104 (ADR-003 Accepted). Internal only.

---

## GW-106 — Cap the JWKS response body

**Why:**
`JWKSCache.Refresh` (`internal/auth/jwks.go`) decodes the response body with no size bound. Every other outbound read in the tree (`ollama`, `openai`, `anthropic`, `providerkeys/vault.go`) caps at `10 << 20`; the JWKS fetch is the one remaining unbounded read. A typo'd `jwks_url` pointing at the wrong service could stream an unbounded body into the process. This is TODO.md §6, still open.

**What:**
`io.LimitReader` around the response body in `Refresh`, with a `maxJWKSBytes = 1 << 20` constant — a cluster's keyset is a few KB, so 1 MiB is generous headroom.

**How:**
One line plus the constant: `json.NewDecoder(io.LimitReader(resp.Body, maxJWKSBytes)).Decode(&wire)`. Keep the decoder streaming — don't switch to `ReadAll` (that shape exists in ollama.go only because it also needs raw bytes for error messages; `Refresh` doesn't).

**Additional Context:**
Test in the existing `jwks_test.go` style: serve a document padded past the cap, assert `Refresh` errors, and — reusing the fail-static pattern already in that file — that a warm cache keeps its old keys.

**Dependencies:**
None. Fully independent; good first ticket.

---

## GW-107 — Document zero-limits semantics where operators read

**Why:**
Main's `newLimiters` comment states "zero in any limit means that currency is unmetered," and the limiters behave that way — but the places an operator actually reads say nothing: no doc comment on `config.AppLimits`, nothing in `config/doc.go`, nothing in `config.example.yaml`. An undocumented convention is one refactor away from being violated. This is the unfinished half of TODO.md §8.

**What:**
The zero-means-unmetered sentence in three places: the `AppLimits` doc comment, `config/doc.go`, and a comment in `config.example.yaml` — plus a `config_test.go` case proving an app with an omitted `limits:` block loads without error and gains no implicit ceiling.

**How:**
Suggested wording (from TODO §8): "Zero in any field means that currency is unlimited for this app — a caller pays for what it configures, not for what it omits, matching `GlobalMaxInFlight`'s own zero-means-unenforced convention."

**Additional Context:**
Coordinate wording with GW-105 if the token field survives — the sentence must cover all remaining currencies.

**Dependencies:**
Soft dependency on GW-104 (so the documented field list is final). Internal only.

---

## GW-108 — Deliver provider keys to the pod: Secret manifest + volume mount

**Why:**
`deploy/configmap.yaml` configures file sources at `/etc/gateway/keys/{openai,anthropic}`, but `deployment.yaml` mounts only the ConfigMap and no Secret manifest exists — so in the cluster those paths don't exist, every refresh fails, and readiness gates the pod forever. The deploy directory currently declares a world the config contradicts.

**What:**
A `deploy/secret.yaml` (template with placeholder values and a comment pointing at `kubectl create secret generic` — never real keys in git; `.gitignore`'s `/secrets/` rule covers local files) and a second volume + mount in `deployment.yaml` at `/etc/gateway/keys`, `readOnly: true`.

**How:**
Nested mounts are legal: keep the ConfigMap at `/etc/gateway` and mount the Secret at `/etc/gateway/keys` — the kubelet layers them. One data key per provider (`openai`, `anthropic`) matching the configured filenames. Verify with the full loop: apply, watch `provider-keys` readiness go green, then `kubectl create secret ... --dry-run=client -o yaml | kubectl apply -f -` with a rotated value and confirm the rotation lands within the refresh interval via the decorator's log line.

**Additional Context:**
The kubelet updates mounted Secret files in place — the same mechanism the `File` fetcher's doc comment relies on. This ticket is what makes ADR-001's file path real in a cluster.

**Dependencies:**
None to write; GW-109's cluster session is the natural place to verify it. Internal only.

---

## GW-109 — In-cluster verification of the refresher architecture

**Why:**
The `KeepFresh`/`RefreshVia` refactor, the corrected `/var/run/secrets/...` ServiceAccount paths, and the `providerkeys` rename have never run in a pod. The path constants in particular were broken by a rename once already — the cluster is the only place that proves they're right now.

**What:**
A full deploy loop on the local cluster: rebuild the image, load it into the node, roll out, and watch (a) `jwks` and `provider-keys` readiness both go green, (b) a provider-key rotation arrive without restart, (c) a forced refresh failure produce exactly the decorator's log line and nothing else.

**How:**
Docker Desktop's Kubernetes node has a separate image store — `docker save llm-gateway:dev | docker exec -i <node> ctr -n k8s.io images import -` after every rebuild, or pods run stale bits. Then `kubectl rollout restart` and `kubectl logs -f`. Failure drill: scale the secret away (or point one source at a missing file) and confirm the pod stays ready, serves the cached key, and logs `provider key refresh failed` per interval.

**Additional Context:**
`docs/pod-logs-and-rollout.md` and `docs/local-testing.md` document the loop from previous sessions.

**Dependencies:**
GW-108 (keys must be mountable for the rotation check). Internal only.

---

## GW-110 — Redis for rate limiting: deploy it or document it away

**Why:**
Main dials a Redis quota store and fails open when it's absent — which means every in-cluster deploy today silently runs with rate limiting disabled. That's the designed degradation mode, not the designed steady state; "done" means limits actually enforce.

**What:**
Either a minimal `deploy/redis.yaml` (single-replica Deployment + Service — quota state is deliberately lossy, fail-open tolerates restarts, so no StatefulSet ceremony needed) or an explicit decision that Redis is external infrastructure, recorded in the deploy README/configmap comment with the address key operators must set. Lean toward the in-cluster manifest: the repo's promise is "apply `deploy/` and it works."

**How:**
Deployment + ClusterIP service in namespace `llm`, `redis:7-alpine`, resource requests, no persistence. Point `redis.addr` in `configmap.yaml` at the service DNS name. Verify: exceed an app's `rps` with minted-token curls and watch 429s; kill the Redis pod and confirm requests keep flowing (fail-open) with the limiter decorator's log line.

**Additional Context:**
Pool size is pinned small on purpose (`redisPoolSize = 8`, budgeted ~3 ops/request per the brief) — the manifest's resource requests can be correspondingly tiny.

**Dependencies:**
None. Internal only.

---

## GW-111 — Sync the architecture brief and finish the design doc sections

**Why:**
House rule: the brief is the source, and design work traces back to it — when it's stale, fix the brief before anything downstream. It currently predates the refresher/loop-ownership design (`KeepFresh`, `RefreshVia`, ports-live-with-their-caller), the `secrets` → `providerkeys` rename, and per `docs/design/notes.txt` the design doc still owes its Contract-guarantees rewrite (promises, not formats) and the Failure-modes section (derived from a request trace, not brainstormed).

**What:**
Update `docs/design/architecture-brief.md` to match the shipped architecture; complete the two open design-doc sections following the method already laid out in `notes.txt` (steps 4 and 5).

**How:**
For failure modes: trace one request caller→provider and back, list every dependency hop (JWKS, Redis, slots, secret store, the provider), ask down/slow/garbage for each, and for the survivors document detect/react/what-the-caller-sees. Close with the deliberate non-recoveries (no retry on 4xx, no eviction on 401) and why.

**Additional Context:**
Each notes.txt step has a pass test ("for each promise, name a code change that would break it") — apply them before calling a section done.

**Dependencies:**
Best done after GW-101/GW-104 so the brief records decisions, not open questions. Internal only.

---

## GW-112 — Replace TODO.md and give the README its final pass

**Why:**
`TODO.md` claims `main()` is empty and the provider adapters don't exist — it's actively misleading about a codebase that is nearly done. And the README must read as finished (present tense, no ADR links, no status caveats) once the functional gaps close.

**What:**
Delete `TODO.md` (this ticket file is its replacement); read the README top to bottom against the final tree and fix drift — the metrics endpoint, the `providerkeys` naming, config keys per ADR-003's outcome, and the deploy story including Redis and the keys Secret.

**How:**
Do it last. The README check is mechanical: every claim gets verified against the tree, every example config line against `config.example.yaml`, every curl against a running pod from GW-109's session.

**Additional Context:**
House rule for public-facing copy: it describes what is, never what's planned.

**Dependencies:**
All other tickets — this is the closer.
