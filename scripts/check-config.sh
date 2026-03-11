#!/bin/sh
set -eu

if [ -f .env ]; then
  set -a
  # shellcheck disable=SC1091
  . ./.env
  set +a
fi

status=0

require() {
  var_name=$1
  eval "var_value=\${$var_name-}"
  if [ -z "$var_value" ]; then
    printf 'Missing required variable: %s\n' "$var_name" >&2
    status=1
  fi
}

require BALE_BOT_TOKEN
require BALE_ALLOWED_CHAT_IDS
require MATTERMOST_MODE

case "${MATTERMOST_MODE-}" in
  webhook)
    require MATTERMOST_WEBHOOK_URL
    ;;
  api)
    require MATTERMOST_BASE_URL
    require MATTERMOST_BOT_TOKEN
    require MATTERMOST_CHANNEL_ID
    ;;
  "")
    ;;
  *)
    printf 'Invalid MATTERMOST_MODE: %s\n' "${MATTERMOST_MODE}" >&2
    status=1
    ;;
esac

if [ "$status" -ne 0 ]; then
  printf 'Configuration is incomplete.\n' >&2
  exit "$status"
fi

printf 'Configuration looks complete for MATTERMOST_MODE=%s\n' "$MATTERMOST_MODE"
