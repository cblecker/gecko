# AGENTS.md

Guidance for `marketplace-signup/`: the interim self-service sign-up backend and
SPA. It supplements the root `AGENTS.md`. Read `README.md` here first.

## Invariants

- **Stateless, no stored secrets.** Do not add a database, a cache that holds
  tokens, or any credential. The backend runs as its Workload Identity service
  account; the OAuth client ID is public.
- **Identity comes only from the verified ID token.** Do not read identity from
  the request body, headers or query. Keep the `hd` and email-domain agreement.
- **Never log tokens.** The signer's email is logged on purpose, for audit.
- **Log categories, not upstream text.** An error that reaches the logs carries
  its category, HTTP status codes and the workflow execution's name, never a
  response body or the workflow's message or stack trace.
- **Notification is fire-and-forget.** One attempt per successful activation. It
  must never change the response, block it, or be retried. Keep the panic
  containment in the handler's goroutine.
- **Activation and notification have separate destinations.**
  `WORKFLOW_PROJECT_ID`/`WORKFLOW_REGION` follow the authorization leader;
  `NOTIFY_PROJECT_ID`/`NOTIFY_REGION` follow the fixed hosting project. Never
  fall back from one to the other, and never take either from a request.
- **The notification inputs are a cross-repository contract.** The backend sends
  exactly `Signer`, `Project`, `Status` and `LeaderProject`
  (`internal/notify.Activation`). The integration definition is in gcp-hcp-infra
  (`helm/charts/signup-notify`). Changing an input means changing both, with the
  definition deployed first. The recipients and the environment are rendered into
  the definition and must not become backend inputs.
- **Reuse `activate-project` unchanged.** Do not add logic here that belongs in
  the workflow or in the platform API.

## Where changes go

| Change | Location |
| --- | --- |
| Backend behavior | `internal/` |
| New or changed setting | `internal/config`, the chart's `values.yaml` and `templates/_helpers.tpl`, and `README.md` |
| Chart | `helm/charts/marketplace-signup`, then `tests/render.sh` |
| Images | `deploy/marketplace-signup/*.Containerfile` (build context is the repository root) |
| Cloud identity, IAM, hostnames, per-environment values | gcp-hcp-infra, not this repository |

## Validation

```bash
make -C marketplace-signup test lint-fmt lint
helm/charts/marketplace-signup/tests/render.sh
```

- Tests must not need network access or credentials. Fake the Google APIs with
  `httptest` and the clients' `BaseURL` fields, as the existing tests do.
- Frontend behavior that has no JavaScript tests is covered where it can be: the
  entrypoint is tested from `frontend/entrypoint_test.go`.
