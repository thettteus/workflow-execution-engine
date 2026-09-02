# Security Policy

## Reporting a vulnerability

Report privately through GitHub Security Advisories:

**<https://github.com/thettteus/workflow-execution-engine/security/advisories/new>**

Please don't open a public issue for a suspected vulnerability. Include what you need to make it
reproducible — a workflow definition, the `wee` command you ran, and what you expected the sandbox to
prevent.

This is a solo-maintained project. Expect an initial acknowledgement within about a week; if the report is
valid, you'll get an assessment and a fix timeline in the advisory thread.

## Supported versions

None yet, in the formal sense: there is no tagged release and no v1-stable API. Fixes land on `main`, which
is the only supported target today. This section will list versions once the project starts tagging.

## Scope

[docs/threat-model.md](docs/threat-model.md) is the authoritative document — it enumerates the trust
boundaries and what stops each attack. In short, the v1 posture is **deny-first**:

- the filesystem tool is confined to a workspace root
- the terminal tool sits behind a command allowlist
- the HTTP tool sits behind a domain allowlist
- the git tool has no remote access
- artifacts are content-addressed and the event log is hash-chained, so tampering is detectable
- secrets are stored and transported as references (`${env:...}`), never values

Reports that defeat one of those boundaries are in scope and interesting.

## Known limits — please don't report these as vulnerabilities

The threat model draws an explicit line between two trust tiers, and today only the first is closed:

- **Trusted definitions** — workflows authored by whoever runs the engine. The tool sandbox applies, but
  the definition's author is not treated as an adversary. This is the supported posture.
- **Untrusted definitions** — third-party workflows from a registry or template share. Items tagged
  _(Phase 2)_ in the threat model are **not fully closed**, and hardening them gates that transition.

So: running a hostile workflow definition you obtained from someone else is outside the current posture,
and is documented as such rather than being an unknown gap. A concrete escape that works against a
*trusted* definition — sandbox escape, allowlist bypass, secret exfiltration through a tool call, event-log
forgery — is very much in scope.

`wee` can also write files, run commands, and open pull requests when a workflow is authored to do so. That
is the product working as designed; mutating steps pause at approval checkpoints
([ADR 0016](docs/adr/0016-safe-mutation-approval-checkpoints.md)). A way to *bypass* a checkpoint is a
vulnerability.
