#!/usr/bin/env bash
set -euo pipefail

APP_USER="${APP_USER:-bifrost}"
APP_GROUP="${APP_GROUP:-$APP_USER}"
BIFROST_BINARY="${BIFROST_BINARY:-/usr/local/bin/bifrost-http}"
BIFROST_HOST="${BIFROST_HOST:-0.0.0.0}"
BIFROST_PORT="${BIFROST_PORT:-8080}"
BIFROST_APP_DIR="${BIFROST_APP_DIR:-/srv/bifrost/app}"
BIFROST_USER_CONFIG="${BIFROST_USER_CONFIG:-/srv/bifrost/.config/bifrost}"
BIFROST_CONFIG_PATH="${BIFROST_CONFIG_PATH:-/etc/bifrost/config.json}"
BIFROST_LOG_LEVEL="${BIFROST_LOG_LEVEL:-info}"
BIFROST_LOG_STYLE="${BIFROST_LOG_STYLE:-json}"
BIFROST_LOG_DIR="${BIFROST_LOG_DIR:-/srv/bifrost/logs}"

APP_HOME="$(getent passwd "${APP_USER}" | cut -d: -f6)"
if [[ -z "${APP_HOME}" ]]; then
  echo "[entrypoint] unable to determine home directory for ${APP_USER}" >&2
  exit 1
fi

XDG_CONFIG_HOME="$(dirname "${BIFROST_USER_CONFIG}")"
mkdir -p "${BIFROST_APP_DIR}" "${BIFROST_USER_CONFIG}" "${XDG_CONFIG_HOME}" "${BIFROST_LOG_DIR}"

if [[ -f "${BIFROST_CONFIG_PATH}" ]]; then
  cp -f "${BIFROST_CONFIG_PATH}" "${BIFROST_APP_DIR}/config.json"
  cp -f "${BIFROST_CONFIG_PATH}" "${BIFROST_USER_CONFIG}/config.json"
fi

chown -R "${APP_USER}:${APP_GROUP}" "${APP_HOME}" "${BIFROST_APP_DIR}" "${BIFROST_USER_CONFIG}" "${BIFROST_LOG_DIR}" "${XDG_CONFIG_HOME}"

export HOME="${APP_HOME}"
export XDG_CONFIG_HOME

exec gosu "${APP_USER}:${APP_GROUP}" "${BIFROST_BINARY}" \
  -host "${BIFROST_HOST}" \
  -port "${BIFROST_PORT}" \
  -app-dir "${BIFROST_APP_DIR}" \
  -log-level "${BIFROST_LOG_LEVEL}" \
  -log-style "${BIFROST_LOG_STYLE}"
