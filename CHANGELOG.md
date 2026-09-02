# Changelog

All notable changes to this project are documented here.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). This project will follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html) from its first tagged release.

## [Unreleased]

Nothing has been released yet — there is no tag, no published binary, and no v1-stable API. Development
history before this point is tracked at milestone granularity in [docs/ROADMAP.md](docs/ROADMAP.md) and the
execution plans ([Phase 1](docs/EXECUTION.md), [Phase 2](docs/EXECUTION-PHASE2.md)), which record what each
milestone delivered and how it was verified. Versioned entries here begin at the first tag.

### Added

- CI enforcement of the PRIN-04 forbidden-vocabulary rule, via `scripts/check-vocabulary.sh` and its own
  workflow. The rule was previously documented as enforced but was only ever a manual grep.
- `make vocab`, to run that gate locally.
- `CONTRIBUTING.md`, `SECURITY.md`, and this changelog.

### Fixed

- The lint job had never succeeded on any commit: `golangci-lint` was pinned to v1.55.2, built with go1.22,
  whose typechecker cannot parse the go1.23 this module targets. Bumped to v1.64.8, the last v1 release.
- Two unchecked `json.Decoder.Decode` errors in the server tests, which would have reported a malformed
  body as a confusing assertion failure against the zero value.
- A deprecated `tar.TypeRegA` case in `wee backup restore`. `archive/tar` normalizes that flag before
  returning a header, so the branch was unreachable.
- Flaky `SettingsModal` secret save/clear tests, which chained three wall-clock waits and could exceed
  their budget under a loaded full-suite run.
- Two docs used `prompt` as a domain concept where the canonical term is `Context` or `inputs`.
