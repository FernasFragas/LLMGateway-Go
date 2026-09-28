# Runbooks

Step-by-step procedures to follow when you need to *do* something to a
running system: deploy it, fix it, or remove it. Each one is meant to be run
top to bottom, not read for background.

| Runbook | Use it when |
|---|---|
| [gateway-pods-not-ready.md](gateway-pods-not-ready.md) | Pods stuck at `0/1`, or `rollout status` says "exceeded its progress deadline" |
| [production-without-aws.md](production-without-aws.md) | Deploying to a non-AWS managed cluster (GHCR + DigitalOcean, GKE, k3s…) |
| [teardown.md](teardown.md) | Removing everything this project created, locally or in the cloud |

**Not here, on purpose:**
- Guides and references stay in `docs/`: `local-testing.md`,
  `production-operations.md` (the *why* behind the production runbook),
  `api-requests.md`, `keeping-keys-safe.md`.
- `deploy-with-evals.md` is a one-off project checklist.
- `pod-logs-and-rollout.md` is marked as a historical record.
