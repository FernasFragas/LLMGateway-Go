# Runbook: gateway pods stuck at 0/1

**Symptom:** one of these.

```
error: deployment "llm-gateway" exceeded its progress deadline
```

`kubectl -n llm get pods` shows `llm-gateway-…   0/1   Running`, and it
stays that way.

**What it means:** the pods are running, but they refuse readiness, so they
get no traffic. The gateway **fails closed**: it won't serve until it has
everything it needs. The log always says what's missing.

---

## 1. Check you're on the right cluster

```sh
kubectl config current-context        # expect docker-desktop (or your cloud cluster)
```

## 2. Read the reason

```sh
kubectl -n llm logs deploy/llm-gateway | grep -E "readiness refused|initial|failed" | tail -5
```

## 3. Match it and fix it

| The log says | Cause | Fix |
|---|---|---|
| `provider-keys: … no configured source has loaded yet` **and** `read /etc/gateway/keys/<name>: no such file or directory` | The config expects a key named `<name>`, but the Secret has no entry with that name | [Fix A](#fix-a-key-name-mismatch) |
| `provider-keys: … no configured source has loaded yet`, with no "no such file" line | The key Secret doesn't exist at all | Load it: [Fix A](#fix-a-key-name-mismatch), second command |
| `jwks: no service-account keys loaded yet` | The gateway can't fetch the cluster's token-signing keys | [Fix B](#fix-b-jwks-not-loaded) |
| No `readiness refused` lines, and the pod status is `CrashLoopBackOff` | The gateway stops at boot: bad config or wrong issuer | [Fix C](#fix-c-crashloopbackoff) |

---

### Fix A: key name mismatch

This happens when you switch provider in the config (e.g. `openai` →
`ollama`) but not in the Secret.

1. See which key names each side uses. This prints names only, never values:
   ```sh
   # what the config expects
   kubectl -n llm get configmap llm-gateway-config -o jsonpath='{.data.config\.yaml}' | sed -n '/^  providers:/,/^[a-z]/p'
   # what the Secret has
   kubectl -n llm get secret llm-gateway-provider-keys -o json | jq -r '.data | keys[]'
   ```
2. Reload the Secret with the name the config expects. Use `--context`, so it
   can't go to another cluster. This replaces the whole Secret, so list every
   provider the config needs:
   ```sh
   kubectl --context docker-desktop -n llm create secret generic llm-gateway-provider-keys \
     --from-file=ollama=secrets/ollama --dry-run=client -o yaml \
     | kubectl --context docker-desktop apply -f -
   ```
3. Restart. Otherwise the gateway waits for its next refresh, up to
   `refresh_interval` (5 min):
   ```sh
   kubectl -n llm rollout restart deployment/llm-gateway
   ```

### Fix B: JWKS not loaded

1. Check the RBAC binding exists. It's cluster-wide and easy to lose:
   ```sh
   kubectl get clusterrolebinding llm-gateway-issuer-discovery
   ```
2. If it prints `NotFound`, apply it and restart:
   ```sh
   kubectl apply -f deploy/rbac.yaml
   kubectl -n llm rollout restart deployment/llm-gateway
   ```

### Fix C: CrashLoopBackOff

1. Read the **previous** attempt's log. The first line names the problem:
   ```sh
   kubectl -n llm logs deploy/llm-gateway --previous | head -3
   ```
2. Fix it in your config file:
   - `configured issuer is not this cluster's … cluster=<url>` → put `<url>`
     in `auth.issuer`.
   - `line N: field … not found` → a misspelled or unknown key on line N.
3. Reload the ConfigMap and restart:
   ```sh
   kubectl --context docker-desktop -n llm create configmap llm-gateway-config \
     --from-file=config.yaml=config/single-provider.yaml --dry-run=client -o yaml \
     | kubectl --context docker-desktop apply -f -
   kubectl -n llm rollout restart deployment/llm-gateway
   ```

---

## 4. Check it's fixed

```sh
kubectl -n llm rollout status deployment/llm-gateway --timeout=180s
kubectl -n llm get pods -l app=llm-gateway        # both 1/1 Running
```

Old pods from failed rollouts disappear on their own once the new ones are
ready.

**Still stuck?** See the Troubleshooting table in
[`local-testing.md`](../local-testing.md#troubleshooting), or the incident
playbook in [`production-operations.md`](../production-operations.md) §10.

---

*First seen 2026-09-28: the config was switched to Ollama Cloud (key
`ollama`) while the Secret still held only `openai`. Fixed with Fix A.*
