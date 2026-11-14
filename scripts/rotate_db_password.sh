#!/usr/bin/env bash
# Move a service's database password to another Secret Manager version (design §4, ticket B2):
#   scripts/rotate_db_password.sh <api|ingest|scoring|notify> <version>
# It pins db-password-<svc> at <version> in BOTH places that read it, in one change:
#   deploy/run/<svc>.yaml     PGPASSWORD         (what the service logs in with)
#   deploy/run/migrate.yaml   DB_PASSWORD_<SVC>  (what migrate sets the role's password to, every run)
# Two different pins would set the role back on the next deploy and lock the service out (and trip
# the pooler's circuit breaker for the whole role); scripts/check_cloudrun.py fails on them. Both
# files are rewritten or neither: each pin must be exactly one line of the committed shape
#   - { name: <ENV>, valueFrom: { secretKeyRef: { name: db-password-<svc>, key: "<n>" } } }
# Local files only: no gcloud, no git. It prints the steps around it (add the version first, commit,
# merge; the deploy runs migrate before the service rolls; after a day, disable the old version).
# Exit 0 done (or nothing to do), 1 a file is not in the expected shape, 2 usage.
set -euo pipefail
cd "$(dirname "$0")/.."
die() { echo "rotate_db_password: $*" >&2; exit 1; }
usage() {
  echo "usage: scripts/rotate_db_password.sh <api|ingest|scoring|notify> <version>   (the db-password-<svc> version number)" >&2
  exit 2
}
[ "$#" = 2 ] || usage
svc=$1 ver=$2
case $svc in
  api | ingest | scoring | notify) ;;
  *) echo "rotate_db_password: '$svc' is not api, ingest, scoring or notify (migrate's own login, db-password-owner, is pinned in migrate.yaml by hand)" >&2; exit 2 ;;
esac
[[ $ver =~ ^[1-9][0-9]{0,5}$ ]] || { echo "rotate_db_password: '$ver' is not a Secret Manager version number (1, 2, ...; never latest)" >&2; exit 2; }
sec=db-password-$svc
up=$(printf '%s' "$svc" | tr '[:lower:]' '[:upper:]')
svc_file=deploy/run/$svc.yaml
mig_file=deploy/run/migrate.yaml
side=$(mktemp)
trap 'rm -f "$side" "$svc_file.rotate-new" "$mig_file.rotate-new"' EXIT

# pin <file> <env>: writes <file>.rotate-new with that env's pin of $sec at $ver; prints the old version.
pin() {
  local f=$1 env=$2 n old bad mentions
  [ -f "$f" ] || die "$f is missing"
  mentions=$(grep -cF "name: $sec," "$f" || true)
  [ "$mentions" = 1 ] || die "$f names $sec on $mentions lines: exactly one, the $env pin"
  : > "$side"
  awk -v head="- { name: $env, valueFrom: { secretKeyRef: { name: $sec, key: \"" -v tail="\" } } }" \
      -v ver="$ver" -v side="$side" '
    {
      ind = $0; sub(/[^ ].*$/, "", ind); body = substr($0, length(ind) + 1)
      if (substr(body, 1, length(head)) == head && length(body) > length(head) + length(tail) \
          && substr(body, length(body) - length(tail) + 1) == tail) {
        mid = substr(body, length(head) + 1, length(body) - length(head) - length(tail))
        if (mid ~ /^[1-9][0-9]*$/) { n++; old = mid; print ind head ver tail; next }
        bad = mid
      }
      print
    }
    END { printf "%d %s %s\n", n, (old == "" ? "-" : old), (bad == "" ? "-" : bad) > side }
  ' "$f" > "$f.rotate-new"
  read -r n old bad < "$side"
  [ "$bad" = - ] || die "$f: the $env pin of $sec has the key \"$bad\": a version number only (never latest); fix it by hand first"
  [ "$n" = 1 ] || die "$f: no line is exactly '- { name: $env, valueFrom: { secretKeyRef: { name: $sec, key: \"<n>\" } } }': fix it by hand (scripts/check_cloudrun.py says what it wants)"
  printf '%s' "$old"
}

was_svc=$(pin "$svc_file" PGPASSWORD)
was_mig=$(pin "$mig_file" "DB_PASSWORD_$up")
if [ "$was_svc" = "$ver" ] && [ "$was_mig" = "$ver" ]; then
  echo "rotate_db_password: $sec is already pinned at $ver in $svc_file and $mig_file; nothing changed"
  exit 0
fi
mv "$svc_file.rotate-new" "$svc_file"
mv "$mig_file.rotate-new" "$mig_file"
echo "rotate_db_password: $sec pinned at $ver in $svc_file (PGPASSWORD, was $was_svc) and $mig_file (DB_PASSWORD_$up, was $was_mig)"
[ "$was_svc" = "$was_mig" ] || echo "  note: the two pins differed ($was_svc and $was_mig); both are $ver now"
if [ "$ver" -lt "$was_svc" ]; then echo "  note: $ver is older than $was_svc (a rotation rolled back): that version must still be enabled"; fi
cat <<EOF
Next:
  0. The version must exist and be enabled before the merge (added with
     gcloud secrets versions add $sec --project=ryftlab-prod --data-file=-   which prints its number):
     gcloud secrets versions describe $ver --secret=$sec --project=ryftlab-prod --format='value(state)'   # ENABLED
  1. make cloudrun-check, then commit both files in ONE commit and open the PR.
  2. The merge's deploy (deploy.yml) runs migrate first, which sets the role ryft_$svc to version $ver,
     then rolls $svc onto it. Until $svc's new revision is ready its old one logs in with the old password;
     merge outside the testers' hours.
  3. After a day (design §4), not when the deploy turns green: until then the old version undoes the
     rotation (the rollback below). With all of $svc's traffic on the latest revision, disable it:
     gcloud run services describe $svc --region=europe-west1 --project=ryftlab-prod --format='yaml(status.traffic)'   # latestRevision: true, percent: 100
     gcloud secrets versions disable $was_svc --secret=$sec --project=ryftlab-prod
  Rollback: run this script with $was_svc again (re-enable that version first), commit, merge.
EOF
