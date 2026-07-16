#!/usr/bin/env bash
# fetch-secrets.sh — ExecStartPre helper for sawt.service.
#
# Pulls the secret values below from GCP Secret Manager using the VM's
# attached service account (via the metadata server — no gcloud CLI needed)
# and writes /opt/sawt/.env fresh on every boot, instead of the service
# reading a static, manually-installed file. Closes IMPLEMENTATION-PLAN.md
# D-1 / Phase 1a: secrets stop living on disk between fetches.
#
# Requires: curl, jq (apt-get install -y jq — see gcp/README.md §3).
# Requires: the VM's service account has roles/secretmanager.secretAccessor
#           and the instance has the cloud-platform access scope (both
#           already granted per IMPLEMENTATION-PLAN.md, 2026-07-13).
#
# IMPORTANT — the SECRET_ID column below is a naming *guess* (kebab-case of
# the env var). Check it against what you actually created in Secret Manager
# (`gcloud secrets list`) and fix any mismatches before wiring this into
# sawt.service. Values not in Secret Manager, or left blank, are skipped —
# the corresponding line is simply omitted from .env (falls back to whatever
# default config.go applies, or the service fails closed if it's required).

set -euo pipefail

# Written directly (no temp-file + rename): systemd refuses to start
# sawt.service at all if this ExecStartPre exits non-zero, so a script
# failure mid-write is never exposed to the running service — it just fails
# closed. Direct writes also avoid needing the containing directory itself
# writable under ProtectSystem=strict, only this one file (see sawt.service).
ENV_FILE="/opt/sawt/.env"

# env var name -> Secret Manager secret id
declare -A SECRETS=(
  [DATABASE_URL]="database-url"
  [NIM_API_KEY]="nim-api-key"
  [AGENT_GATEWAY_SECRET]="agent-gateway-secret"
  [MSHALIA_API_URL]="mshalia-api-url"
  [DEFAULT_ORG_ID]="default-org-id"
  [GROQ_API_KEY]="groq-api-key"
  [HF_API_KEY]="hf-api-key"
  [GCP_API_KEY]="gcp-api-key"
  [OPENAI_API_KEY]="openai-api-key"
  [SESSION_SECRET]="session-secret"
  [ADMIN_USERNAME]="admin-username"
  [ADMIN_PASSWORD]="admin-password"
)

METADATA="http://metadata.google.internal/computeMetadata/v1"
PROJECT_ID="$(curl -sf -H 'Metadata-Flavor: Google' "${METADATA}/project/project-id")"
ACCESS_TOKEN="$(curl -sf -H 'Metadata-Flavor: Google' \
  "${METADATA}/instance/service-accounts/default/token" | jq -r '.access_token')"

if [[ -z "$PROJECT_ID" || -z "$ACCESS_TOKEN" || "$ACCESS_TOKEN" == "null" ]]; then
  echo "fetch-secrets: failed to get project id or access token from the metadata server" >&2
  exit 1
fi

: > "$ENV_FILE"
chmod 600 "$ENV_FILE"

fetch_missing=0
for env_name in "${!SECRETS[@]}"; do
  secret_id="${SECRETS[$env_name]}"
  url="https://secretmanager.googleapis.com/v1/projects/${PROJECT_ID}/secrets/${secret_id}/versions/latest:access"
  payload="$(curl -sf -H "Authorization: Bearer ${ACCESS_TOKEN}" "$url" || true)"
  value="$(printf '%s' "$payload" | jq -r '.payload.data // empty' | base64 -d 2>/dev/null || true)"
  if [[ -z "$value" ]]; then
    echo "fetch-secrets: WARNING — could not fetch secret '${secret_id}' for ${env_name}; skipping" >&2
    fetch_missing=1
    continue
  fi
  # Single-quote and escape any embedded single quotes so values with spaces
  # or special characters survive EnvironmentFile parsing intact.
  printf '%s=%s\n' "$env_name" "'${value//\'/\'\\\'\'}'" >> "$ENV_FILE"
done

# Standard prod defaults that are not secrets — kept out of Secret Manager,
# safe to hardcode here. Adjust to match gcp/.env.production if it diverges.
cat >> "$ENV_FILE" <<'EOF'
PORT=8080
SECURE_COOKIE=true
ALLOW_MISSING_FFMPEG=false
RETENTION_DAYS=90
EOF

if [[ "$fetch_missing" -eq 1 ]]; then
  echo "fetch-secrets: completed with missing secrets — see WARNINGs above" >&2
  # Non-fatal: let sawt.service start and fail its own required-config checks
  # (config.go) rather than silently masking which secret was wrong.
fi

echo "fetch-secrets: wrote ${ENV_FILE} ($(wc -l < "$ENV_FILE") lines)"
