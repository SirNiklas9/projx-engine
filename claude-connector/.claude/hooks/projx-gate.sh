#!/usr/bin/env bash
# ProjX → Claude Code connector — PreToolUse GATE hook (matcher: Read|Edit|Write).
#
# Claude Code passes the tool call as JSON on stdin. This hook extracts the target
# file path and asks the ProjX engine whether that path is off-limits:
#   exit 2  -> BLOCK the tool call; stderr is returned to Claude as the reason.
#   exit 0  -> allow.
#
# It never sets PROJX_AGENT_CONTEXT=1 (that restricted engine mode refuses the
# `gate` command), and it unsets any INHERITED value so the gate query always
# works even when this Claude was launched inside a ProjX caged `agent run`.
set -u
unset PROJX_AGENT_CONTEXT 2>/dev/null || true

ENGINE="${PROJX_ENGINE_BIN:-projx-engine}"
ROOT="${CLAUDE_PROJECT_DIR:-$PWD}"

input="$(cat)"

# Extract tool_input.file_path. Prefer jq (robust); fall back to a best-effort
# sed extraction if jq is not installed.
if command -v jq >/dev/null 2>&1; then
  path="$(printf '%s' "$input" | jq -r '.tool_input.file_path // empty')"
else
  path="$(printf '%s' "$input" \
    | sed -n 's/.*"file_path"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n1)"
fi

# No path to check (e.g. a matched tool that carries no file_path) -> allow.
[ -n "$path" ] || exit 0

reason="$("$ENGINE" --root "$ROOT" gate check "$path" 2>&1)"
rc=$?

if [ "$rc" -eq 2 ]; then
  # Blocking: on exit 2 Claude Code feeds this stderr back to the model.
  echo "${reason:-ProjX gate: \"$path\" is off-limits by a project gate rule.}" >&2
  exit 2
fi

if [ "$rc" -ne 0 ]; then
  # LOUD fail-open. Engine errored or binary missing: don't brick the session,
  # but make the loss of enforcement VISIBLE (not silent) so a misconfiguration
  # is noticed. The gate is one cooperative layer; OS-FS confinement (Landlock on
  # the Linux target) is the real wall.
  echo "ProjX gate UNAVAILABLE — NOT enforcing (engine rc=$rc). '$path' allowed unchecked." >&2
  exit 0
fi

# rc 0 = allowed.
exit 0
