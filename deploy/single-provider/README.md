# Single-provider deployment and evaluation

This recipe reuses the existing Kubernetes deployment, ServiceAccount auth,
Redis and OTLP setup. It does not create a public endpoint. Finish one checkbox
per work session. Commands below are operator steps, not an automatic deploy.

## Weekend 1: prepare and establish the baseline

- [ ] Choose a Kubernetes context, an accessible image registry/tag, one
  OpenAI-compatible provider endpoint and model, and a provider spending limit.
  This example uses the gateway's `openai` adapter; using Ollama or Anthropic
  requires the matching route and credential configuration instead.
- [ ] Run `python3 -m unittest discover -s evals -v` and `go test ./internal/config`.
- [ ] Run `kubectl config current-context` and confirm the intended cluster.
  Obtain its exact issuer with
  `kubectl get --raw /.well-known/openid-configuration`.
- [ ] Copy `deploy/single-provider/config.yaml.example` to the gitignored
  `config/config.yaml` (create `config/` first; preserve an existing local config).
  Set issuer, model and endpoint. Match Redis and OTLP to your existing services.
  The example's hostnames match `deploy/dev/redis.yaml` and
  `deploy/dev/otel-collector.yaml`. Redis failure makes quotas fail open, so
  verify Redis before relying on configured limits.
- [ ] Build and push a uniquely tagged image with the existing Dockerfile.
  Record the tag; avoid overwriting an existing release tag. Store the provider
  key in `secrets/openai` (the repository ignores `secrets/`).

## Weekend 2: deploy and evaluate

- [ ] For an existing installation, save its Deployment and ConfigMap before
  applying anything. Keep snapshots in `evals/results/` so they stay ignored:

  ```sh
  mkdir -p evals/results
  kubectl -n llm get deployment llm-gateway -o yaml > evals/results/deployment-before.yaml
  kubectl -n llm get configmap llm-gateway-config -o yaml > evals/results/config-before.yaml
  ```

- [ ] Install the existing namespace and identity resources:

  ```sh
  kubectl apply -f deploy/00-namespace.yaml
  kubectl apply -f deploy/serviceaccount.yaml -f deploy/callers.yaml -f deploy/rbac.yaml
  ```

- [ ] Ensure Redis and an OTLP collector are reachable. For a disposable
  development cluster, the existing `deploy/dev/redis.yaml` and
  `deploy/dev/otel-collector.yaml` provide them. These are development examples,
  not managed production infrastructure.
- [ ] Install the one-provider config and credential from the repository root.
  Use the ignored `secrets/openai` file created above; the secret has only the
  `openai` entry:

  ```sh
  kubectl -n llm create configmap llm-gateway-config --from-file=config.yaml=config/config.yaml --dry-run=client -o yaml | kubectl apply -f -
  kubectl -n llm create secret generic llm-gateway-provider-keys --from-file=openai=secrets/openai --dry-run=client -o yaml | kubectl apply -f -
  ```

- [ ] Render the Deployment with your published image before applying it. Set
  `GATEWAY_IMAGE` to the exact tag or digest built above:

  ```sh
  mkdir -p evals/results
  : "${GATEWAY_IMAGE:?Set GATEWAY_IMAGE to the published image tag or digest}"
  kubectl set image --local -f deploy/deployment.yaml gateway="$GATEWAY_IMAGE" -o yaml > evals/results/deployment-next.yaml
  kubectl apply -f evals/results/deployment-next.yaml -f deploy/service.yaml
  kubectl -n llm rollout restart deployment/llm-gateway
  kubectl -n llm rollout status deployment/llm-gateway --timeout=180s
  ```

  The restart also picks up ConfigMap changes; the app loads config at startup.
  Private image registries may require an imagePullSecret configured on the pod
  or ServiceAccount. Resolve that before expecting the rollout to finish.

- [ ] Run the authenticated evaluation using [the harness instructions](../../evals/README.md).
  Save a baseline, then rerun with a distinct filename after any change.
- [ ] Inspect pod logs, readiness and the evaluation report. Record image,
  configuration and report filename together.

## Rollback

If a prior installation existed, restore **both** saved resources and restart:

```sh
kubectl apply -f evals/results/config-before.yaml -f evals/results/deployment-before.yaml
kubectl -n llm rollout restart deployment/llm-gateway
kubectl -n llm rollout status deployment/llm-gateway --timeout=180s
```

Restoring only the image does not restore ConfigMap changes. If credentials were
changed, restore the previous credential through your secret management process
as well. For a first installation there is no prior version to roll back to;
use [the teardown runbook](../../docs/runbooks/teardown.md) only after checking for shared resources.
