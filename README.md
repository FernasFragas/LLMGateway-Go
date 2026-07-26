# LLMGateway-Go
One front door for every LLM. Unified API over OpenAI / Anthropic / Ollama
with per-app failover policies, rate + slot limiting, and full cost visibility.

```mermaid
graph LR
    RAG[RAG API] -->|"HTTP/JSON + bound SA token"| GW
    AGENT[Agent service] -->|"HTTP/JSON + bound SA token"| GW
    PROM[Prometheus] -->|scrapes /metrics| GW

    GW["LLMGateway-Go<br/>auth (JWKS cache), rate limiting,<br/>routing, fallback, observability"]

    subgraph PROVIDERS["LLM Providers - failover order (per app policy)"]
        P1["1. OpenAI-compatible API"]
        P2["2. Anthropic-compatible API"]
        P3["3. Ollama (local / dev)"]
    end

    GW -->|HTTPS| P1
    GW -->|HTTPS| P2
    GW -->|"HTTP, local"| P3

    GW -->|"OTLP traces + logs"| OTEL[OTel Collector]
    GW -.->|"quotas + breaker state (fail open)"| REDIS[("Redis")]
    GW -.->|"signing keys: startup load + TTL refresh (fail static)"| JWKS[("kube-apiserver JWKS")]
    GW -.->|"provider keys: startup load + TTL refresh (fail static)"| SECRETS[("Secret source")]
```

## Why it's interesting 
- Failover is a contract: apps declare same-model / allowlist / any —
  the gateway never silently swaps models.
- Keys and quotas deliberately don't share a store — one Redis outage
  can't fail-closed the whole gateway.
- Slots (concurrency), not just rps, are rate-limited — a 2 rps agent
  can't starve a 40 rps API. 
- Every acceptance criterion is a runnable test: `make verify`.

## Adding an app
1. Generate a key and store it in the secret source: `...`
2. Add the app block to the gateway config (see config.example.yaml) —
   choose the failover policy deliberately; default is `same-model`,
   which today means no failover at all.
3. Roll the gateway (config applies on restart).
4. Verify: a request with the new key returns 200 and
   `llm_tokens_total{app="<name>"}` increments.
