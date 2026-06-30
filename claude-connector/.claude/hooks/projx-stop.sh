#!/usr/bin/env bash
# ProjX → Claude Code connector — Stop hook (SUGGEST-ONLY capture nudge).
#
# Fires when the agent finishes responding. It asks the engine whether the user
# explicitly flagged an @remember this session that went uncommitted to the store:
#   engine exit 2 -> a nudge is warranted: feed its stdout back as the Stop reason
#                    (exit 2 from a Stop hook blocks the stop and returns stderr to the
#                    model, so it can either commit the fact or note it wasn't needed).
#   engine exit 0 -> silent (nothing flagged, or a commit already landed).
# The engine self-disarms after one nudge, so this never loops. It is SUGGEST-ONLY:
# the engine never writes to the store — the agent (with the human) decides.
#
# Best-effort: any engine error falls through to a silent allow (never trap the agent).
set -u
unset PROJX_AGENT_CONTEXT 2>/dev/null || true

ENGINE="${PROJX_ENGINE_BIN:-projx-engine}"
ROOT="${CLAUDE_PROJECT_DIR:-$PWD}"

input="$(cat)"
if command -v jq >/dev/null 2>&1; then
  sid="$(printf '%s' "$input" | jq -r '.session_id // empty')"
else
  sid="$(printf '%s' "$input" | sed -n 's/.*"session_id"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n1)"
fi
[ -n "$sid" ] || sid="default"

out="$("$ENGINE" --root "$ROOT" session-suggest --session "$sid" 2>/dev/null)"
rc=$?
if [ "$rc" -eq 2 ] && [ -n "$out" ]; then
  echo "$out" >&2
  exit 2
fi
exit 0
