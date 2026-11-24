#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: bash deploy_job.sh --plan|--apply

Required: GCP_PROJECT, ARTIFACT_REPOSITORY, IMAGE_TAG, JOB_SERVICE_ACCOUNT,
          SCHEDULER_SERVICE_ACCOUNT and one database configuration:
          DATABASE_URL_SECRET (Secret Manager NAME:VERSION)
          INSTANCE_CONNECTION_NAME, PGUSER, PGDATABASE, PGPASSWORD_SECRET
Optional: GCP_REGION (europe-west1), SCHEDULER_REGION (GCP_REGION),
          SCHEDULE (0 4 1 * *), SCHEDULE_TIME_ZONE (Etc/UTC)
EOF
}

case "${1:---plan}" in
  --plan) apply=0 ;;
  --apply) apply=1 ;;
  --help|-h) usage; exit 0 ;;
  *) usage >&2; exit 2 ;;
esac
if (( $# > 1 )); then usage >&2; exit 2; fi

: "${GCP_PROJECT:?Set GCP_PROJECT}" "${ARTIFACT_REPOSITORY:?Set ARTIFACT_REPOSITORY}"
if [[ $GCP_PROJECT == ryftlab-prod ]]; then
  printf 'Use the Terraform Nutrition rollout in infra/gcp/README.md for ryftlab-prod.\n' >&2
  exit 2
fi
: "${IMAGE_TAG:?Set IMAGE_TAG to a unique release tag}"
: "${JOB_SERVICE_ACCOUNT:?Set JOB_SERVICE_ACCOUNT}" "${SCHEDULER_SERVICE_ACCOUNT:?Set SCHEDULER_SERVICE_ACCOUNT}"
region=${GCP_REGION:-europe-west1}
scheduler_region=${SCHEDULER_REGION:-$region}
directory=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
name=nutrition-etl-job
image="${region}-docker.pkg.dev/${GCP_PROJECT}/${ARTIFACT_REPOSITORY}/${name}:${IMAGE_TAG}"

validate_secret() {
  if [[ ! $1 =~ ^[a-zA-Z0-9_-]+:[1-9][0-9]*$ ]]; then
    printf 'Expected a Secret Manager name and numeric version.\n' >&2
    exit 2
  fi
}

if [[ -n ${INSTANCE_CONNECTION_NAME:-} ]]; then
  : "${PGUSER:?Set PGUSER}" "${PGDATABASE:?Set PGDATABASE}" "${PGPASSWORD_SECRET:?Set PGPASSWORD_SECRET}"
  if [[ -n ${DATABASE_URL_SECRET:-} || ! $INSTANCE_CONNECTION_NAME =~ ^[a-zA-Z0-9-]+:[a-zA-Z0-9-]+:[a-zA-Z0-9-]+$ ||
        ! $PGUSER =~ ^[a-zA-Z0-9_-]+$ || ! $PGDATABASE =~ ^[a-zA-Z0-9_-]+$ ]]; then
    printf 'Use only the socket configuration with valid instance, user and database names.\n' >&2
    exit 2
  fi
  validate_secret "$PGPASSWORD_SECRET"
  db_flags=(--set-cloudsql-instances "$INSTANCE_CONNECTION_NAME"
            --set-env-vars "INSTANCE_CONNECTION_NAME=$INSTANCE_CONNECTION_NAME,PGUSER=$PGUSER,PGDATABASE=$PGDATABASE"
            --set-secrets "PGPASSWORD=$PGPASSWORD_SECRET")
else
  : "${DATABASE_URL_SECRET:?Set DATABASE_URL_SECRET or INSTANCE_CONNECTION_NAME}"
  validate_secret "$DATABASE_URL_SECRET"
  db_flags=(--remove-volume cloudsql --clear-env-vars --set-secrets "DATABASE_URL=$DATABASE_URL_SECRET")
fi

run() {
  if (( apply )); then
    "$@"
  else
    printf '%q ' "$@"
    printf '\n'
  fi
}

run gcloud builds submit "$directory" --project "$GCP_PROJECT" --tag "$image"
run gcloud run jobs deploy "$name" --project "$GCP_PROJECT" --region "$region" \
  --image "$image" --service-account "$JOB_SERVICE_ACCOUNT" --cpu 2 --memory 4Gi \
  --tasks 1 --parallelism 1 --max-retries 1 --task-timeout 1800s \
  --args=--source=all,--full,--write "${db_flags[@]}"
run gcloud run jobs add-iam-policy-binding "$name" --project "$GCP_PROJECT" --region "$region" \
  --member "serviceAccount:$SCHEDULER_SERVICE_ACCOUNT" --role roles/run.invoker

scheduler=(--project "$GCP_PROJECT" --location "$scheduler_region"
           --schedule "${SCHEDULE:-0 4 1 * *}" --time-zone "${SCHEDULE_TIME_ZONE:-Etc/UTC}"
           --uri "https://run.googleapis.com/v2/projects/$GCP_PROJECT/locations/$region/jobs/$name:run"
           --http-method POST --oauth-service-account-email "$SCHEDULER_SERVICE_ACCOUNT"
           --oauth-token-scope https://www.googleapis.com/auth/cloud-platform)
if (( apply )); then
  existing=$(gcloud scheduler jobs list --project "$GCP_PROJECT" --location "$scheduler_region" \
    --format='value(name.basename())')
  if printf '%s\n' "$existing" | grep -Fxq "$name"; then
    run gcloud scheduler jobs update http "$name" "${scheduler[@]}"
  else
    run gcloud scheduler jobs create http "$name" "${scheduler[@]}"
  fi
else
  printf '# The command uses update when the schedule already exists.\n'
  run gcloud scheduler jobs create http "$name" "${scheduler[@]}"
fi
