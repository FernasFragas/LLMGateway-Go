#!/usr/bin/env bash
# End-to-end check of the whole request chain on a throwaway kind cluster:
#   ServiceAccount token → gateway auth → route → provider key
#   → openai adapter → stub provider → evals/run.py scoring
# The stub (evals/stub_provider.py) stands in for a model, so the check needs
# no real key, costs nothing, and gives the same result every run.
#
# CI runs it on every PR (.github/workflows/verify.yml). Locally: `make e2e`,
# with kubectl pointed at a kind cluster. It refuses to run anywhere else,
# because it creates and overwrites objects in the llm namespace.
set -euo pipefail

cd "$(dirname "$0")/../.."
KIND=${KIND:-kind}
PORT=${E2E_PORT:-18080}
REPORT=${E2E_REPORT:-evals/results/e2e.json}

ctx=$(kubectl config current-context)
case "$ctx" in
  kind-*) cluster=${ctx#kind-} ;;
  *) echo "refusing to run: kubectl context '$ctx' is not a kind cluster" >&2; exit 2 ;;
esac

pf_pid=""
work=$(mktemp -d)
cleanup() {
  if [[ -n $pf_pid ]]; then
    kill "$pf_pid" 2>/dev/null || true
    wait "$pf_pid" 2>/dev/null || true   # reap it quietly, no "Terminated" notice
  fi
  rm -rf "$work"
}
diagnose() {
  echo "::group::diagnostics"
  kubectl -n llm get pods -o wide || true
  kubectl -n llm logs deploy/llm-gateway --tail=40 || true
  kubectl -n llm logs deploy/stub-provider --tail=40 || true
  echo "::endgroup::"
}
trap cleanup EXIT
trap diagnose ERR

echo "== build the gateway image and load it into kind"
docker build -q -t llm-gateway:dev .
"$KIND" load docker-image llm-gateway:dev --name "$cluster"

echo "== namespace, identities, Redis"
kubectl apply -f deploy/00-namespace.yaml
kubectl apply -f deploy/serviceaccount.yaml -f deploy/callers.yaml -f deploy/rbac.yaml
kubectl apply -f deploy/dev/redis.yaml

echo "== a fake provider key, and the stub provider that expects it"
kubectl -n llm create secret generic llm-gateway-provider-keys \
  --from-literal=openai=e2e-stub-key --dry-run=client -o yaml | kubectl apply -f -
kubectl -n llm create configmap stub-provider \
  --from-file=stub_provider.py=evals/stub_provider.py --from-file=cases.json=evals/cases.json \
  --dry-run=client -o yaml | kubectl apply -f -
kubectl apply -f deploy/ci/stub-provider.yaml

echo "== gateway config, with this cluster's issuer"
issuer=$(kubectl get --raw /.well-known/openid-configuration \
  | python3 -c 'import json, sys; print(json.load(sys.stdin)["issuer"])')
sed "s#__ISSUER__#${issuer}#" deploy/ci/config.yaml > "$work/config.yaml"
kubectl -n llm create configmap llm-gateway-config \
  --from-file=config.yaml="$work/config.yaml" --dry-run=client -o yaml | kubectl apply -f -

echo "== the gateway"
kubectl apply -f deploy/deployment.yaml -f deploy/service.yaml
kubectl -n llm rollout restart deployment/llm-gateway   # pick up the image and config just loaded
kubectl -n llm rollout status deployment/redis --timeout=120s
kubectl -n llm rollout status deployment/stub-provider --timeout=120s
kubectl -n llm rollout status deployment/llm-gateway --timeout=180s

echo "== tunnel to the gateway on localhost:$PORT"
kubectl -n llm port-forward service/llm-gateway "$PORT:80" >/dev/null 2>&1 &
pf_pid=$!
for _ in $(seq 1 30); do
  curl -sf "localhost:$PORT/healthz" >/dev/null && break
  sleep 1
done

echo "== a token meant for another service is refused"
other=$(kubectl -n llm create token agent-service --audience=someone-else --duration=10m)
code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "localhost:$PORT/v1/chat" \
  -H "Authorization: Bearer $other" -H 'Content-Type: application/json' -d '{}')
if [[ $code != 401 ]]; then echo "wrong-audience token got HTTP $code, want 401" >&2; false; fi

echo "== evaluation (the runner also checks readiness and that no token gets 401)"
GATEWAY_TOKEN=$(kubectl -n llm create token agent-service --audience=llm-gateway --duration=10m) \
  python3 evals/run.py --base-url "http://127.0.0.1:$PORT" --model stub-model \
    --cases evals/cases.json --output "$REPORT"

echo "== passed"
