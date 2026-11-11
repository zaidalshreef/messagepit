#!/usr/bin/env bash
# Start a loopback-only, temporary Dex and MessagePit instance for local testing.
set -euo pipefail
set +x

root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
cd "$root"
for tool in docker go curl npm; do
	if ! command -v "$tool" >/dev/null; then
		echo "$tool is required." >&2
		exit 1
	fi
done

tempdir=$(mktemp -d "${TMPDIR:-/tmp}/messagepit-dev-sso.XXXXXX")
umask 077
printf '%s\n' "$$" > "$tempdir/session.pid"
container="messagepit-dex-${tempdir##*.}"
started=false
app_pid=""
cleanup() {
	if [[ -n $app_pid ]]; then
		kill "$app_pid" 2>/dev/null || true
		wait "$app_pid" 2>/dev/null || true
	fi
	if [[ $started == true ]]; then docker rm -f "$container" >/dev/null 2>&1 || true; fi
	rm -rf -- "$tempdir"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

go run ./scripts/dev-sso-config "$tempdir"
npm run package
go build -o "$tempdir/messagepit" .
# Running as the current user keeps the temporary, mode-0600 configuration private.
docker run -d --rm --name "$container" --user "$(id -u):$(id -g)" \
	-p 127.0.0.1:18880:5556 \
	-v "$tempdir/dex.yaml:/etc/dex/config.yaml:ro" \
	ghcr.io/dexidp/dex:v2.46.1@sha256:65b72ca0f4b0bf6d4ada84ec21da8851782563404ac7b8f926923d27ceced9ab \
	dex serve /etc/dex/config.yaml >/dev/null
started=true
ready=false
for ((i = 0; i < 100; i++)); do
	if curl --noproxy '*' --fail --silent --max-time 1 http://127.0.0.1:18880/dex/.well-known/openid-configuration >/dev/null; then
		ready=true
		break
	fi
	if [[ $(docker inspect -f '{{.State.Running}}' "$container" 2>/dev/null || true) != true ]]; then break; fi
	sleep 0.1
done
if [[ $ready != true ]]; then
	echo "Dex did not become ready." >&2
	docker logs "$container" >&2 || true
	exit 1
fi

env -i PATH="$PATH" HOME="${HOME:-/tmp}" \
	MP_OIDC_ISSUER=http://127.0.0.1:18880/dex \
	MP_OIDC_CLIENT_ID=messagepit-local \
	MP_OIDC_PUBLIC_URL=http://127.0.0.1:18825 \
	MP_OIDC_ALLOWED_SUBJECTS="$(cat "$tempdir/allowed-subjects")" \
	MP_OIDC_ADMIN_SUBJECTS="$(cat "$tempdir/admin-subject")" \
	MP_DATABASE="$tempdir/messagepit.db" \
	MP_UI_BIND_ADDR=127.0.0.1:18825 \
	MP_SMTP_BIND_ADDR=127.0.0.1:18826 \
	MP_SENDGRID_BIND_ADDR=127.0.0.1:18827 \
	MP_TWILIO_BIND_ADDR=127.0.0.1:18828 \
	MP_WEBHOOK_BIND_ADDR=127.0.0.1:18829 \
	MP_SMTP_AUTH_ALLOW_INSECURE=1 MP_DISABLE_VERSION_CHECK=1 \
	"$tempdir/messagepit" &
app_pid=$!
ready=false
for ((i = 0; i < 100; i++)); do
	if curl --noproxy '*' --fail --silent --max-time 1 http://127.0.0.1:18825/readyz >/dev/null; then
		ready=true
		break
	fi
	if ! kill -0 "$app_pid" 2>/dev/null; then break; fi
	sleep 0.1
done
if [[ $ready != true ]]; then
	echo "MessagePit did not become ready." >&2
	exit 1
fi
printf 'Local MessagePit: http://127.0.0.1:18825/\n'
printf 'Temporary credentials: %s/credentials.txt\n' "$tempdir"
printf 'Session PID file: %s/session.pid\n' "$tempdir"
printf 'All data and credentials are removed when this process stops.\n'
printf 'Press Ctrl-C to stop the local test.\n'
wait "$app_pid"
