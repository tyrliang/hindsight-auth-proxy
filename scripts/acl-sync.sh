#!/usr/bin/env bash
# scripts/acl-sync.sh — upload or download the hindsight-auth-proxy ACL from the
# Railway Storage Bucket (S3-compatible) for a given environment.
#
# Usage:
#   ./scripts/acl-sync.sh get <env> [outfile]
#     Download the current bucket acl.yaml.
#     Default outfile: acl-<env>.yaml
#
#   ./scripts/acl-sync.sh put <env> <file>
#     Validate <file> as YAML then upload it as the bucket acl.yaml.
#     After a successful put, redeploy the proxy to pick up the change:
#       railway redeploy --service hindsight-auth-proxy --environment <env>
#
# Requires: railway CLI (logged in, project linked), aws CLI, python3.

set -euo pipefail

# ── Validate arguments ─────────────────────────────────────────────────────────

subcmd="${1:?subcommand required: get or put}"
env="${2:?environment required: dev or prod}"

if [[ "${env}" != "dev" && "${env}" != "prod" ]]; then
  echo "ERROR: environment must be 'dev' or 'prod', got '${env}'" >&2
  exit 1
fi

# ── Load bucket credentials for the target environment ──────────────────────
# 'railway bucket credentials' emits AWS_* exports; eval sources them into the
# current shell. If the variable names differ in your Railway version, adjust
# the mapping below after running: railway bucket credentials --help
#
# Expected exports after eval:
#   AWS_ENDPOINT_URL_S3   — e.g. https://storage.railway.app
#   BUCKET                — bucket name
#   AWS_ACCESS_KEY_ID     — access key
#   AWS_SECRET_ACCESS_KEY — secret key
#   AWS_REGION            — e.g. us-east-1

eval "$(railway bucket credentials --environment "${env}" --output env 2>/dev/null \
  || railway bucket credentials --environment "${env}")"

# Fallback: some Railway CLI versions use ENDPOINT instead of AWS_ENDPOINT_URL_S3.
if [[ -z "${AWS_ENDPOINT_URL_S3:-}" && -n "${ENDPOINT:-}" ]]; then
  AWS_ENDPOINT_URL_S3="${ENDPOINT}"
fi

: "${AWS_ENDPOINT_URL_S3:?ACL_S3_ENDPOINT not exported by railway bucket credentials}"
: "${BUCKET:?BUCKET not exported by railway bucket credentials}"
: "${AWS_ACCESS_KEY_ID:?AWS_ACCESS_KEY_ID not exported by railway bucket credentials}"
: "${AWS_SECRET_ACCESS_KEY:?AWS_SECRET_ACCESS_KEY not exported by railway bucket credentials}"

readonly ACL_KEY="acl.yaml"

# ── Subcommand dispatch ────────────────────────────────────────────────────────

case "${subcmd}" in
get)
  outfile="${3:-acl-${env}.yaml}"
  echo "Downloading s3://${BUCKET}/${ACL_KEY} → ${outfile}"
  aws s3 cp "s3://${BUCKET}/${ACL_KEY}" "${outfile}" \
    --endpoint-url "${AWS_ENDPOINT_URL_S3}"
  echo "Downloaded: ${outfile}"
  ;;

put)
  infile="${3:?file required: ./scripts/acl-sync.sh put <env> <file>}"
  if [[ ! -f "${infile}" ]]; then
    echo "ERROR: file not found: ${infile}" >&2
    exit 1
  fi

  # Validate YAML before uploading — catch syntax errors locally.
  python3 -c "import yaml, sys; yaml.safe_load(open('${infile}'))" \
    && echo "YAML valid: ${infile}"

  echo "Uploading ${infile} → s3://${BUCKET}/${ACL_KEY}"
  aws s3 cp "${infile}" "s3://${BUCKET}/${ACL_KEY}" \
    --endpoint-url "${AWS_ENDPOINT_URL_S3}"
  echo "Upload complete. Redeploy the proxy to apply:"
  echo "  railway redeploy --service hindsight-auth-proxy --environment ${env}"
  ;;

*)
  echo "ERROR: unknown subcommand '${subcmd}'. Use get or put." >&2
  exit 1
  ;;
esac
