#!/usr/bin/env bash
# Installs WALRUS (Neo4j + engine + admin dashboard) with Docker Compose.
#
#   curl -fsSL https://raw.githubusercontent.com/WalrusYours/WalrusCore/main/install.sh | bash
#
# Re-running it upgrades: it keeps your .env, refreshes the compose file, pulls the images and
# recreates what changed. Like Coolify's installer it generates the secrets itself, never overwrites
# values you already have, and waits until the stack is healthy.
#
# Settings (all optional, read from the environment):
#   WALRUS_DIR           where to install (default /opt/walrus as root, else ~/walrus)
#   WALRUS_REF           branch or tag of the compose file to install (default main)
#   WALRUS_COMPOSE_URL   full URL of the compose file (overrides WALRUS_REF)
#   WALRUS_ADMIN_KEY     preset the admin key instead of generating one
#   DOCKERHUB_USERNAME, WALRUS_VERSION, DASHBOARD_VERSION, REGISTRY_URL, WALRUS_INSTANCE_NAME,
#   WALRUS_BIND, WALRUS_PORT, DASHBOARD_PORT   written to .env on first install (see .env.example)
#
# It does not install Docker or touch the host: get Docker with https://get.docker.com first.
set -euo pipefail

REF="${WALRUS_REF:-main}"
COMPOSE_URL="${WALRUS_COMPOSE_URL:-https://raw.githubusercontent.com/WalrusYours/WalrusCore/${REF}/docker-compose.yml}"
if [ -n "${WALRUS_DIR:-}" ]; then DIR="$WALRUS_DIR"
elif [ "$(id -u)" = 0 ]; then DIR=/opt/walrus
else DIR="$HOME/walrus"; fi

say()  { printf '%s\n' "$*"; }
warn() { printf 'warning: %s\n' "$*" >&2; }
die()  { printf 'error: %s\n' "$*" >&2; exit 1; }

# 1. Requirements.
command -v curl >/dev/null   || die "curl is required"
command -v docker >/dev/null || die "Docker is not installed. Install it first: curl -fsSL https://get.docker.com | sh"
docker compose version >/dev/null 2>&1 || die "the Docker Compose v2 plugin is required (docker compose version failed)"
docker info >/dev/null 2>&1  || die "cannot reach the Docker daemon: is it running, and may this user use it?"

random() {
  if command -v openssl >/dev/null; then openssl rand -hex 24
  else head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n'; fi
}

# env_get KEY: the value of KEY in .env, empty if absent. .env is read, never sourced.
env_get() { grep -E "^$1=" "$DIR/.env" 2>/dev/null | tail -n1 | cut -d= -f2- || true; }

# env_set KEY VALUE: add KEY only when .env does not already define it.
env_set() { [ -n "$(env_get "$1")" ] || printf '%s=%s\n' "$1" "$2" >> "$DIR/.env"; }

# 2. Directory and compose file.
mkdir -p "$DIR"
cd "$DIR"
say "Installing into $DIR"

tmp="$(mktemp)"
trap 'rm -f "$tmp"' EXIT
curl -fsSL "$COMPOSE_URL" -o "$tmp" || die "could not download $COMPOSE_URL (is the repository public?)"
if [ -f docker-compose.yml ] && ! cmp -s "$tmp" docker-compose.yml; then
  cp docker-compose.yml "docker-compose.yml.bak"
  say "Updated docker-compose.yml (previous copy kept as docker-compose.yml.bak)"
fi
mv "$tmp" docker-compose.yml
trap - EXIT

# 3. .env: generate what is missing, keep what is there.
fresh_key=""
if [ ! -f .env ]; then
  if docker volume inspect walrus-neo4j >/dev/null 2>&1; then
    warn "a Neo4j volume from an earlier install exists but .env is gone: it keeps its old password, so the"
    warn "generated one will not match. Restore the old .env, or remove the volume (wipes data): docker volume rm walrus-neo4j"
  fi
  : > .env
  chmod 600 .env
  say "Created .env"
fi
if [ -z "$(env_get WALRUS_ADMIN_KEY)" ]; then
  fresh_key="${WALRUS_ADMIN_KEY:-$(random)}"
  env_set WALRUS_ADMIN_KEY "$fresh_key"
fi
env_set NEO4J_PASSWORD "$(random)"
for var in WALRUS_INSTANCE_NAME DOCKERHUB_USERNAME REGISTRY_URL WALRUS_VERSION DASHBOARD_VERSION \
           WALRUS_BIND WALRUS_PORT DASHBOARD_PORT; do
  [ -z "${!var:-}" ] || env_set "$var" "${!var}"
done

# 4. Pull and start. `up` waits for each dependency to be healthy (neo4j, then the engine).
say "Pulling images..."
docker compose pull --quiet
say "Starting..."
docker compose up -d --remove-orphans

# 5. Wait for the dashboard, the last service to come up.
say "Waiting for the stack to be healthy..."
status=""
for _ in $(seq 1 60); do
  status="$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' walrus-dashboard 2>/dev/null || echo missing)"
  [ "$status" = healthy ] && break
  sleep 3
done
if [ "$status" != healthy ]; then
  docker compose ps || true
  die "the dashboard is '$status' after 3 minutes. See: docker compose -f $DIR/docker-compose.yml logs"
fi

# 6. Done.
port="$(env_get DASHBOARD_PORT)"; port="${port:-3001}"
eport="$(env_get WALRUS_PORT)"; eport="${eport:-8080}"
say ""
say "WALRUS is running."
say "  Dashboard   http://127.0.0.1:${port}   (loopback only: put a reverse proxy or SSH tunnel in front)"
say "  Engine API  http://127.0.0.1:${eport}"
say ""
if [ -n "$fresh_key" ]; then
  say "  Admin key   $fresh_key"
  say "  Sign in to the dashboard with it. It is shown once here; it is also in $DIR/.env."
else
  say "  Admin key   unchanged (see $DIR/.env)"
fi
say ""
say "Back up $DIR/.env somewhere safe: it holds the admin key and the Neo4j password."
