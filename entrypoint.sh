#!/bin/sh
set -e

mkdir -p /var/run/tailscale

# Start tailscaled in background with explicit socket path
tailscaled \
  --state=${TS_STATE_DIR:-/var/lib/tailscale} \
  --socket=/var/run/tailscale/tailscaled.sock &

# Wait for socket (up to 30 s)
for i in $(seq 1 30); do
  [ -S /var/run/tailscale/tailscaled.sock ] && break
  sleep 1
done
[ -S /var/run/tailscale/tailscaled.sock ] || { echo "tailscaled socket timeout"; exit 1; }

TS="tailscale --socket=/var/run/tailscale/tailscaled.sock"

$TS up \
  --authkey=${TS_AUTHKEY} \
  --hostname=${TS_HOSTNAME:-ai-memory-dev} \
  --accept-routes=false

# Configure serve: tailnet :8888 -> proxy loopback :8889
# --bg persists the serve config; --http injects Tailscale-User-Login automatically
$TS serve --bg --http=8888 http://127.0.0.1:8889
# Self-configure: serve mode and identity header for proxy binary
export TAILSCALE_SERVE_MODE=true
export DEV_IDENTITY_HEADER=Tailscale-User-Login


# exec replaces shell with proxy binary (PID 1 = proxy)
exec /usr/local/bin/hindsight_auth_proxy
