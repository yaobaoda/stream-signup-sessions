#!/usr/bin/env bash
# Signup -> login -> ingest -> delivery, against a locally running streamauthd.
set -euo pipefail

BASE="${BASE:-http://localhost:8080}"
JAR="$(mktemp)"
EMAIL="nina+$$@studio.example"

curl -sS -X POST "$BASE/signup" -H 'content-type: application/json' \
  -d "{\"email\":\"$EMAIL\",\"password\":\"long-enough-passphrase\",\"captcha_token\":\"$CAPTCHA_TOKEN\",\"idempotency_key\":\"signup-$$\"}"
echo

curl -sS -X POST "$BASE/login" -c "$JAR" -H 'content-type: application/json' \
  -d "{\"email\":\"$EMAIL\",\"password\":\"long-enough-passphrase\"}"
echo

curl -sS -X POST "$BASE/assets" -b "$JAR" -H 'content-type: application/json' \
  -d '{"title":"ep-1","source_key":"masters/ep-1.mov","upload_id":"up-1"}'
echo

curl -sS -X POST "$BASE/assets/drain" -b "$JAR"
echo

curl -sS "$BASE/assets" -b "$JAR"
echo
rm -f "$JAR"
