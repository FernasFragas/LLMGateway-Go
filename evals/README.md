# Gateway evaluation

Requires Python 3.9+; no packages or model SDKs. Calls this gateway's `/v1/chat`
endpoint (not `/v1/chat/completions`). Use only after choosing a model and
provider budget: a run makes ten sequential completions, each capped at 128
output tokens by default. Input tokens and provider pricing also count toward
cost. No retries are performed.

Offline validation (no provider calls):

```sh
python3 -m unittest discover -s evals -v
go test ./internal/config
```

Against a running deployment, with a loopback port forward in another terminal:

```sh
kubectl -n llm port-forward service/llm-gateway 8080:80
```

Then run from the repo root with the exact configured route model:

```sh
export GATEWAY_TOKEN="$(kubectl -n llm create token agent-service --audience=llm-gateway --duration=10m)"
python3 evals/run.py --base-url http://127.0.0.1:8080 --model YOUR_CONFIGURED_MODEL --output evals/results/baseline.json
unset GATEWAY_TOKEN
```

The runner first requires readiness and a 401 for an unauthenticated request.
Each completion must return HTTP 200, the configured model, one completed
assistant answer, consistent nonnegative token counts, and satisfy its explicit
answer criterion within 30 seconds. The gateway reports the configured route
model, so an upstream provider's dated model identifier does not affect this
check. The single-provider config forbids substitutions. JSON criteria compare
structure and types, ignoring object key order. Exact text criteria trim only
leading/trailing whitespace.

Exit 0 means every case passed, 1 means at least one failed, and 2 means invalid
configuration or a failed preflight. Reports record cases, latency, token usage,
UTC completion time, target origin, suite hash and thresholds. They deliberately
omit prompts, responses and bearer tokens; `evals/results/` is gitignored. Use a unique output
filename to retain each baseline. A connection error, malformed JSON, truncated
completion, HTTP error or missing usage cannot silently pass. HTTP is accepted
only on loopback, and redirects are refused to avoid forwarding credentials.

These ten synthetic cases are a deployment smoke evaluation, not evidence of
specialized model quality. Replace or extend them with representative examples
before using the report to make model decisions. No judge LLM or paid service is
required by the harness itself; a live provider can charge for completions.
