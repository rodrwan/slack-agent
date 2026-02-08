#!/bin/sh
set -eu

if [ -n "${OPENAI_API_KEY:-}" ]; then
  : "${CODEX_HOME:=/data/.codex}"
  export CODEX_HOME
  mkdir -p "$CODEX_HOME"

  tmp_auth="$CODEX_HOME/auth.json.tmp"
  printf '{"OPENAI_API_KEY":"%s"}\n' "$OPENAI_API_KEY" > "$tmp_auth"
  mv "$tmp_auth" "$CODEX_HOME/auth.json"
fi

exec /usr/local/bin/slack-codex-runner
