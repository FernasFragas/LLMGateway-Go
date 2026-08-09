# LLMGateway-Go — API Requests

> **Scope of v0.1.0:** one data-path operation (chat completion, OpenAI-compatible subset) and two Kubernetes probes. Tool calling is in scope; streaming, `n > 1`, multimodal content blocks, embeddings, and per-request policy overrides are out — refused with a named parameter, never silently ignored. All traffic is in-cluster; there is no public route and no CORS.

## 1. Overview

| Flow | Endpoint |
|---|---|
| Calling app → Gateway — Chat completion | `POST http://llm-gateway.llm.svc.cluster.local/v1/chat` |
| Kubelet → Gateway — Liveness | `GET http://<pod-ip>:8080/healthz` |
| Kubelet → Gateway — Readiness | `GET http://<pod-ip>:8080/readyz` |
| Gateway → Provider — Completion attempt (**not** this contract) | `POST https://api.openai.com/v1/…`, `https://api.anthropic.com/v1/…`, `http://ollama.llm.svc.cluster.local:11434/…` |

Calling apps (`rag-api`, `agent-service`, `support-bot`) hold no provider key, SDK, or hostname; they know one endpoint and one body shape. The gateway owns all business logic on the path — identity, the three metered currencies, route selection, failover, and usage accounting — and providers own only the completion itself.

The fourth row is listed to be explicit that it is **not a proxied hop sharing this contract**. The gateway translates into each vendor's dialect and authenticates with its own credential; no field, header, or error from this document survives into the upstream call, and none comes back. That translation is the product, not an implementation detail — which is why the upstream contract is deliberately absent here.

> **The disclosure rule.** A 200 never confirms that the *requested* model answered. `model` in the response body always names what actually served, and `X-Model-Substituted: true` marks the cases where that differs from the request. A caller that echoes its own request model to a user is wrong; read it from the response. This is the one convention every consumer must follow.

> **Two 429s that mean different things.** Rate quota, token budget, and slot ceiling all refuse with 429. `error.code` separates the ceiling (`concurrency_ceiling`) from the rate currencies (`quota_exceeded`), and within `quota_exceeded`, `details.quota.window_seconds` separates requests (`1`) from tokens (`60`). Branch on those, not on the status.

## 2. Authentication

### 2.1 Calling app → Gateway

```
Authorization: Bearer <projected ServiceAccount token>
Content-Type: application/json
Accept: application/json
X-Correlation-ID: <opaque string, ≤128 chars>   # optional
```

The token is the app's own bound Kubernetes ServiceAccount token, projected into its pod with `audience: llm-gateway`. Apps mint nothing and share nothing: identity is the token the platform already gives them, and the gateway maps its `sub` (`system:serviceaccount:llm:<app>`) to the app's config block — which is where its limits and failover policy live.

Verification is local, against a cached copy of the cluster's JWKS. There is no TokenReview call on the request path, so a slow apiserver costs latency to no one.

Failures are always **401 `unauthorized`**, with one message, for every cause: header missing, scheme not `Bearer`, signature invalid, expired, wrong audience, wrong issuer, or signed by an unknown key. The gateway does not distinguish them to the caller — a probe that reports *why* a token failed is a probe for forging one. The specific reason reaches the gateway's own logs.

**A 401 from this API is always about your token.** It is never a relayed provider credential failure; see §6.

### 2.2 Gateway → Provider

```
Authorization: Bearer <provider key>      # OpenAI, Ollama (when configured)
x-api-key: <provider key>                 # Anthropic
anthropic-version: <pinned version>       # Anthropic
Content-Type: application/json
```

Provider credentials are read from the gateway's secret source (mounted files or Vault), cached in memory per provider with its own refresh cadence, and never logged. They authenticate the *gateway*, never a caller — the two credential systems share no cache, no store, and no failure domain by design.

**The correlation ID does not propagate upstream.** It joins the caller's log line to the gateway's, and stops there; providers are given no tracing header. A correlation ID is therefore sufficient to trace a request through the gateway and useless for asking a provider about it.

## 3. Chat completion

**Endpoint (calling app → Gateway):** `POST /v1/chat`

No second hop shares this contract — see §1.

### 3.1 Request payload

```json
{
    "model": "gpt-4.1",
    "max_tokens": 512,
    "messages": [
        { "role": "system", "content": "You answer in one sentence." },
        { "role": "user", "content": "Why do slot ceilings matter?" }
    ],
    "temperature": 0.7,
    "top_p": 1,
    "stop": ["\n\n"]
}
```

The calling app is identified **only** by the token — never by a body field, a path segment, or a header. There is no `app`, `tenant`, or `user` parameter, and adding one would create a second, forgeable identity; attribution, limits, and policy all derive from `sub`.

The decoder rejects unknown fields rather than dropping them. A parameter the gateway does not implement is a semantic difference the caller is entitled to hear about, so it earns a 400 naming the parameter instead of a silently different completion.

### 3.2 Response — 200, served by the requested model

```
HTTP/1.1 200 OK
Content-Type: application/json
X-Correlation-ID: 9f8b7a6c5d4e3f2a1b0c9d8e7f6a5b4c
```

```json
{
    "id": "chatcmpl-3f9a2b1c",
    "object": "chat.completion",
    "created": 1786274522,
    "model": "gpt-4.1",
    "choices": [
        {
            "index": 0,
            "message": { "role": "assistant", "content": "Because a few long requests can hold more capacity than many short ones." },
            "finish_reason": "stop"
        }
    ],
    "usage": { "prompt_tokens": 21, "completion_tokens": 16, "total_tokens": 37 }
}
```

### 3.3 Response — 200, served by a substitute model

The requested model's providers all failed and the app's `failover_policy` permitted a different model. Note `X-Model-Substituted` **and** the changed `model`; the two always agree.

```
HTTP/1.1 200 OK
X-Model-Substituted: true
X-Correlation-ID: 4c1f9e0a77aa4b2e9d1c0f8a6c2d5e3b
```

```json
{
    "id": "chatcmpl-8d2e4f60",
    "object": "chat.completion",
    "created": 1786274531,
    "model": "claude-sonnet-4",
    "choices": [
        {
            "index": 0,
            "message": { "role": "assistant", "content": "Because a few long requests can hold more capacity than many short ones." },
            "finish_reason": "stop"
        }
    ],
    "usage": { "prompt_tokens": 21, "completion_tokens": 16, "total_tokens": 37 }
}
```

The header is absent — not `false` — when no substitution occurred. Route failover between two hosts serving the *same* model is not a substitution and sets no header: nothing about the answer changed.

### 3.4 Response — 200, the model asked for a tool

`finish_reason` is `tool_calls`, `content` is `null`, and the caller is expected to execute the tools and send a follow-up request. Requires `tools` in the request.

```json
{
    "id": "chatcmpl-b71c0a93",
    "object": "chat.completion",
    "created": 1786274544,
    "model": "gpt-4.1",
    "choices": [
        {
            "index": 0,
            "message": {
                "role": "assistant",
                "content": null,
                "tool_calls": [
                    {
                        "id": "call_a1b2c3",
                        "type": "function",
                        "function": { "name": "get_weather", "arguments": "{\"city\":\"Lisbon\"}" }
                    }
                ]
            },
            "finish_reason": "tool_calls"
        }
    ],
    "usage": { "prompt_tokens": 84, "completion_tokens": 19, "total_tokens": 103 }
}
```

The follow-up sends the assistant message back verbatim, then one `role: "tool"` message per call, each carrying the matching `tool_call_id`:

```json
{
    "model": "gpt-4.1",
    "max_tokens": 512,
    "messages": [
        { "role": "user", "content": "Weather in Lisbon?" },
        { "role": "assistant", "content": null, "tool_calls": [ { "id": "call_a1b2c3", "type": "function", "function": { "name": "get_weather", "arguments": "{\"city\":\"Lisbon\"}" } } ] },
        { "role": "tool", "tool_call_id": "call_a1b2c3", "content": "{\"temp_c\":19}" }
    ]
}
```

> **Note:** the gateway holds no conversation state. Every request carries its whole history, and a follow-up is an independent request that re-runs auth, limits, routing, and failover — so it may be served by a *different* provider than the turn that requested the tool call. Apps needing tool-call fidelity across turns declare `failover_policy: same-model`.

### 3.5 Response — 429, a currency ran out

Same status, same code for both rate currencies; `window_seconds` is the discriminator. `Retry-After` is a whole number of seconds, always rounded **up** — a retry a moment late is honest, a moment early is refused again.

Request rate (`rps`):

```
HTTP/1.1 429 Too Many Requests
Retry-After: 1
```

```json
{
    "error": {
        "code": "quota_exceeded",
        "message": "support-bot is over its rate quota",
        "correlation_id": "1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d",
        "details": {
            "retry_after_seconds": 1,
            "quota": { "limit": 10, "window_seconds": 1, "used": 10 }
        }
    }
}
```

Token budget (`tokens_per_minute`) — identical shape, `window_seconds: 60`:

```json
{
    "error": {
        "code": "quota_exceeded",
        "message": "support-bot is over its token budget",
        "correlation_id": "7d6c5b4a39281706f5e4d3c2b1a09876",
        "details": {
            "retry_after_seconds": 12,
            "quota": { "limit": 50000, "window_seconds": 60, "used": 50000 }
        }
    }
}
```

Slot ceiling — a **different** code, because the remedy differs: the app is holding too many requests open at once, and time alone does not fix it.

```json
{
    "error": {
        "code": "concurrency_ceiling",
        "message": "agent-service is at its in-flight ceiling (300)",
        "correlation_id": "0f1e2d3c4b5a69788796a5b4c3d2e1f0",
        "details": { "retry_after_seconds": 1, "max_in_flight": 300 }
    }
}
```

### 3.6 Response — 503, policy left no eligible route

The app's `failover_policy` refused every remaining provider. This is the declared fidelity-over-availability trade taking effect, not a gateway fault: an app that says `same-model` has asked to be refused rather than substituted.

```json
{
    "error": {
        "code": "model_unavailable",
        "message": "no eligible model provider for gpt-4.1 under the app's failover policy",
        "correlation_id": "5b6a79889706f5e4d3c2b1a012345678",
        "details": { "requested_model": "gpt-4.1" }
    }
}
```

The same code and status answer a `model` absent from the routes list — from the caller's side both mean "nothing here can serve this", and distinguishing them would leak the routing table.

### 3.7 Field notes — request

| Field | Notes |
|---|---|
| `model` | Required. Must appear in the gateway's routes list; otherwise 503 `model_unavailable`. Selects the model, never the provider — which provider serves it is the gateway's decision and may change between identical requests. |
| `messages` | Required, non-empty. `content` is required on every message except an `assistant` message carrying `tool_calls`, where it may be `null`. Content is a **string**: an array of content blocks is a type error (multimodal is out of scope). |
| `messages[].role` | One of `system`, `user`, `assistant`, `tool`. Anything else is 400 naming the index. |
| `messages[].tool_calls` | Allowed on `assistant` messages only. Echo back the object the gateway returned, unmodified. |
| `messages[].tool_call_id` | Required when `role` is `tool`; must match the `id` of the call being answered. |
| `max_tokens` | Required, ≥ 1. Doubles as the cost cap: it is the upper bound the gateway books against the app when a provider may have billed for tokens the gateway never received. An inflated `max_tokens` therefore inflates that estimate — send what you mean. |
| `temperature` | Optional, 0–2 inclusive. Out of range is 400. |
| `top_p` | Optional, 0–1 inclusive. Out of range is 400. |
| `stop` | Optional. A single string or an array of at most 4; more is 400. |
| `tools` | Optional. `type` must be `"function"`; `function.name` required. `function.parameters` is passed through as an opaque JSON Schema object. |
| `tool_choice` | Optional. `"none"`, `"auto"`, or `{"type":"function","function":{"name":"…"}}`. Any other string or a malformed object is 400 naming `tool_choice`. |
| `n` | Accepted only as `1`. Any other value is 400 `unsupported_parameter` — it is not clamped. |
| `stream` | `false` is accepted; `true` is 400 `unsupported_parameter` naming `stream`. Serving a streaming request unstreamed would be a silent contract break, so it is refused instead. |
| *anything else* | Unknown fields are rejected with 400 `unsupported_parameter`, never ignored. |

### 3.8 Field notes — response

| Field | Notes |
|---|---|
| `id` | Gateway-generated per response (`chatcmpl-` + random hex). It is **not** the provider's ID and is not stable across retries — correlate with `X-Correlation-ID`, not this. |
| `object` | Always `chat.completion`. |
| `created` | Unix seconds, stamped by the gateway when it encoded the response — not by the provider. |
| `model` | **The model that actually served.** Never an echo of the request. The one field a caller must read rather than assume. |
| `choices` | Always exactly one element (`n > 1` is refused), so `choices[0]` is safe. |
| `choices[].message.content` | `null` only when the assistant returned tool calls and no prose; a string otherwise. |
| `choices[].finish_reason` | `stop`, `length`, or `tool_calls`. `length` means `max_tokens` truncated the answer — the response is valid and incomplete. |
| `usage` | Tokens as the serving provider reported them. Absent from every non-200: a refused request spends nothing the caller can be shown. |
| `X-Correlation-ID` (header) | On **every** response including errors. Adopted from the request when supplied and ≤128 characters, otherwise generated — an over-long ID is replaced whole, never truncated, since a partial ID correlates with nothing. |
| `X-Model-Substituted` (header) | Present and `true` only when a model other than the requested one served. Absent otherwise — never `false`. |
| `Retry-After` (header) | On 429 only, whole seconds, rounded up. Mirrors `details.retry_after_seconds`. |

## 4. Liveness probe

**Endpoint (kubelet → Gateway):** `GET /healthz`

### 4.1 Request payload

None. No credential either — the kubelet sends none, and the probes are the only unauthenticated routes.

### 4.2 Response — 200, process healthy

```json
{ "status": "ok" }
```

### 4.3 Response — 503, process unhealthy

Empty body.

> **Note:** liveness is **unconditional on dependencies** and must stay that way. A failing liveness probe restarts the pod, so a JWKS outage, a dead secret source, or an unreachable Redis must never reach this endpoint — restarting a healthy process because something else is down converts one outage into two. Only true process health belongs here.

## 5. Readiness probe

**Endpoint (kubelet → Gateway):** `GET /readyz`

### 5.1 Request payload

None; unauthenticated.

### 5.2 Response — 200, pod may receive traffic

Empty body. Both gates are satisfied: the JWKS cache has loaded, and at least one route is actually servable.

### 5.3 Response — 503, pod refuses traffic

Empty body. The pod is removed from the Service's endpoints until it recovers; nothing else changes and the process is not restarted.

The reason is deliberately not in the body — it reaches the gateway's log instead, since the only consumer is a kubelet that branches on the status alone:

```
kubectl -n llm logs deploy/llm-gateway | grep "readiness refused"
```

> **Note:** this endpoint fails **closed**, and that asymmetry against `/healthz` is the design. A pod that cannot verify tokens would 401 every caller; a pod with no servable route would 502 them. Refusing traffic while cold harms nothing, because the Service simply routes to a warm replica. The gate is *at least one route servable* — never *the key cache is non-empty* — so a deployment whose routes need no credential at all is ready immediately, and one dead provider source degrades routing instead of grounding the pod.

## 6. Enums / Codes

### 6.1 Message role (request `messages[].role`)

| Label | Code |
|---|---|
| System instruction | `system` |
| End-user turn | `user` |
| Model turn | `assistant` |
| Tool result | `tool` |

### 6.2 Finish reason (response `choices[].finish_reason`)

| Label | Code | Consumer behavior |
|---|---|---|
| Completed naturally | `stop` | Render the content. |
| Truncated by `max_tokens` | `length` | Render, and treat as incomplete — raise `max_tokens` or continue the turn. Not an error. |
| Tool calls requested | `tool_calls` | `content` is `null`; execute the calls and send a follow-up (§3.4). |

### 6.3 Tool choice (request `tool_choice`)

| Label | Code |
|---|---|
| Never call a tool | `"none"` |
| Model decides | `"auto"` |
| Force one function | `{"type":"function","function":{"name":"…"}}` |

### 6.4 Failover policy (gateway config, not a request field)

Set per app in the gateway's config; it governs how §3.3 behaves for that caller. Listed here because it is the contract term that decides whether an outage reaches the caller as a substitute or as a 503.

| Label | Code | Effect on this API |
|---|---|---|
| No substitutes, ever | `same-model` | Route failover only. Provider outage surfaces as 503 `model_unavailable`. **Default for new apps.** |
| Named substitutes | `allowlist: [...]` | Substitutes only from the list, in order. 503 when the list is exhausted. |
| Any available model | `any` | Takes whatever the failover order offers; 502 only when everything failed. |

> **To confirm per app:** a policy change alters which of §3.3 and §3.6 a caller sees during an incident, so it is an app-owned decision recorded in config — the gateway never picks substitutes on an app's behalf.

## 7. Error Catalogue

Applies to `POST /v1/chat`. Every entry uses one body shape:

```json
{
    "error": {
        "code": "…",
        "message": "…",
        "correlation_id": "…",
        "details": { }
    }
}
```

`code` is the stable contract; `message` is human-readable and **may change without notice** — never branch on it. `details` is present only when a code carries extra facts, and `correlation_id` is always present.

| Status | Code | When | Consumer behavior |
|---|---|---|---|
| `400` | `invalid_request` | Malformed JSON, missing `model`/`messages`, `max_tokens` < 1, bad role, out-of-range `temperature`/`top_p`, > 4 stop sequences, content sent as an array | Fix and resend. Never retry unchanged — the result is identical. |
| `400` | `unsupported_parameter` | `stream: true`, `n` ≠ 1, or any unknown field. `details.param` names it | Remove the parameter. A bug in the caller, not a capacity problem. |
| `401` | `unauthorized` | Token missing, malformed, expired, wrong audience or issuer, or signed by an unknown key | Re-read the projected token from disk and retry once; if it persists, the SA or audience is misconfigured. **Never** a provider-credential problem. |
| `413` | `payload_too_large` | Body exceeded `max_body_bytes`; the limit is in the message | Shorten the prompt or history. Deterministic. |
| `415` | `unsupported_media_type` | `Content-Type` absent or not `application/json` (parameters like `charset` are fine) | Fix the header. |
| `429` | `quota_exceeded` | The app's rate quota (`window_seconds: 1`) or token budget (`window_seconds: 60`) is spent | Back off for `Retry-After` seconds, then retry. Only this app is affected; other apps are unimpaired. |
| `429` | `concurrency_ceiling` | The app is holding its maximum in-flight requests; `details.max_in_flight` is the ceiling | Reduce concurrency — retrying immediately at the same parallelism will refuse again. Requests are never queued inside the gateway. |
| `499` | *(no body)* | The caller disconnected mid-request | Nothing to handle; no one is listening. The upstream call was aborted to stop further token spend. |
| `500` | `internal_error` | A bug in the gateway (recovered panic) | Retry once; if it persists, report it with the correlation ID. No detail is exposed by design. |
| `502` | `upstream_failed` | Every policy-eligible provider attempt failed | Retry with the caller's own backoff. The gateway has already failed over and will not retry internally. |
| `503` | `model_unavailable` | Policy allowed no eligible route, or `model` is not in the routes list; `details.requested_model` echoes it | Do not retry in a tight loop — the answer will not change until the outage clears or config does. |
| `504` | `gateway_timeout` | The request's `total_deadline` expired, including failover time | The budget is exhausted, not necessarily the provider. Retry only if the caller has time left. |

Two error rules worth stating outright, because they are what a gateway is *for*:

- **A provider's error is never yours.** No upstream status, body, or message is ever forwarded. A provider's own 429 becomes your 502 or a successful failover — never your 429, because you did not exceed anything.
- **An upstream 401 is never your 401.** A provider refusing the *gateway's* credential surfaces as 502, or 503 under a conservative policy. If you receive 401, the problem is your ServiceAccount token, always.

## 8. Behavioral notes

**Model identity is a runtime fact, not a request parameter.** Any surface that shows users which model answered must read `model` from the response body. If an app renders the requested model instead, it will confidently attribute an answer to the wrong model during exactly the incident where that matters.

If substitution is disclosed to end users, the honest phrasing is what actually happened:

> Answered by *[response `model`]* — the requested model was unavailable.

**Latency expectations.** The gateway adds under 50 ms at p95 at design load; the rest is the provider. A failover doubles the wait in the worst case, bounded by `total_deadline`, and `agent-service`-style apps run to 150 s — set client timeouts from the configured deadline, not from a typical response.

**Backoff is the caller's job.** The gateway never queues and never retries the same provider. `Retry-After` on a 429 says when to return; honoring it is what keeps one app's burst from becoming everyone's incident.

**Forward compatibility.** These are already shaped so the next iteration is additive rather than breaking:

- `choices` is an array with exactly one element today; `n > 1` would populate it without changing the shape.
- `X-Model-Substituted` is absent-or-`true`, so a richer disclosure (which model was requested, why it changed) arrives as new fields under `details`, never as a changed meaning for this one.
- `error.details` is an open object — new codes carry new keys, and unknown keys must be ignored rather than treated as errors.
- `finish_reason` is an open enum. Treat an unrecognized value as `stop` and render what arrived.
- `stream` is refused rather than absent from the schema: when streaming lands it becomes a supported value of a field callers already know, and the refusal today prevents apps from being written against a silently non-streaming endpoint.
