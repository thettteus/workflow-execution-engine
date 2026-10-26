# Self-Hosted Operation

M2.6 defines the boring path: one `wee` process, one persistent state directory, and optional Docker
Compose. Hosted operation is later; this is the local/self-hosted product.

## Paths

- State directory: `.workflow` by default, or `--workspace <dir>`.
- Docker state: `/data/.workflow`, persisted by the `wee-data` volume.
- Workflow files: current directory by default, or `--dir <dir>`.
- Docker workflow files: `/workflows`, persisted by the `wee-workflows` volume.
- Templates: `--templates <dir>`; the Compose file mounts `./examples/templates` read-only.
- Built UI: `wee serve --ui-dir <ui/dist>` serves the React app at `/` and keeps APIs under `/api`.

The state directory contains execution snapshots, event logs, artifacts, cache entries, and non-secret
settings. Secret values remain OS environment variables and are not stored in backups.

## Release Binary

Download the `wee_<os>_<arch>` archive from GitHub Releases, put `wee` on your `PATH`, then:

```sh
wee init
wee serve --addr 127.0.0.1:7676 --workspace .workflow --dir . --templates examples/templates
```

Open the UI from a local development build, or serve a production UI build from the same process:

```sh
pnpm --dir ui install
pnpm --dir ui build
wee serve --addr 127.0.0.1:7676 --workspace .workflow --dir . --templates examples/templates --ui-dir ui/dist
```

Run a read-only template from the CLI:

```sh
wee run examples/refactor-plan/workflow.yaml --input goal="summarize the project" --workspace .workflow
wee inspect <execution-id> --workspace .workflow
wee replay <execution-id> --workspace .workflow
```

## Docker Compose

```sh
docker compose up --build
```

Then open:

```text
http://localhost:7676
```

Use the Templates picker to import a workflow into the `/workflows` volume.

The control plane has no authentication yet (planned for M2.7), so `compose.yaml` publishes port 7676 on
`127.0.0.1` only. Do not change it to every interface, or put it behind a reverse proxy without
authentication. Anything that can reach the port can change settings, set secrets, approve mutations, and
start runs ([threat-model.md](threat-model.md) §6).

Provider keys are optional until a workflow needs them. Put them in a local, untracked
`compose.override.yaml`; Compose passes them to the container, and `wee` still treats those values as
environment secrets rather than persisted settings.

```yaml
services:
  wee:
    environment:
      OPENAI_API_KEY: ${OPENAI_API_KEY}
      ANTHROPIC_API_KEY: ${ANTHROPIC_API_KEY:-}
      GITHUB_AUTH_HEADER: ${GITHUB_AUTH_HEADER:-}
```

Stop and restart with:

```sh
docker compose down
docker compose up
```

The `wee-data` and `wee-workflows` volumes preserve history, artifacts, cache, settings, and imported
workflow files. To remove everything deliberately:

```sh
docker compose down -v
```

## Backup And Restore

Stop `wee serve` before restoring.

```sh
wee backup create wee-backup.tar.gz --workspace .workflow
wee backup restore wee-backup.tar.gz --workspace .workflow-restored
```

Restoring into a non-empty workspace requires an explicit force flag:

```sh
wee backup restore wee-backup.tar.gz --workspace .workflow --force
```

For Docker Compose:

```sh
docker compose stop wee
docker compose run --rm wee backup create /workflows/wee-backup.tar.gz --workspace /data/.workflow
docker compose run --rm wee backup restore /workflows/wee-backup.tar.gz --workspace /data/.workflow --force
docker compose up -d
```

## Upgrades

Before upgrading, create a backup. Then replace the release binary or rebuild/pull the Docker image and
start `wee serve` again. There are no schema migrations in M2.6; if a later version adds one, it must be
append-only or explicitly documented here before release.
