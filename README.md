# LLMGateway-Go

One front door for every LLM. A unified API over OpenAI / Anthropic / Ollama
with per-app failover policies, rate + slot limiting, and full cost visibility.

## Tech stack

![Go 1.26](https://img.shields.io/badge/Go_1.26-00ADD8?style=for-the-badge&logo=go&logoColor=white)
![Kubernetes](https://img.shields.io/badge/Kubernetes-326CE5?style=for-the-badge&logo=kubernetes&logoColor=white)
![Distroless](https://img.shields.io/badge/Distroless-2496ED?style=for-the-badge&logo=docker&logoColor=white)
![HashiCorp Vault](https://img.shields.io/badge/Vault-FFEC6E?style=for-the-badge&logo=vault&logoColor=black)
![Redis](https://img.shields.io/badge/Redis-FF4438?style=for-the-badge&logo=redis&logoColor=white)
![OpenTelemetry](https://img.shields.io/badge/OpenTelemetry-425CC7?style=for-the-badge&logo=opentelemetry&logoColor=white)
![Prometheus](https://img.shields.io/badge/Prometheus-E6522C?style=for-the-badge&logo=prometheus&logoColor=white)
![OpenAPI](https://img.shields.io/badge/OpenAPI-6BA539?style=for-the-badge&logo=openapiinitiative&logoColor=white)
![YAML](https://img.shields.io/badge/yaml.v3-CB171E?style=for-the-badge&logo=yaml&logoColor=white)


```mermaid
graph LR
    RAG[RAG API] -->|"HTTP/JSON + bound SA token"| GW
    AGENT[Agent service] -->|"HTTP/JSON + bound SA token"| GW

    GW["LLMGateway-Go<br/>auth (JWKS cache), rate limiting,<br/>routing, fallback, observability"]

    subgraph PROVIDERS["LLM Providers - failover order (per app policy)"]
        P1["1. OpenAI-compatible API"]
        P2["2. Anthropic-compatible API"]
        P3["3. Ollama (local / dev)"]
    end

    GW -->|HTTPS| P1
    GW -->|HTTPS| P2
    GW -->|"HTTP, local"| P3

    GW -->|"OTLP metrics + traces + logs"| OTEL[OTel Collector]
    OTEL -->|"forwards metrics"| PROM[Prometheus]
    GW -.->|"quotas + breaker state (fail open)"| REDIS[("Redis")]
    GW -.->|"signing keys: startup load + TTL refresh (fail static)"| JWKS[("kube-apiserver JWKS")]
    GW -.->|"provider keys: per-provider TTL + refresh on upstream 401 (fail static)"| SECRETS[("Secret source<br/>Vault / files")]
```

## What it solves

Every app that talks to an LLM rebuilds the same plumbing: one integration per
provider, API keys copy-pasted around, no shared limits, and no idea what
anything costs — until a provider has a bad day and takes the app down with it.

The gateway makes that one service's problem. Apps speak a single
OpenAI-compatible contract; the gateway translates per provider, holds every
credential, meters each app, and fails over under rules the app itself
declared. Adding a provider or swapping a model is a config change, not a
release.

## Why it's interesting

- **Failover is a contract, not a fallback.** Apps declare `same-model` /
  `allowlist` / `any`. The gateway never decides two models are
  interchangeable on an app's behalf, and the response always names the model
  that actually answered.
- **Keys and quotas deliberately don't share a store.** Auth fails *static*,
  rate limiting fails *open* — put them behind one Redis and a single outage
  triggers both policies, where fail-closed wins and the asymmetry becomes
  unreachable.
- **Slots are a currency, like rps and tokens.** A 2 rps agent holding 120 s
  calls occupies more concurrency than a 40 rps API. Meter only rates, and the
  slow app quietly starves the fast one.
- **Nothing fails invisibly.** Every degradation has a gauge, a log line, or a
  probe attached — serving a stale credential is only defensible because the
  staleness is measurable.
- **Every acceptance criterion is a runnable test.** `make verify`.

## Design - Hexagonal architecture.

`internal/gateway` holds the policy — admit, route, fail over — in domain
vocabulary, and declares the interfaces it needs. Everything technological
implements one of them from the outside, and every import points inward, so
swapping a provider, a transport, or the limiter never reaches the logic.
Cross-cutting concerns are decorators: same interface in, same interface out,
composed in `main` — which is why neither the core nor the adapters contain a
line of logging or metrics code.

```mermaid
graph LR
    subgraph DRIVING["Driving adapter"]
        API["internal/api<br/>HTTP · OpenAPI · middleware"]
    end

    subgraph CORE["internal/gateway — pure policy"]
        SVC["Service<br/>admit → route → fail over<br/><i>no HTTP, no SDKs, no Redis</i>"]
        P1(["AppDirectory"])
        P2(["RateLimiter"])
        P3(["SlotLimiter"])
        P4(["ProviderClient"])
        P5(["UsageRecorder"])
    end

    subgraph DRIVEN["Driven adapters"]
        AUTH["internal/auth<br/>SA token + JWKS cache"]
        RL["Redis limiter"]
        SL["in-process semaphore"]
        PROV["internal/openai<br/>internal/anthropic<br/>internal/ollama"]
        UR["usage recorder"]
    end

    API -->|"implements ChatService against"| SVC
    SVC --- P1 & P2 & P3 & P4 & P5
    AUTH -->|implements| P1
    RL -->|implements| P2
    SL -->|implements| P3
    PROV -->|implements| P4
    UR -->|implements| P5
```

Every arrow points *into* `internal/gateway`. Adapters import the core; the
core imports none of them, and cannot — the interfaces live in the package
that consumes them.

```mermaid
graph LR
    REQ([HTTP request]) --> MW["middleware chain<br/>RequestLogger → RequestMetrics → PanicLogger"]
    MW --> CS["logs/api.ChatService<br/><i>decorator</i>"]
    CS --> SVC2["gateway.Service<br/><i>core — never decorated</i>"]
    SVC2 --> DEC["logs/gateway<br/>one decorator per port"]
    DEC --> ADP["the real adapter<br/>auth · providers · limiters"]
```

The reasoning behind the failure modes, the capacity math, and the decisions
they commit to lives in
[`docs/design/architecture-brief.md`](docs/design/architecture-brief.md).
