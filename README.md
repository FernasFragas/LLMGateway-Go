# LLMGateway-Go
One front door for every LLM. Unified API over OpenAI / Anthropic / Ollama
with per-app failover policies, rate + slot limiting, and full cost visibility.

![img.png](docs/design/context_diagram.png)

## Why it's interesting 
- Failover is a contract: apps declare same-model / allowlist / any —
  the gateway never silently swaps models.
- Keys and quotas deliberately don't share a store — one Redis outage
  can't fail-closed the whole gateway.
- Slots (concurrency), not just rps, are rate-limited — a 2 rps agent
  can't starve a 40 rps API. 
- Every acceptance criterion is a runnable test: `make verify`.
