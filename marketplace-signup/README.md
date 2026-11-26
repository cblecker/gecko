# Marketplace sign-up (interim)

Interim self-service bootstrap for Private Preview: a user signs in with their
Red Hat Google Workspace identity, submits a GCP project ID, and receives the
initial `service-admin` RoleBinding in that project's namespace. It stands in
for the Marketplace controller until the Marketplace integration can supply the
customer's identity and target project.

This module owns the application: the Go backend, the static SPA, the Helm chart
(`helm/charts/marketplace-signup`) and the container images
(`deploy/marketplace-signup`). Cloud identities, IAM, hostnames, per-environment
values and the notification integration belong to
[gcp-hcp-infra](https://github.com/openshift-online/gcp-hcp-infra). The earlier
experiment (`gcp-hcp/experiments/marketplace-signup-service`) remains as
historical reference only.

## Scope: Red Hat identities only

The backend accepts only identities whose Google-signed hosted-domain claim
(`hd`) is in `ALLOWED_EMAIL_DOMAINS` (default `redhat.com`) and whose email
domain agrees with it. It cannot onboard real Marketplace customers. Relaxing
the policy is a deliberate design change, not a configuration tweak.

The OAuth client is a per-environment Google Identity Services Web client with
sign-in scopes only (`openid email profile`). It cannot be created by Terraform,
so it is a one-time console step per environment. Only its public client ID is
configured; there is no client secret and no redirect URI.

> **Interim limitation: project ownership is not verified.** Workspace
> app-access control blocks the scope needed to read a project's IAM policy as
> the user, so any accepted Red Hat user can activate any syntactically valid
> project ID. First-subject-wins means a mistaken claim needs an operator to
> undo. See the interim sign-up design decision in gcp-hcp
> (`design-decisions/identity/marketplace-signup-interim-bootstrap.md`).

## How it works

The SPA and API share one hostname behind the regional Gateway. The HTTPRoute
sends `/api/` to the backend and everything else to the SPA.

1. The SPA gets a Google ID token through Google sign-in and `POST`s
   `{project_id, id_token}` to `/api/v1/signup` on its own origin.
2. The backend validates the token (signature, issuer, audience, expiry,
   verified email) and the hosted-domain policy.
3. It runs the existing `activate-project` Cloud Workflow with
   `{project_namespace, subject}` in the **authorization leader's** project. The
   workflow creates `service-admin-rb` idempotently, rejecting a different
   subject (409) and reserved namespaces (403).
4. After either success result it sends a best-effort email by executing the
   `signup-notify` Application Integration, then answers the user. The email
   never changes the response and is never retried.

### Two destinations

The workflow and the notifier are configured independently, because they do not
move together:

| | Settings | Follows |
| --- | --- | --- |
| Activation workflow | `WORKFLOW_PROJECT_ID`, `WORKFLOW_REGION` | The current authorization leader. |
| Notification integration | `NOTIFY_PROJECT_ID`, `NOTIFY_REGION` | The environment's fixed hosting project. |

There is no fallback from one to the other: with `NOTIFY_INTEGRATION` set, both
notification settings are required and validated at startup (and when the chart
renders).

### Notification contract

The backend executes the integration with exactly these inputs:

| Input | Value |
| --- | --- |
| `Signer` | The verified, normalized email that activated the project. |
| `Project` | The customer project ID the user submitted. |
| `Status` | `activated` or `already_active`. |
| `LeaderProject` | `WORKFLOW_PROJECT_ID`, the project whose workflow ran. Never from the request. |

The integration definition lives in gcp-hcp-infra. It renders the recipients, the
environment and the hosting project, so the backend does not send them and
cannot redirect mail. **Adding, renaming or removing an input is a change to both
repositories**: update the definition before deploying a backend that sends a new
input.

### What the backend never does

- Persist tokens or any state: it has no database and no stored secrets.
- Write tokens to logs. The signer's email is logged deliberately, for audit.
- Trust client-supplied identity: the email comes from the signed ID token.
- Inspect the customer's project (see the limitation above).
- Let a request choose the leader project, the notification destination or the
  recipients.

## Layout

```text
marketplace-signup/
├── cmd/server/            entrypoint and wiring
├── internal/
│   ├── auth/              ID token and issuer verification
│   ├── config/            environment configuration and validation
│   ├── httpapi/           HTTP handlers, error contract, CORS
│   ├── identity/          hosted-domain policy and email normalization
│   ├── notify/            signup-notify execution (best effort)
│   └── workflow/          activate-project execution and outcome mapping
└── frontend/              static SPA, nginx.conf and the container entrypoint
```

## Configuration

| Variable | Purpose |
| --- | --- |
| `OAUTH_CLIENT_ID` | Dedicated OAuth client ID. ID tokens must carry it as `aud` (required). |
| `ALLOWED_EMAIL_DOMAINS` | Hosted-domain allowlist (default `redhat.com`). |
| `WORKFLOW_PROJECT_ID`, `WORKFLOW_REGION` | Where `activate-project` runs: the authorization leader (required). |
| `WORKFLOW_NAME` | Workflow to run (default `activate-project`). |
| `NOTIFY_INTEGRATION` | Integration that sends the email. Empty disables notifications. |
| `NOTIFY_PROJECT_ID`, `NOTIFY_REGION` | Where the integration lives. Required, and validated, when `NOTIFY_INTEGRATION` is set. |
| `ALLOWED_ORIGIN` | Only for a split-origin deployment. Empty means same-origin only. |
| `FRONTEND_DIR` | Serve the SPA from disk at the same origin. Local development only. |
| `PORT` | Listen port (default `8080`). |

The backend uses Application Default Credentials: in a cluster, the pod's
Workload Identity service account. The frontend container takes
`SIGNUP_CLIENT_ID` (required) and `SIGNUP_API_BASE`. The API base is empty for
the normal same-origin deployment; otherwise it must be
`https://<name>.gcp-hcp.devshift.net` (no port or path), the only origins the
page's Content-Security-Policy allows. The container refuses to start on a value
that is not plausible, instead of failing silently in the browser.

## Development

Run everything from this directory. The repository has no root Go module.

```bash
make test            # backend and entrypoint tests
make lint-fmt lint   # formatting and linting, as for the other modules
```

To run the backend with the SPA on one origin, render a local copy of the page
with your client ID (the container's entrypoint makes the same substitution) and
serve that directory. The backend uses Application Default Credentials, so run
`gcloud auth application-default login` first.

```bash
CLIENT_ID=<client-id>.apps.googleusercontent.com
mkdir -p /tmp/signup-local
cp frontend/app.js frontend/styles.css /tmp/signup-local/
sed "s|data-client-id=\"\"|data-client-id=\"$CLIENT_ID\"|" frontend/index.html > /tmp/signup-local/index.html

OAUTH_CLIENT_ID=$CLIENT_ID \
WORKFLOW_PROJECT_ID=<leader-project> WORKFLOW_REGION=<leader-region> \
FRONTEND_DIR=/tmp/signup-local \
  go run ./cmd/server          # http://localhost:8080
```

`app.js` reads the client ID from the page once, when it loads. To change it,
render the page again and reload; setting it later from the browser console has
no effect.

For local sign-in, use a development OAuth client and authorize both
`http://localhost` and `http://localhost:8080` as JavaScript origins, as Google's
guide requires for local testing.

## Images and chart

Both images build from the repository root:

```bash
podman build -f deploy/marketplace-signup/backend.Containerfile  -t marketplace-signup-backend .
podman build -f deploy/marketplace-signup/frontend.Containerfile -t marketplace-signup-frontend .
```

They run as UID 65532 with a read-only root filesystem. The frontend writes only
to `/tmp`, `/var/cache/nginx` and `/usr/share/nginx/html`, which the chart mounts
as `emptyDir` volumes. A quick check of the frontend:

```bash
podman run --rm -d --name signup-fe --read-only --user 65532:65532 --cap-drop ALL \
  --tmpfs /tmp:mode=1777 --tmpfs /usr/share/nginx/html:mode=1777 --tmpfs /var/cache/nginx:mode=1777 \
  -p 8081:8080 -e SIGNUP_CLIENT_ID=<client-id> marketplace-signup-frontend
curl http://127.0.0.1:8081/       # use 127.0.0.1: nginx listens on IPv4 only
```

The chart (`helm/charts/marketplace-signup`) is disabled by default and fails to
render when a required setting is missing or malformed. Its render tests need
`helm`:

```bash
helm/charts/marketplace-signup/tests/render.sh
```

### Network isolation

The chart defines no NetworkPolicy yet. The only chart in Gecko or gcp-hcp-infra
that has one is `gecko-nodepool-upgrade`, a controller with no inbound traffic.
For the sign-up service a correct policy needs this cluster's Gateway
health-check and proxy-only ranges, the metadata server and Google API egress,
and the pod and service CIDRs, none of which the chart can know, and a wrong
ingress rule would take the service down. It should be designed and tested on a
real cluster and added as an opt-in, values-driven template like that chart's,
not guessed here.
