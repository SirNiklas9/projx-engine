#!/usr/bin/env bash
# ProjX → Claude Code connector — SessionStart code-map refresh.
#
# Re-indexes the project's symbols (signature + doc + file:line anchor) into the store
# so the task-sliced context injects CURRENT anchors. Runs once per session at
# SessionStart — cheap and safe. Output is suppressed: this hook must inject NOTHING
# into the model context (it only writes the store); the context hook does the
# injecting. Mid-session incremental refresh on edits is a deliberate non-goal for now
# (a full re-sync on every keystroke would be costly on large repos).
#
# Best-effort: a missing/erroring engine never blocks the session.
set -u
unset PROJX_AGENT_CONTEXT 2>/dev/null || true

ENGINE="${PROJX_ENGINE_BIN:-projx-engine}"
ROOT="${CLAUDE_PROJECT_DIR:-$PWD}"

"$ENGINE" --root "$ROOT" map sync >/dev/null 2>&1 || true
exit 0
