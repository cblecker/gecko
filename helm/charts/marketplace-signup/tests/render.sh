#!/usr/bin/env bash
# Render tests for the marketplace-signup chart. Requires helm.
set -euo pipefail

chart_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
tmp_dir=$(mktemp -d)
trap 'rm -rf "$tmp_dir"' EXIT

fail() {
	echo "FAIL: $*" >&2
	exit 1
}

# A complete, valid configuration. The workflow runs in the authorization
# leader's project and region; the notifier lives in the separate hosting
# project and region.
valid=(
	--set enabled=true
	--set image.tag=abc1234
	--set spa.image.tag=abc1234
	--set gcp.projectId=host-project-1
	--set gateway.hostname=signup.int.gcp-hcp.devshift.net
	--set serviceAccount.gcpServiceAccount=signup-backend@svc-project-1.iam.gserviceaccount.com
	--set config.oauthClientId=123-abc.apps.googleusercontent.com
	--set config.workflowProjectId=leader-project-1
	--set config.workflowRegion=us-east1
	--set config.notifyIntegration=signup-notify
	--set config.notifyProjectId=host-project-1
	--set config.notifyRegion=us-central1
)

render() {
	helm template test "$chart_dir" --namespace marketplace-signup "$@"
}

# render_only TEMPLATE... renders one template with the valid configuration.
render_one() {
	local template=$1
	shift
	render "${valid[@]}" "$@" --show-only "templates/$template"
}

expect() { # expect FILE PATTERN DESCRIPTION
	grep -q -- "$2" "$1" || fail "$3 (missing: $2)"
}

# env_value FILE NAME prints the value of one container env var.
env_value() {
	awk -v name="$2" '$0 ~ "- name: " name "$" { getline; gsub(/.*value: "?|"$/, ""); print; exit }' "$1"
}

# Disabled by default: nothing renders until an environment opts in.
render > "$tmp_dir/default.yaml"
if grep -q '^kind:' "$tmp_dir/default.yaml"; then
	fail "the chart rendered objects while disabled"
fi

# Enabled: both workloads and their routing exist.
render "${valid[@]}" > "$tmp_dir/all.yaml"
for kind in Deployment Service ServiceAccount HTTPRoute HealthCheckPolicy \
	CertificateManagerCertificate CertificateManagerCertificateMapEntry; do
	grep -q "^kind: $kind$" "$tmp_dir/all.yaml" || fail "no $kind rendered"
done
[ "$(grep -c '^kind: Deployment$' "$tmp_dir/all.yaml")" = 2 ] || fail "expected two Deployments"
[ "$(grep -c '^kind: HealthCheckPolicy$' "$tmp_dir/all.yaml")" = 2 ] || fail "expected two HealthCheckPolicies"

# Images use the Gecko registry/repository:tag convention and the GAR defaults.
render_one deployment.yaml > "$tmp_dir/backend.yaml"
render_one spa-deployment.yaml > "$tmp_dir/spa.yaml"
expect "$tmp_dir/backend.yaml" 'image: "us-docker.pkg.dev/gcp-hcp-commons/gcp-hcp-images/gecko-marketplace-signup-backend:abc1234"' "backend image"
expect "$tmp_dir/spa.yaml" 'image: "us-docker.pkg.dev/gcp-hcp-commons/gcp-hcp-images/gecko-marketplace-signup-frontend:abc1234"' "SPA image"

# Activation and notification are configured separately: the workflow runs in
# the leader's project and region, the notifier in the hosting project.
[ "$(env_value "$tmp_dir/backend.yaml" WORKFLOW_PROJECT_ID)" = leader-project-1 ] || fail "WORKFLOW_PROJECT_ID"
[ "$(env_value "$tmp_dir/backend.yaml" WORKFLOW_REGION)" = us-east1 ] || fail "WORKFLOW_REGION"
[ "$(env_value "$tmp_dir/backend.yaml" NOTIFY_INTEGRATION)" = signup-notify ] || fail "NOTIFY_INTEGRATION"
[ "$(env_value "$tmp_dir/backend.yaml" NOTIFY_PROJECT_ID)" = host-project-1 ] || fail "NOTIFY_PROJECT_ID"
[ "$(env_value "$tmp_dir/backend.yaml" NOTIFY_REGION)" = us-central1 ] || fail "NOTIFY_REGION"

# Both pods run unprivileged with a read-only root filesystem, no capabilities
# and the runtime's default seccomp profile, and neither mounts a Kubernetes API
# token: the backend authenticates to Google through Workload Identity and the
# SPA has no cloud identity at all.
for workload in backend spa; do
	expect "$tmp_dir/$workload.yaml" 'automountServiceAccountToken: false' "$workload service account token"
	expect "$tmp_dir/$workload.yaml" 'runAsNonRoot: true' "$workload runAsNonRoot"
	expect "$tmp_dir/$workload.yaml" 'runAsUser: 65532' "$workload runAsUser"
	expect "$tmp_dir/$workload.yaml" 'type: RuntimeDefault' "$workload seccomp profile"
	expect "$tmp_dir/$workload.yaml" 'readOnlyRootFilesystem: true' "$workload read-only root"
	expect "$tmp_dir/$workload.yaml" 'allowPrivilegeEscalation: false' "$workload privilege escalation"
	expect "$tmp_dir/$workload.yaml" '- ALL' "$workload capabilities dropped"
done
# The SPA writes only to its three volumes. They must be emptyDir: the kubelet
# creates those world-writable, which is what lets UID 65532 write to them
# without an fsGroup. Another volume type would need that reconsidered.
for mount in /var/cache/nginx /usr/share/nginx/html /tmp; do
	expect "$tmp_dir/spa.yaml" "mountPath: $mount" "SPA volume at $mount"
done
[ "$(grep -c 'emptyDir: {}' "$tmp_dir/spa.yaml")" = 3 ] || fail "the SPA's three volumes must be emptyDir"

# Health: probes and the Gateway's health checks.
expect "$tmp_dir/backend.yaml" 'path: /healthz' "backend probes"
expect "$tmp_dir/spa.yaml" 'path: /index.html' "SPA probes"
render_one healthcheckpolicy.yaml > "$tmp_dir/backend-hc.yaml"
render_one spa-healthcheckpolicy.yaml > "$tmp_dir/spa-hc.yaml"
expect "$tmp_dir/backend-hc.yaml" 'requestPath: /healthz' "backend Gateway health check"
expect "$tmp_dir/spa-hc.yaml" 'requestPath: /index.html' "SPA Gateway health check"

# Routing: /api/ must be matched before the SPA's catch-all.
render_one httproute.yaml > "$tmp_dir/route.yaml"
api_line=$(grep -n 'value: /api/$' "$tmp_dir/route.yaml" | cut -d: -f1 || true)
spa_line=$(grep -n 'value: /$' "$tmp_dir/route.yaml" | cut -d: -f1 || true)
[ -n "$api_line" ] && [ -n "$spa_line" ] && [ "$api_line" -lt "$spa_line" ] || fail "/api/ must precede the SPA catch-all"
expect "$tmp_dir/route.yaml" 'name: signup-backend' "API backend"
expect "$tmp_dir/route.yaml" 'name: signup-spa' "SPA backend"

# Notifications are optional: without an integration, no notifier coordinates
# are needed and the backend starts with notifications disabled.
render "${valid[@]}" --set config.notifyIntegration= --set config.notifyProjectId= --set config.notifyRegion= \
	--show-only templates/deployment.yaml > "$tmp_dir/no-notify.yaml"
[ -z "$(env_value "$tmp_dir/no-notify.yaml" NOTIFY_INTEGRATION)" ] || fail "notifications should be disabled"

# Invalid configurations fail at render time, naming the setting.
rejects() { # rejects SETTING DESCRIPTION [helm args...]
	local setting=$1
	shift
	if render "${valid[@]}" "$@" > /dev/null 2> "$tmp_dir/error"; then
		fail "the chart accepted: $*"
	fi
	grep -q -- "$setting" "$tmp_dir/error" || fail "the error for '$*' does not name $setting: $(cat "$tmp_dir/error")"
}
rejects image.tag --set image.tag=
rejects spa.image.tag --set spa.image.tag=
rejects gateway.hostname --set gateway.hostname=
rejects serviceAccount.gcpServiceAccount --set serviceAccount.gcpServiceAccount=
rejects config.oauthClientId --set config.oauthClientId=
rejects config.workflowProjectId --set config.workflowProjectId=
rejects config.workflowRegion --set config.workflowRegion=
# The notifier never falls back to the workflow's project and region.
rejects config.notifyProjectId --set config.notifyProjectId=
rejects config.notifyRegion --set config.notifyRegion=
rejects config.notifyProjectId --set config.notifyProjectId=Bad_Project
rejects config.notifyProjectId --set config.notifyProjectId=ends-with-hyphen-
rejects config.notifyRegion --set config.notifyRegion=US-CENTRAL1
rejects config.notifyRegion --set config.notifyRegion=us-central

echo 'marketplace-signup chart tests passed.'
