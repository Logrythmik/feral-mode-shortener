#!/usr/bin/env bash
# Build, push, and deploy feral-shortener to Cloud Run.
# Usage: bash deploy/deploy.sh
# First-time setup (secrets, DB, domain mapping): see deploy/provision.sh and README.md.
set -euo pipefail

if ! command -v gcloud >/dev/null 2>&1; then
  export PATH="/opt/homebrew/share/google-cloud-sdk/bin:/usr/local/share/google-cloud-sdk/bin:$PATH"
fi
if ! gcloud auth print-access-token >/dev/null 2>&1; then
  echo "gcloud session expired — run: gcloud auth login" >&2
  exit 1
fi

PROJECT_ID="${PROJECT_ID:-feral-mode-web}"
REGION="us-central1"
SERVICE="feral-shortener"
IMAGE="${REGION}-docker.pkg.dev/${PROJECT_ID}/app/${SERVICE}:$(git rev-parse --short HEAD)"
SA="feral-run@${PROJECT_ID}.iam.gserviceaccount.com"

cd "$(git rev-parse --show-toplevel)"

docker build --platform linux/amd64 -t "$IMAGE" .
docker push "$IMAGE"

# Secrets/env come from the service's existing configuration (set once by
# provision.sh); this script only rolls the image forward.
gcloud run deploy "$SERVICE" \
  --project="$PROJECT_ID" \
  --region="$REGION" \
  --image="$IMAGE" \
  --service-account="$SA" \
  --allow-unauthenticated \
  --min-instances=0 \
  --max-instances=2 \
  --memory=128Mi \
  --cpu=1 \
  --network=default --subnet=default \
  --vpc-egress=private-ranges-only

echo "Deployed $IMAGE"
