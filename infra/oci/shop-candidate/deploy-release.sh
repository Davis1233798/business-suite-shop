#!/usr/bin/env bash
# Runs only through the dedicated Shop candidate forced-command SSH key.
set -Eeuo pipefail

readonly repository=ghcr.io/davis1233798/business-suite-shop-station
readonly root=/srv/business-suite-shop-candidate
readonly shared=/usr/local/lib/business-suite-shop-candidate
readonly port=18195
readonly volume=business-suite-shop-candidate-data

sha=${1:-}
digest=${2:-}
[[ $sha =~ ^[0-9a-f]{40}$ && $digest =~ ^sha256:[0-9a-f]{64}$ ]] || {
  echo 'Expected a commit SHA and immutable image digest' >&2
  exit 2
}

image="$repository@$digest"
expected_arch=''
case "$(uname -m)" in
  x86_64) expected_arch=amd64 ;;
  aarch64|arm64) expected_arch=arm64 ;;
  *) echo 'Unsupported Shop candidate host architecture' >&2; exit 3 ;;
esac

install -d -m 700 "$root/releases" "$root/backups" "$root/shared"
exec 9>"$root/shared/deploy.lock"
flock -n 9 || { echo 'Another Shop candidate deployment is running' >&2; exit 4; }

docker pull "$image"
actual_arch=$(docker image inspect "$image" --format '{{.Architecture}}')
[[ $actual_arch == "$expected_arch" ]] || {
  echo "Candidate image architecture mismatch: expected $expected_arch, got $actual_arch" >&2
  exit 5
}
image_revision=$(docker image inspect "$image" --format '{{ index .Config.Labels "org.opencontainers.image.revision" }}')
[[ $image_revision == "$sha" ]] || {
  echo "Candidate image revision label does not match requested commit $sha" >&2
  exit 5
}

release="$root/releases/$sha"
if [[ -e $release ]]; then
  [[ -f $release/COMMIT && -f $release/IMAGE && $(<"$release/COMMIT") == "$sha" && $(<"$release/IMAGE") == "$image" ]] || {
    echo 'Commit already has a different immutable candidate image' >&2
    exit 6
  }
else
  release_tmp=$(mktemp -d "$root/releases/.${sha}.XXXXXX")
  install -m 600 "$shared/compose.yaml" "$release_tmp/compose.yaml"
  cat > "$release_tmp/release.env" <<EOF
SHOP_CANDIDATE_IMAGE=$image
SHOP_CANDIDATE_REVISION=$sha
SHOP_CANDIDATE_PORT=$port
SHOP_CANDIDATE_VOLUME=$volume
EOF
  chmod 600 "$release_tmp/release.env"
  printf '%s\n' "$sha" > "$release_tmp/COMMIT"
  printf '%s\n' "$image" > "$release_tmp/IMAGE"
  chmod 600 "$release_tmp/COMMIT" "$release_tmp/IMAGE"
  mv "$release_tmp" "$release"
fi

compose() {
  local dir=$1
  shift
  docker compose --project-name shop-private-candidate --project-directory "$dir" \
    --env-file "$dir/release.env" -f "$dir/compose.yaml" "$@"
}

check_health() {
  local expected=$1
  curl --fail --silent --show-error --retry 5 --retry-delay 2 \
    "http://127.0.0.1:$port/api/health" | python3 -c \
    'import json,sys; expected=sys.argv[1]; actual=json.load(sys.stdin)["data"]["revision"]; assert actual == expected, f"revision mismatch: {actual!r}"' "$expected"
}

previous=''
previous_sha=''
previous_image=''
backup=''
new_started=0
initial_retry=0
if [[ -L $root/current ]]; then
  previous=$(readlink -f "$root/current")
  case "$previous" in
    "$root"/releases/*) ;;
    *) echo 'Candidate current link leaves the candidate release root' >&2; exit 7 ;;
  esac
  [[ -f $previous/COMMIT && -f $previous/IMAGE ]] || { echo 'Previous Shop candidate release is incomplete' >&2; exit 7; }
  previous_sha=$(<"$previous/COMMIT")
  previous_image=$(<"$previous/IMAGE")
  [[ $previous_sha =~ ^[0-9a-f]{40}$ && $previous_image =~ ^${repository//./\\.}@sha256:[0-9a-f]{64}$ ]] || {
    echo 'Previous Shop candidate release metadata is invalid' >&2
    exit 7
  }
  previous_image_revision=$(docker image inspect "$previous_image" --format '{{ index .Config.Labels "org.opencontainers.image.revision" }}')
  [[ $previous_image_revision == "$previous_sha" ]] || {
    echo 'Previous candidate image revision label does not match release metadata' >&2
    exit 7
  }
elif docker volume inspect "$volume" >/dev/null 2>&1; then
  if [[ -f $root/INITIALIZING && ! -L $root/INITIALIZING ]]; then
    IFS= read -r initializing_sha < "$root/INITIALIZING"
    IFS= read -r initializing_image < <(sed -n '2p' "$root/INITIALIZING")
    [[ $initializing_sha == "$sha" && $initializing_image == "$image" ]] || {
      echo 'A failed first candidate release left data for a different image; manual recovery is required' >&2
      exit 8
    }
    initial_retry=1
  else
    echo 'An untracked candidate volume already exists; refusing to attach it' >&2
    exit 8
  fi
fi

rollback() {
  local status=$?
  trap - ERR
  echo "Shop candidate release failed ($status); restoring only the prior candidate release" >&2
  if [[ -n $previous ]]; then
    if (( new_started )) && [[ -s $backup/shop-candidate.sqlite ]]; then
      compose "$release" stop shop-candidate >/dev/null 2>&1 || true
      docker run --rm --network none --user 0:0 \
        --mount "type=volume,src=$volume,dst=/data" \
        --mount "type=bind,src=$backup,dst=/backup,readonly" \
        --entrypoint /bin/sh "$previous_image" -ec \
        'find /data -mindepth 1 -maxdepth 1 -exec rm -rf -- {} +; cp -a /backup/. /data/; chown -R 10001:10001 /data; chmod -R u+rwX,go-rwx /data' || {
          echo 'Shop candidate data snapshot restore failed' >&2
          exit 70
        }
    fi
    compose "$previous" up -d --no-build --pull never --force-recreate --wait --wait-timeout 180 shop-candidate || {
      echo 'Previous Shop candidate container could not be restored' >&2
      exit 71
    }
    check_health "$previous_sha" || {
      echo 'Previous Shop candidate health could not be verified' >&2
      exit 72
    }
  else
    compose "$release" stop shop-candidate >/dev/null 2>&1 || true
    compose "$release" rm -f shop-candidate >/dev/null 2>&1 || true
  fi
  exit "$status"
}

trap rollback ERR
compose "$release" config --quiet
if [[ -n $previous ]]; then
  previous_container=$(compose "$previous" ps -q shop-candidate)
  [[ -n $previous_container ]] || { echo 'Previous Shop candidate container is missing' >&2; exit 9; }
  backup="$root/backups/${previous_sha}-before-${sha}-$(date -u +%Y%m%dT%H%M%SZ)"
  mkdir -m 700 "$backup"
  compose "$previous" stop shop-candidate
  docker cp "$previous_container:/srv/station/data/." "$backup/"
  [[ -s $backup/shop-candidate.sqlite ]] || { echo 'Candidate database snapshot is missing' >&2; exit 10; }
  find "$backup" -type d -exec chmod 700 {} +
  find "$backup" -type f -exec chmod 600 {} +
fi

if [[ -z $previous && $initial_retry == 0 ]]; then
  printf '%s\n%s\n' "$sha" "$image" > "$root/.INITIALIZING.$$"
  chmod 600 "$root/.INITIALIZING.$$"
  mv "$root/.INITIALIZING.$$" "$root/INITIALIZING"
fi

new_started=1
compose "$release" up -d --no-build --pull never --wait --wait-timeout 180 shop-candidate
check_health "$sha"

ln -s "$release" "$root/.current.$$"
mv -Tf "$root/.current.$$" "$root/current"
trap - ERR
rm -f "$root/INITIALIZING"
printf 'Deployed private Shop candidate %s with %s on 127.0.0.1:%s\n' "$sha" "$digest" "$port"
