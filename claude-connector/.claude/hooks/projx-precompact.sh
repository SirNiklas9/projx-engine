#!/usr/bin/env bash
# ProjX → Claude Code connector — PreCompact hook (context-loss refill trigger).
#
# Claude Code fires PreCompact right before it compacts the conversation, which drops
# earlier injected context. PreCompact stdout is NOT added to the post-compaction
# context, so this hook does not try to re-inject here. Instead it tells the engine to
# RESET the session checkpoint (mark "floor lost", clear what's been seen) so the NEXT
# UserPromptSubmit re-sends the full floor (protocol + law) plus the active task slice.
#
# Best-effort and silent: a missing/erroring engine must never block compaction.
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

"$ENGINE" --root "$ROOT" context --session "$sid" --reset >/dev/null 2>&1 || true
exit 0
