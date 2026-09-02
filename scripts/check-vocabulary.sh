#!/usr/bin/env bash
# Enforces PRIN-04's forbidden-vocabulary rule (CONSTITUTION.md, AGENTS.md).
#
# `Prompt`, `Agent`, `Chat` and `Memory` must not be used as domain concepts in
# schemas/, core/, cli/, sdk/ or docs/. The canonical replacements are
# Contract, Worker, Workspace and Workspace/Artifacts/Context — see
# docs/glossary.md's "instead of" table.
#
# Until now this rule was only a manual grep written down in EXECUTION.md's M1.0
# acceptance criteria, while AGENTS.md claimed CI enforced it. This script is
# that enforcement.
#
# The rule is about *domain* usage, so the gate carves out three things, each
# deliberately narrow:
#
#   1. Files whose subject matter IS the vocabulary rule or the project's
#      positioning. They cannot state "this is not an agent framework" without
#      using the word. Listed in EXEMPT_FILES.
#   2. Standard technical senses that collide with the word list ("in-memory",
#      OpenAI's chat/completions endpoint, a browser permission prompt). Listed
#      in ALLOWED_PATTERNS.
#   3. A `vocab-allow` marker for one-off legitimate prose, so the judgment sits
#      next to the sentence where a reviewer sees it. In Markdown use an inline
#      HTML comment: <!-- vocab-allow: why -->. It must be on the offending line
#      and must not start that line: a line beginning with <!-- is an HTML block
#      and would split the surrounding paragraph in two when rendered.
#
# Usage: scripts/check-vocabulary.sh   (exit 0 clean, 1 on violations)

set -euo pipefail

cd "$(dirname "$0")/.."

FORBIDDEN='\b(prompt|agent|chat|memory)\b'
SCAN_DIRS=(schemas core cli sdk docs)

# Subject matter is the rule itself or the project's positioning against it.
EXEMPT_FILES=(
  docs/glossary.md                        # defines the "instead of" table PRIN-04 carves out
  docs/CONSTITUTION.md                    # states PRIN-04 verbatim
  docs/VISION.md                          # positioning: "not an AI agent framework"
  docs/ROADMAP.md                         # restates the rule and the non-goals list
  docs/EXECUTION.md                       # playbook quoting the rule; historical record
  docs/EXECUTION-PHASE2.md                # same
  docs/adr/README.md                      # ADR index, titles include ADR 0001's
  docs/adr/0001-harness-oriented-docs.md  # about coding-agent harness consumption
)

# Standard technical senses that are not the domain concept. Note the trade-off:
# a match skips the whole line, so a line mixing an allowed sense with a genuine
# domain use would slip through. Kept simple deliberately — the gate guards
# against drift, it is not a parser.
ALLOWED_PATTERNS=(
  'in-memory'            # standard term for non-persistent storage
  'in memory'            # same, spelled open
  'chat[-/]completions'  # OpenAI's API path and its vendor name (ADR 0006)
  'coding-agent'         # the harness that reads these docs, per ADR 0001
  'agent[- ]harness'     # same
  'AGENTS\.md'           # the cross-tool filename convention, per ADR 0001
  'permission prompt'    # the browser/OS Notification permission dialog
  'process memory'       # where a value lives at runtime
  'memory amplification' # a named CVE class (EXECUTION.md dependency vetting)
  'exhaust memory'       # resource-exhaustion threat wording
  'vocab-allow'          # the inline exemption marker itself
)

# Compared case-insensitively on purpose: this repo is developed on macOS, where
# core.ignorecase makes docs/glossary.md show up as docs/GLOSSARY.md in the
# working tree, while a Linux CI checkout gets git's tracked lowercase name.
# A case-sensitive match here would pass locally and fail in CI.
lower() { printf '%s' "$1" | tr '[:upper:]' '[:lower:]'; }

exempt_file() {
  local f
  f=$(lower "$1")
  for e in "${EXEMPT_FILES[@]}"; do
    [[ "$f" == "$(lower "$e")" ]] && return 0
  done
  return 1
}

allowed_line() {
  local line=$1
  for p in "${ALLOWED_PATTERNS[@]}"; do
    grep -qiE "$p" <<<"$line" && return 0
  done
  return 1
}

violations=0
while IFS= read -r hit; do
  file=${hit%%:*}
  rest=${hit#*:}
  lineno=${rest%%:*}
  text=${rest#*:}

  exempt_file "$file" && continue
  allowed_line "$text" && continue

  if [[ $violations -eq 0 ]]; then
    echo "PRIN-04 forbidden-vocabulary violations:"
    echo
  fi
  violations=$((violations + 1))
  printf '  %s:%s\n    %s\n' "$file" "$lineno" "$(sed 's/^[[:space:]]*//' <<<"$text")"
done < <(
  grep -rniE "$FORBIDDEN" "${SCAN_DIRS[@]}" \
    --include='*.go' --include='*.md' --include='*.json' \
    --exclude-dir=private \
    2>/dev/null || true
)

if [[ $violations -gt 0 ]]; then
  echo
  echo "$violations violation(s). Use Contract / Worker / Workspace / Artifacts / Context"
  echo "instead (docs/glossary.md), or mark a legitimate non-domain use with vocab-allow."
  exit 1
fi

echo "PRIN-04 vocabulary check passed: no forbidden domain vocabulary in ${SCAN_DIRS[*]}."
