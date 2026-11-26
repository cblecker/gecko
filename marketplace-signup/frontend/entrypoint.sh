#!/bin/sh
# Container entrypoint. Renders the SPA into a writable directory (the root
# filesystem is read-only) and injects the public configuration into the
# page's meta tag, with no inline script, so the page's CSP needs no
# 'unsafe-inline'. Then it runs nginx.
set -eu

: "${SIGNUP_CLIENT_ID:?SIGNUP_CLIENT_ID must be set (public OAuth client ID)}"
: "${SIGNUP_API_BASE:=}"

# Both values are substituted into an HTML attribute, so accept only the
# characters real values use. Anything else is a misconfiguration to reject,
# not something to escape.
case "$SIGNUP_CLIENT_ID" in
*[!A-Za-z0-9._-]*)
	echo "SIGNUP_CLIENT_ID contains unexpected characters" >&2
	exit 1
	;;
esac
# The API is on the page's own origin, which is the normal case and leaves this
# empty. Otherwise the page's Content-Security-Policy (index.html) only allows
# connections to https://*.gcp-hcp.devshift.net on the default port, so any other
# origin could never work; reject it here instead of failing silently in the
# browser.
case "$SIGNUP_API_BASE" in
"") ;;
https://?*.gcp-hcp.devshift.net)
	case "${SIGNUP_API_BASE#https://}" in
	*[!A-Za-z0-9.-]*)
		echo "SIGNUP_API_BASE contains unexpected characters" >&2
		exit 1
		;;
	esac
	;;
*)
	echo "SIGNUP_API_BASE must be empty (same origin) or https://<name>.gcp-hcp.devshift.net, the only origins the page's Content-Security-Policy allows" >&2
	exit 1
	;;
esac

# The directories are overridable so the tests can run without root.
src="${SIGNUP_SRC_DIR:-/etc/signup}"
target="${SIGNUP_HTML_DIR:-/usr/share/nginx/html}"
mkdir -p "$target"
# Remove the targets first: a redirect or cp onto a pre-existing symlink (for
# example the nginx package's default index.html, copied into a new volume by
# some runtimes) would write through it, outside this directory.
rm -f "$target/index.html" "$target/app.js" "$target/styles.css"

sed \
	-e "s|data-client-id=\"\"|data-client-id=\"${SIGNUP_CLIENT_ID}\"|" \
	-e "s|data-api-base=\"\"|data-api-base=\"${SIGNUP_API_BASE}\"|" \
	"$src/index.html" >"$target/index.html"
cp "$src/app.js" "$src/styles.css" "$target/"

echo "signup SPA configured (api base: ${SIGNUP_API_BASE:-same-origin})"
exec nginx -g 'daemon off;'
