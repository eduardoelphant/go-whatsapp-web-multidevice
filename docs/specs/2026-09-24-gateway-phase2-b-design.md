# Fork phase 2, part B: fork CI, image publishing, deployment on noria

**Date:** 2026-09-24
**Status:** approved design, awaiting implementation plan
**Depends on:** part A (`2026-09-24-gateway-phase2-a-design.md`), implemented on the local branch `elphant`

## 1. Context and scope

The fork `eduardoelphant/go-whatsapp-web-multidevice` is maintained as its own product
("fork only": no PRs or issues upstream). Branch `elphant` = upstream tag + fork commits.
Part B makes that branch buildable, publishable and running in production.

In scope:

| # | Deliverable | Where |
|---|---|---|
| B1 | Fork CI: upstream sync PRs, tests, multi-arch image on GHCR | New workflow files in the fork |
| B2 | Stack `gowa` on the noria Swarm | Portainer (source of truth), copy in the elphantcrm docs |
| B3 | Consistent backup of the gateway data | `/root/backup/bin/backup.sh` on noria |
| B4 | Move the lab test instance to noria without a new QR | One-off procedure |
| B5 | Operations doc | elphantcrm `docs/reference/38-gowa.md` |

Out of scope: the generic `gw.sh` installer (deferred until there is a second server; with one
server, updating `GOWA_VERSION` in the Portainer stack covers install, update and rollback),
Postgres for the gateway, durable webhook delivery (G3) and the other gateway items.

Nothing about noria (host names, IPs, stack file) goes into the public fork.

## 2. Decisions

| # | Decision | Discarded | Why |
|---|---|---|---|
| B-D1 | Sync by merge: a PR `sync/vX.Y.Z → elphant` whose head is the upstream tag commit, merged with a merge commit | Rebase of `elphant` with force-push | No force-push: every commit that became an image stays reachable, so rollbacks are reproducible. Fork commits stay listable with `git log vX.Y.Z..elphant`. Conflicts show once per tag, in the PR |
| B-D2 | The image is published only when the owner pushes a tag `vX.Y.Z-elphant.N` | Publish on every merge | Publishing is an explicit owner action |
| B-D3 | All gateway data in SQLite on a volume, backed up with `sqlite3 .backup` | Signal keys in Postgres with its own role | Chat storage (device slots, per-device webhooks) is SQLite-only in GOWA, so a volume exists anyway; a live `tar` of SQLite can capture a torn write; `.backup` is consistent while the app runs. The lab session moves by file copy, no conversion |
| B-D4 | Public HTTPS endpoint with GOWA Basic Auth, `/statics` blocked at Traefik, no IP allowlist | IP allowlist for the CRM server only | The CRM runs on another server and calls the API over the internet. The owner keeps browser access to the GOWA UI. An allowlist breaks silently if the CRM IP changes |
| B-D5 | Health check in the stack, not in the Dockerfile | `HEALTHCHECK` in `docker/golang.Dockerfile` | Same effect for Swarm, no upstream file edited |
| B-D7 | Sync PRs opened with a fork-scoped fine-grained token (`SYNC_TOKEN`) | `GITHUB_TOKEN`; manual sync | `GITHUB_TOKEN` cannot push upstream commits that touch workflow files and its PRs do not trigger CI. Manual sync turns every upstream tag into an owner request |
| B-D6 | GHCR package public | Private package with registry credentials in Swarm | The code is already public; Swarm pulls without stored credentials |

## 3. B1: fork CI

Three new files under `.github/workflows/`. No upstream workflow is edited. Upstream workflows
trigger only on tags matching `v[0-9]+.[0-9]+.[0-9]+` exactly (and on manual dispatch), so fork
tags `vX.Y.Z-elphant.N` do not start them, and upstream tags are never pushed to the fork.

### `elphant-sync.yml`

- Triggers: `schedule` daily at 09:00 UTC, and `workflow_dispatch` with an optional `base` input
  (default `elphant`, used only to test the workflow against a scratch branch).
- Token: fine-grained personal access token stored as the fork secret `SYNC_TOKEN`, scoped to
  this fork only, with Contents, Pull requests and Workflows read/write, 1-year expiry. The
  built-in `GITHUB_TOKEN` cannot push commits that touch `.github/workflows/` (upstream edits its
  workflows from time to time), and PRs it opens do not trigger other workflows.
- Steps:
  1. Check out the base branch with full history; add `upstream`
     (`https://github.com/aldinokemal/go-whatsapp-web-multidevice.git`) and fetch its tags.
  2. Pick the highest stable tag: `git tag -l 'v*' | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | sort -V | tail -1`.
  3. Stop if `git merge-base --is-ancestor <tag> origin/<base>` (already merged), or if the
     remote branch `sync/<tag>` already exists (PR already opened).
  4. Push branch `sync/<tag>` pointing at the tag commit (`git push origin <tag>^{commit}:refs/heads/sync/<tag>`).
  5. `gh pr create --base <base> --head sync/<tag>`, title `sync: upstream <tag>`, body with the
     upstream release URL and the instruction "merge with **Create a merge commit**; never squash
     or rebase".
- The Action performs no merge. Conflicts are resolved locally on the `sync/<tag>` branch by
  merging `elphant` into it.

### `elphant-ci.yml`

- Triggers: `pull_request` targeting `elphant`, and `push` to `elphant`.
- Permissions: `contents: read`.
- Runs from `src/`: `go vet ./...` and `go test ./...`, with `actions/setup-go` reading
  `go-version-file: src/go.mod`. CGO stays enabled (default SQLite build).
- The sync PR is opened with `SYNC_TOKEN`, so this workflow runs on it. GitHub runs it on the
  merge ref, so a green PR means the upstream tag plus fork commits pass together.

### `elphant-image.yml`

- Triggers: `push` of tags `v*-elphant.*`, and `workflow_dispatch` with a `tag` input.
- Permissions: `contents: read`, `packages: write`. Registry login with `GITHUB_TOKEN` only.
- Jobs:
  1. `test`: same as `elphant-ci.yml`.
  2. `build` (needs `test`), matrix on native runners: `linux/amd64` on `ubuntu-latest`,
     `linux/arm64` on `ubuntu-24.04-arm`. Builds `docker/golang.Dockerfile` and pushes
     `ghcr.io/eduardoelphant/gowa:<tag>-amd64` / `-arm64`.
  3. `manifest` (needs `build`): `docker buildx imagetools create -t ghcr.io/eduardoelphant/gowa:<tag>`
     from the two arch tags.
- No `latest` tag. The version is always pinned.

### One-time owner actions (GitHub, each with explicit approval)

- Enable Actions on the fork (disabled by default on forks).
- Set the fork's default branch to `elphant`: GitHub runs scheduled workflows only from the default
  branch, and `main` must stay a pure mirror.
- Create the `SYNC_TOKEN` fine-grained token and store it as a fork secret; renew it yearly.
- Push `elphant` to `origin` (first publication of the branch).
- After the first image: set the `gowa` package visibility to public.
- First tag: `v9.4.0-elphant.1` on the current `elphant` head.

## 4. B2: stack `gowa` on noria

Stack name `gowa`, one service `gowa` (Swarm name `gowa_gowa`, the same pattern as `postgres_postgres` and `traefik_traefik` on noria). Created through the Portainer API
(`POST /api/stacks/create/swarm/string`, header `X-API-Key` with an access token the owner
creates), so the stack in Portainer is exactly the reviewed file. Stack variables in Portainer:
`GOWA_VERSION`, `GOWA_BASIC_AUTH`.

```yaml
version: "3.8"

services:
  gowa:
    image: ghcr.io/eduardoelphant/gowa:${GOWA_VERSION}
    command: ["rest"]
    environment:
      APP_PORT: "3000"
      APP_BASIC_AUTH: ${GOWA_BASIC_AUTH}
      APP_DEBUG: "false"
      DB_URI: "file:storages/whatsapp.db?_foreign_keys=on"
      MCP_ENABLED: "false"
      WHATSAPP_AUTO_DOWNLOAD_MEDIA: "false"
    volumes:
      - storages:/app/storages
      - statics:/app/statics
    networks:
      - ElphantNet
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://127.0.0.1:3000/health"]
      interval: 30s
      timeout: 5s
      retries: 3
      start_period: 30s
    deploy:
      replicas: 1
      update_config:
        order: stop-first
        failure_action: rollback
      rollback_config:
        order: stop-first
      restart_policy:
        condition: any
      resources:
        limits:
          memory: 1G
        reservations:
          memory: 256M
      labels:
        traefik.enable: "true"
        traefik.swarm.network: ElphantNet
        traefik.http.routers.gowa.rule: Host(`devias.elphant.com.br`) && !PathRegexp(`(?i)^/+statics`)
        traefik.http.routers.gowa.entrypoints: websecure
        traefik.http.routers.gowa.tls.certresolver: letsencryptresolver
        traefik.http.routers.gowa.service: gowa
        traefik.http.services.gowa.loadbalancer.server.port: "3000"

volumes:
  storages:
  statics:

networks:
  ElphantNet:
    external: true
```

Notes:

- Swarm prefixes stack volumes with the stack name, so the volumes are `gowa_storages` and
  `gowa_statics` on the host.
- `devias.elphant.com.br` already resolves to noria (A record, not proxied) and no router uses
  it today, so no DNS change is needed. The certificate is issued on first request.
- `/health` is public by design in GOWA and reports service initialization, not WhatsApp
  connectivity. A disconnected device never makes Swarm restart the container.
- `/statics` (QR images, media) is served by GOWA before Basic Auth; the router rule keeps it off
  the internet. The match is a case-insensitive regex that also absorbs repeated slashes, because
  Fiber routes case-insensitively (`/STATICS/x` reaches the same files) and a plain `PathPrefix`
  would not catch it. The proper fix (authenticated `/statics`, G8) is a later gateway item. The CRM gets media through authenticated endpoints (G4, later).
- `MCP_ENABLED` defaults to `true` in GOWA; it is turned off.
- `WHATSAPP_AUTO_DOWNLOAD_MEDIA=false` keeps personal media of the test number off the server
  until the CRM needs media.
- No global webhook: the CRM registers each device with its own `webhook_url` via `POST /devices`.
- `GOWA_BASIC_AUTH` is a long random `user:password`, generated once, stored only in the Portainer
  stack variables and in the CRM configuration. It is never printed or committed.
- Update and rollback: change `GOWA_VERSION` in the stack variables and click "Update the stack"
  (or the same through the API). `stop-first` guarantees two containers never hold the session.

## 5. B3: backup

> **As built (2026-09-24):** `sqlite3` could not be installed (bullseye-security 404); the copy uses the system python3 `sqlite3.Connection.backup` (same online backup API) plus `integrity_check`, then `chown 20001:20000`. The verbatim block and the tested failure path are in elphantcrm `docs/reference/38-gowa.md` §5.

Changes to `/root/backup/bin/backup.sh` on noria:

1. Before the volume loop, for each `*.db` file in the `gowa_storages` volume directory, run
   `sqlite3 <file> ".backup '$TARGET/gateway-<name>.db'"` and `gzip` the result. `.backup` uses
   SQLite's online backup API and is consistent while GOWA writes.
2. Add `gowa_storages` and `gowa_statics` to `VOLUMES` (the raw `tar` stays as a secondary
   copy; restores use the `.backup` files).
3. Prerequisite: `apt install sqlite3` on the host (that package only; no `apt upgrade`).
4. Verification in the script: `sqlite3 <backup> "PRAGMA integrity_check"` must print `ok`, in
   line with the existing `pg_restore -l` / `gzip -t` checks.

The existing encrypted upload to R2 picks up the new files without changes.

## 6. B4: move the lab test instance

Preconditions: the 24-hour observation has ended (2026-09-25 18:45, Brasília), the owner has
stopped their lab process, and the owner approves the move. Only one process may hold the
session at any time.

1. Deploy the stack with an empty volume. Check `https://devias.elphant.com.br/health` returns
   `OK`, the certificate is valid, `/statics/...` returns 404, and the API asks for Basic Auth.
2. Scale the service to `replicas: 0`.
3. With the lab process stopped, copy every file in the lab `run/storages/` (databases and their
   `-wal`/`-shm` files) into the `gowa_storages` volume directory with `scp`, owned by
   `20001:20000` (the image's `gowauser:gowa`).
4. Scale back to `replicas: 1`.
5. Expected: the device connects without QR; `session.status connected` if a webhook is
   configured; sending and receiving work.
6. Rollback: scale to 0, restart the lab GOWA on the Mac with the files that stayed there.
7. Part A's gated Task 10 (real StreamReplaced) runs here, with the default device attached to a
   named slot, which is the configuration the part A review flagged.

After noria is stable: delete the lab personal data on the Mac (`run/`, `exports/`, `gowa.log`),
unless the owner asks to keep it.

## 7. B5: operations doc

New `docs/reference/38-gowa.md` in elphantcrm (listed in the CRM spec §11):

- Where the fork lives, branch model, and the stack file copy.
- Update flow: sync PR → merge (merge commit) → tag `vX.Y.Z-elphant.N` → image → `GOWA_VERSION`.
- Rollback: previous `GOWA_VERSION`.
- Weekly routine (CRM spec §8.6): check for a sync PR, merge, tag, update.
- Backup location and restore steps from the `.backup` files.
- The rule: never run the same session in two processes.

The CRM spec decisions D2 to D4 and §5.1 are updated to the fork-only model at the same time.

## 8. Verification

| Deliverable | Check |
|---|---|
| B1 sync | Manual `workflow_dispatch` with `elphant` at v9.4.0 (the latest upstream tag): exits without a PR. Then `workflow_dispatch` with `base` = a scratch branch cut from v9.3.1 plus one fork commit: opens `sync/v9.4.0`, and `elphant-ci.yml` runs green on it. Close the PR and delete the scratch branches afterwards |
| B1 setup | The fork's default branch is `elphant`, so the scheduled sync runs (GitHub schedules only default-branch workflows) |
| B1 CI | The push of `elphant` shows the `vet` and `test` checks green |
| B1 image | Tag `v9.4.0-elphant.1`: the GHCR manifest (read with the registry API) lists `linux/amd64` and `linux/arm64`; `docker run --rm ghcr.io/eduardoelphant/gowa:v9.4.0-elphant.1 --help` works on noria (amd64). The Mac has no Docker |
| B2 | Section 6 step 1 checks |
| B3 | Run `backup.sh` once by hand; the gateway `.backup` files exist, pass `integrity_check`, and reach R2 |
| B4 | Section 6 step 5 |

Every step that writes to GitHub, GHCR, Portainer or noria runs only with the owner's explicit
approval at that moment.
