#!/usr/bin/env bash
# One-time provisioning for feral-shortener in the feral-mode-web GCP project.
# Idempotent; safe to re-run. Assumes the studio's infrastructure already
# exists (feral-db VM, Artifact Registry repo "app", feral-run service account).
#
# Prerequisites, done by hand before running this:
#   1. Create the shortener database on the feral-db VM (see README.md):
#        gcloud compute ssh feral-db --zone=us-central1-a -- -N -L 15432:localhost:5432
#        psql "postgresql://feral:$(gcloud secrets versions access latest --secret=feral-db-password)@localhost:15432/feral" \
#          -c 'CREATE DATABASE shortener OWNER feral'
#   2. gcloud auth login
set -euo pipefail

PROJECT_ID="${PROJECT_ID:-feral-mode-web}"
REGION="us-central1"
SERVICE="feral-shortener"
SA="feral-run@${PROJECT_ID}.iam.gserviceaccount.com"

ensure_secret() {
  local name="$1"
  if ! gcloud secrets describe "$name" --project="$PROJECT_ID" >/dev/null 2>&1; then
    gcloud secrets create "$name" --project="$PROJECT_ID" --replication-policy=automatic
    echo "Created secret $name — add a version:"
    echo "  printf '%s' '<value>' | gcloud secrets versions add $name --project=$PROJECT_ID --data-file=-"
  fi
  gcloud secrets add-iam-policy-binding "$name" --project="$PROJECT_ID" \
    --member="serviceAccount:${SA}" --role="roles/secretmanager.secretAccessor" >/dev/null
}

# DATABASE_URL: postgresql://feral:<feral-db-password>@10.128.0.2:5432/shortener
ensure_secret shortener-database-url
# ADMIN_API_KEY: generate with `openssl rand -hex 32`
ensure_secret shortener-admin-key

echo
echo "Once both secrets have versions and the first image is pushed"
echo "(bash deploy/deploy.sh), bind the secrets to the service:"
echo
echo "  gcloud run services update $SERVICE --project=$PROJECT_ID --region=$REGION \\"
echo "    --set-secrets=DATABASE_URL=shortener-database-url:latest,ADMIN_API_KEY=shortener-admin-key:latest"
echo
echo "Then map the domain (after adding feralmo.de DNS records it prints):"
echo
echo "  gcloud beta run domain-mappings create --service=$SERVICE \\"
echo "    --domain=feralmo.de --project=$PROJECT_ID --region=$REGION"
