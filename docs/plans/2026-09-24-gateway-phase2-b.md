# Fork Phase 2 Part B Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build and publish the `elphant` branch as a pinned multi-arch image, run it as the `gowa` stack on noria behind Traefik with consistent backups, and move the lab test session there without a new QR.

**Architecture:** Three new GitHub Actions workflows in the fork (sync PR per upstream tag, CI, image on tag), plus a small tested shell script that picks the upstream tag. The Swarm stack file and the operations doc live in the elphantcrm repo (branch `feature/whatsapp-gateway`); Portainer holds the live stack. Backup gains a `sqlite3 .backup` step on noria.

**Tech Stack:** GitHub Actions, Docker Buildx, GHCR, Docker Swarm + Portainer CE 2.33 API, Traefik v3.4, SQLite, bash.

**Spec:** `docs/specs/2026-09-24-gateway-phase2-b-design.md`

## Global Constraints

- Every write to GitHub (push, secret, setting, tag, package visibility), GHCR, Portainer or noria needs the owner's explicit OK at that moment. Tasks marked **[GATED]** start by asking for it. Reads on noria are authorized.
- Never print credentials: the Portainer token, `GOWA_BASIC_AUTH`, `SYNC_TOKEN`, QR content, `.env`. Show names, counts and structure only.
- Fork repo: `eduardoelphant/go-whatsapp-web-multidevice`, branch `elphant`; `main` stays a pure upstream mirror. No PR or issue upstream.
- Fork commits: English, `type(scope): subject`, no attribution line. Fork logic in new files only; no upstream workflow or Dockerfile is edited.
- Image: `ghcr.io/eduardoelphant/gowa:<tag>`, tags `vX.Y.Z-elphant.N`, platforms `linux/amd64` and `linux/arm64`, no `latest`.
- Nothing about noria (host names, IPs, stack file) goes into the public fork.
- elphantcrm work: worktree `~/Documents/elphantcrm-whatsapp-gateway` on the existing local branch `feature/whatsapp-gateway`; docs in Portuguese BR; commits `docs(reference): …` / `docs(specs): …`; no push.
- noria: `ssh root@<gateway-host>`; Portainer `https://<portainer-host>`; Swarm ID `<swarm-id>`; stack `gowa`, service `gowa_gowa`, volumes `gowa_storages` and `gowa_statics`; host `devias.elphant.com.br`.
- Local secrets live in `~/.config/elphant/` with mode 600: `portainer-token` (created by the owner), `gowa-basic-auth` (generated in Task 6).
- Only one process may hold the WhatsApp session at any time. The owner's lab process (`gowa rest --port 3030` on the Mac) is running until the 24-hour observation ends on 2026-09-25 18:45 (Brasília).
- Go commands run from `src/` with `GOTOOLCHAIN=auto`.

## Review Focus

1. **Upstream patch for an older line after a newer release** (e.g. v9.3.2 published after v9.4.0): the sync must still target the highest version, not the newest tag. Test in Task 1 (`older patch tag after newer release`).
2. **A sync PR the owner closed without merging**: the daily run must not reopen it every day. Guard in Task 2 (`gh pr list --state all`), checked in Task 4 step 6.
3. **`/statics` reached by case or slash variants** (`/STATICS/…`, `//statics/…`): Fiber routes case-insensitively, so the Traefik rule must block every variant. Check in Task 6 step 7 with a probe file.
4. **A container that is unhealthy only because WhatsApp is disconnected**: Swarm must not restart it in a loop. `/health` reports service readiness only; Task 6 step 6 checks health with the empty volume (no device connected).
5. **Backup while GOWA writes**: the gateway copies must be consistent and verified, and a failed copy must stop the upload. Task 7 step 5 runs `integrity_check` on the produced files.

---

### Task 1: Upstream tag picker script

**Files:**
- Create: `.github/scripts/elphant-sync-tag.sh`
- Test: `.github/scripts/elphant-sync-tag_test.sh`

**Interfaces:**
- Produces: `bash .github/scripts/elphant-sync-tag.sh <base-ref>` prints the upstream tag to sync (a `vX.Y.Z` tag that `<base-ref>` does not contain yet) or prints nothing. Exit code 0 in both cases. Expects upstream tags fetched into the repository.

- [ ] **Step 1: Write the failing test**

Create `.github/scripts/elphant-sync-tag_test.sh`:

```bash
#!/usr/bin/env bash
# Tests for elphant-sync-tag.sh. Run: bash .github/scripts/elphant-sync-tag_test.sh
set -euo pipefail

script="$(cd "$(dirname "$0")" && pwd)/elphant-sync-tag.sh"
failures=0
repo=""

new_repo() {
	repo=$(mktemp -d)
	git -C "$repo" init -q -b main
	git -C "$repo" config user.email test@example.com
	git -C "$repo" config user.name test
	git -C "$repo" config commit.gpgsign false
	git -C "$repo" config tag.gpgsign false
}

commit() {
	git -C "$repo" commit -q --allow-empty -m "$1"
}

check() {
	local name=$1 base=$2 want=$3 got
	got=$(cd "$repo" && bash "$script" "$base")
	if [ "$got" = "$want" ]; then
		echo "ok   $name"
	else
		echo "FAIL $name: got '$got', want '$want'"
		failures=$((failures + 1))
	fi
}

# Upstream released v1.2.0 and v1.10.0; the fork branched at v1.2.0.
new_repo
commit "upstream 1.2.0"
git -C "$repo" tag v1.2.0
git -C "$repo" branch fork
commit "upstream 1.10.0"
git -C "$repo" tag v1.10.0
commit "upstream 2.0.0 release candidate"
git -C "$repo" tag v2.0.0-rc1
git -C "$repo" checkout -q fork
commit "fork change"
git -C "$repo" tag v1.2.0-elphant.1

check "picks the highest stable tag the base lacks" fork v1.10.0

# Upstream patches the old line after the newer release.
git -C "$repo" checkout -q v1.2.0
commit "upstream 1.2.1"
git -C "$repo" tag v1.2.1
git -C "$repo" checkout -q fork
check "older patch tag after newer release" fork v1.10.0

git -C "$repo" merge -q --no-edit v1.10.0
check "nothing to do once the base contains it" fork ""
rm -rf "$repo"

new_repo
commit "initial"
check "nothing to do without tags" main ""
rm -rf "$repo"

if [ "$failures" -ne 0 ]; then
	echo "$failures failure(s)"
	exit 1
fi
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `bash .github/scripts/elphant-sync-tag_test.sh`
Expected: FAIL lines (the script does not exist: `bash: …/elphant-sync-tag.sh: No such file or directory`) and a non-zero exit.

- [ ] **Step 3: Write the script**

Create `.github/scripts/elphant-sync-tag.sh`:

```bash
#!/usr/bin/env bash
# Prints the upstream release tag the fork still needs to merge, or nothing.
#
# Usage: elphant-sync-tag.sh <base-ref>
#
# Considers only stable upstream tags (vX.Y.Z; fork tags vX.Y.Z-elphant.N and
# release candidates are ignored) and picks the highest version, so a patch
# for an older line never outranks a newer release. Expects the upstream tags
# to be fetched into this repository.
set -euo pipefail

base_ref=${1:?usage: elphant-sync-tag.sh <base-ref>}

latest=$(git tag -l 'v*' | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | sort -V | tail -n 1 || true)
if [ -z "$latest" ]; then
	exit 0
fi

if git merge-base --is-ancestor "$latest^{commit}" "$base_ref"; then
	exit 0
fi

printf '%s\n' "$latest"
```

Then `chmod +x .github/scripts/elphant-sync-tag.sh .github/scripts/elphant-sync-tag_test.sh`.

- [ ] **Step 4: Run the test to verify it passes**

Run: `bash .github/scripts/elphant-sync-tag_test.sh`
Expected: four `ok` lines, exit 0.

- [ ] **Step 5: Commit**

```bash
git add .github/scripts/elphant-sync-tag.sh .github/scripts/elphant-sync-tag_test.sh
git commit -m "ci(elphant): add upstream tag picker for sync PRs"
```

---

### Task 2: Fork workflows (sync, CI, image)

**Files:**
- Create: `.github/workflows/elphant-sync.yml`
- Create: `.github/workflows/elphant-ci.yml`
- Create: `.github/workflows/elphant-image.yml`

**Interfaces:**
- Consumes: `.github/scripts/elphant-sync-tag.sh` and its test (Task 1).
- Produces: secret name `SYNC_TOKEN`; workflow names `Elphant upstream sync`, `Elphant CI`, `Elphant image`; image `ghcr.io/eduardoelphant/gowa:<tag>` with `<tag>-amd64` and `<tag>-arm64` per-arch tags; sync branches `sync/vX.Y.Z`.

- [ ] **Step 1: Write `elphant-ci.yml`**

```yaml
name: Elphant CI

on:
  push:
    branches: [elphant]
  pull_request:
    branches: [elphant]

permissions:
  contents: read

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7
      - uses: actions/setup-go@v7
        with:
          go-version-file: src/go.mod
          cache-dependency-path: src/go.sum
      - name: Vet
        working-directory: src
        run: go vet ./...
      - name: Test
        working-directory: src
        run: go test ./...
      - name: Sync script tests
        run: bash .github/scripts/elphant-sync-tag_test.sh
```

- [ ] **Step 2: Write `elphant-sync.yml`**

```yaml
name: Elphant upstream sync

on:
  schedule:
    - cron: "0 9 * * *"
  workflow_dispatch:
    inputs:
      base:
        description: "Branch that receives the sync PR"
        required: false
        default: elphant

permissions:
  contents: read

jobs:
  sync:
    runs-on: ubuntu-latest
    env:
      BASE: ${{ inputs.base || 'elphant' }}
      UPSTREAM: https://github.com/aldinokemal/go-whatsapp-web-multidevice
    steps:
      # SYNC_TOKEN (fine-grained, this fork only: Contents, Pull requests and
      # Workflows read/write). GITHUB_TOKEN cannot push upstream commits that
      # touch .github/workflows, and PRs it opens do not trigger Elphant CI.
      - uses: actions/checkout@v7
        with:
          ref: ${{ env.BASE }}
          fetch-depth: 0
          token: ${{ secrets.SYNC_TOKEN }}

      - name: Fetch upstream tags
        run: git fetch --no-tags "$UPSTREAM.git" '+refs/tags/*:refs/tags/*'

      - name: Pick the tag to sync
        id: pick
        env:
          GH_TOKEN: ${{ secrets.SYNC_TOKEN }}
        run: |
          tag=$(bash .github/scripts/elphant-sync-tag.sh "origin/$BASE")
          if [ -n "$tag" ]; then
            existing=$(gh pr list --head "sync/$tag" --state all --json number --jq 'length')
            if [ "$existing" != "0" ]; then
              echo "A PR for sync/$tag already exists (open or closed); nothing to do."
              tag=""
            fi
          fi
          echo "tag=$tag" >> "$GITHUB_OUTPUT"

      - name: Open the sync PR
        if: steps.pick.outputs.tag != ''
        env:
          TAG: ${{ steps.pick.outputs.tag }}
          GH_TOKEN: ${{ secrets.SYNC_TOKEN }}
        run: |
          git push --force origin "$(git rev-parse "$TAG^{commit}"):refs/heads/sync/$TAG"
          gh pr create \
            --base "$BASE" \
            --head "sync/$TAG" \
            --title "sync: upstream $TAG" \
            --body "Upstream release: $UPSTREAM/releases/tag/$TAG

          Merge with **Create a merge commit**. Never squash or rebase: the fork keeps the upstream history so every published image stays reproducible."
```

- [ ] **Step 3: Write `elphant-image.yml`**

```yaml
name: Elphant image

on:
  push:
    tags:
      - "v*-elphant.*"
  workflow_dispatch:
    inputs:
      tag:
        description: "Existing fork tag to build (e.g. v9.4.0-elphant.1)"
        required: true

permissions:
  contents: read
  packages: write

env:
  IMAGE: ghcr.io/eduardoelphant/gowa
  TAG: ${{ inputs.tag || github.ref_name }}

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7
        with:
          ref: ${{ env.TAG }}
      - uses: actions/setup-go@v7
        with:
          go-version-file: src/go.mod
          cache-dependency-path: src/go.sum
      - name: Vet and test
        working-directory: src
        run: |
          go vet ./...
          go test ./...

  build:
    needs: test
    strategy:
      matrix:
        include:
          - arch: amd64
            runner: ubuntu-latest
          - arch: arm64
            runner: ubuntu-24.04-arm
    runs-on: ${{ matrix.runner }}
    steps:
      - uses: actions/checkout@v7
        with:
          ref: ${{ env.TAG }}
      - uses: docker/login-action@v4
        with:
          registry: ghcr.io
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}
      - uses: docker/setup-buildx-action@v4
      - uses: docker/build-push-action@v7
        with:
          context: .
          file: ./docker/golang.Dockerfile
          platforms: linux/${{ matrix.arch }}
          push: true
          provenance: false
          tags: ${{ env.IMAGE }}:${{ env.TAG }}-${{ matrix.arch }}

  manifest:
    needs: build
    runs-on: ubuntu-latest
    steps:
      - uses: docker/login-action@v4
        with:
          registry: ghcr.io
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}
      - uses: docker/setup-buildx-action@v4
      - name: Create the multi-arch manifest
        run: docker buildx imagetools create -t "$IMAGE:$TAG" "$IMAGE:$TAG-amd64" "$IMAGE:$TAG-arm64"
```

- [ ] **Step 4: Lint the workflows**

Run: `cd src && GOTOOLCHAIN=auto go run github.com/rhysd/actionlint/cmd/actionlint@latest ../.github/workflows/elphant-*.yml`
Expected: no output, exit 0. (Downloads actionlint into the Go module cache; nothing is written to the repo.)

Also confirm the upstream workflows ignore fork tags:
Run: `grep -n 'v\[0-9\]' .github/workflows/build-docker-image.yaml .github/workflows/release.yml`
Expected: both filter on `"v[0-9]+.[0-9]+.[0-9]+"` only (a `vX.Y.Z-elphant.N` tag does not match the whole pattern).

- [ ] **Step 5: Commit**

```bash
git add .github/workflows/elphant-sync.yml .github/workflows/elphant-ci.yml .github/workflows/elphant-image.yml
git commit -m "ci(elphant): add sync, test and image workflows"
```

---

### Task 3: Stack file, operations doc and CRM spec update (elphantcrm)

**Files (in `~/Documents/elphantcrm-whatsapp-gateway`, branch `feature/whatsapp-gateway`):**
- Create: `docs/reference/38-gowa.md`
- Modify: `docs/specs/2026-09-24-whatsapp-gateway.spec.md` (decisions D2, D3, D4; §5.1 heading and G10; §5.2; §8.5 "candidata a PR"; §8.6; §11 last bullet)

**Interfaces:**
- Produces: the stack YAML as the first ```` ```yaml ```` block of `38-gowa.md` (Task 6 extracts it from there).

- [ ] **Step 1: Create the worktree**

Run: `git -C ~/Documents/elphantcrm worktree add ~/Documents/elphantcrm-whatsapp-gateway feature/whatsapp-gateway`
Expected: `Preparing worktree (checking out 'feature/whatsapp-gateway')`. Read `~/Documents/elphantcrm-whatsapp-gateway/CLAUDE.md` sections on docs and commits before editing.

- [ ] **Step 2: Write `docs/reference/38-gowa.md`**

````markdown
# Gateway WhatsApp não oficial (GOWA)

Operação do gateway que atende o provider `whatsapp_gowa`. Desenho e decisões:
`docs/specs/2026-09-24-whatsapp-gateway.spec.md`.

## 1. Onde fica

- **Código:** fork público `eduardoelphant/go-whatsapp-web-multidevice`. `main` é espelho do
  upstream (`aldinokemal/go-whatsapp-web-multidevice`); a branch `elphant` (padrão do fork) é a
  última tag do upstream mais os commits do fork. Nada vai para o upstream (modelo "só fork").
  As diferenças do fork estão em `docs/reference/elphant-fork.md` do próprio fork.
- **Imagem:** `ghcr.io/eduardoelphant/gowa:vX.Y.Z-elphant.N` (amd64 e arm64, pública, sem
  `latest`).
- **Produção:** stack `gowa` no Swarm do `noria` (Portainer em `<portainer-host>`),
  serviço `gowa_gowa`, em `https://devias.elphant.com.br`. A stack dentro do Portainer é a
  fonte da verdade; o arquivo abaixo é a cópia revisada.

## 2. Stack

Variáveis da stack no Portainer: `GOWA_VERSION` (tag da imagem) e `GOWA_BASIC_AUTH`
(`usuário:senha`, aleatória; só no Portainer e na configuração do CRM, nunca em arquivo).

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

Por que cada escolha:

- `replicas: 1` e `stop-first`: dois containers com a mesma sessão brigam (StreamReplaced).
- `/health` só diz que o serviço subiu, não que o WhatsApp está conectado; device desconectado
  não faz o Swarm reiniciar o container.
- `/statics` (QR e mídia) é servido pelo GOWA antes do Basic Auth. A regra do Traefik bloqueia o
  caminho sem diferenciar maiúsculas e com barras repetidas, porque o Fiber aceita `/STATICS`.
- `MCP_ENABLED` vem ligado por padrão no GOWA; fica desligado.
- `WHATSAPP_AUTO_DOWNLOAD_MEDIA=false` até o CRM precisar de mídia (G4).
- Sem webhook global: o CRM cadastra cada device com o próprio `webhook_url` em `POST /devices`.

## 3. Atualizar

1. Todo dia às 09:00 UTC a Action `Elphant upstream sync` abre um PR `sync/vX.Y.Z → elphant`
   quando sai tag nova no upstream. A Action `Elphant CI` roda vet e testes no PR.
2. Com o PR verde, merge com **Create a merge commit** (nunca squash nem rebase). Conflito:
   resolver localmente na branch `sync/vX.Y.Z` fazendo merge da `elphant` nela.
3. Criar a tag `vX.Y.Z-elphant.N` no merge (N começa em 1 e sobe a cada imagem da mesma base).
   A Action `Elphant image` roda os testes e publica a imagem.
4. No Portainer, stack `gowa`, trocar `GOWA_VERSION` e "Update the stack".
5. Conferir `https://devias.elphant.com.br/health` e o log do serviço.

Rotina semanal (spec §8.6): ver se há PR de sync aberto e seguir os passos acima.

## 4. Voltar versão

No Portainer, `GOWA_VERSION` com a tag anterior e "Update the stack". Toda tag publicada continua
existindo no GHCR e no fork (sem force-push na `elphant`).

## 5. Backup e restauração

`/root/backup/bin/backup.sh` (diário, 03:30) grava em `/root/backup/daily/<data>/`:

- `gowa-whatsapp.db.gz` e `gowa-chatstorage.db.gz`: cópias consistentes feitas com
  `sqlite3 .backup` com o GOWA no ar, conferidas com `PRAGMA integrity_check`.
- `volume-gowa_storages.tar.gz` e `volume-gowa_statics.tar.gz`: cópia bruta secundária.

Tudo sobe cifrado para o R2 (`elphant-backups/noria/<data>`).

Restaurar: `docker service scale gowa_gowa=0`; em
`/var/lib/docker/volumes/gowa_storages/_data/`, apagar `whatsapp.db*` e `chatstorage.db*` e
descompactar as cópias `gowa-*.db.gz` com os nomes `whatsapp.db` e `chatstorage.db`;
`chown 20001:20000` nos arquivos; `docker service scale gowa_gowa=1`.

## 6. Regras

- Nunca rodar a mesma sessão em dois processos (duas réplicas, dois gateways, Evolution e GOWA).
- Sessão derrubada por StreamReplaced fica parada até reconexão manual
  (`POST /devices/:device_id/reconnect`); não reinicia sozinha.
- Toda escrita no `noria`, no Portainer, no GitHub ou no GHCR pede OK do dono.
````

- [ ] **Step 3: Update the CRM spec**

In `docs/specs/2026-09-24-whatsapp-gateway.spec.md`, make these replacements (Portuguese BR, keep the table format):

- Header `**Status:**` line: keep; add below it `**Atualização 2026-09-24:** o gateway passou ao modelo "só fork" (sem PR nem issue no upstream) e o gw.sh foi adiado; detalhes nas decisões D2, D3, D4 e D10.`
- D2 row, decision column: `Só fork: o fork é o produto; nenhuma issue ou PR vai para o upstream`; alternatives: `Primeiro no upstream (issue de desenho e PRs pequenos)`; reason: `Decisão do dono em 2026-09-24: não depender do calendário do mantenedor nem negociar desenho. Custo: cada commit do fork vira rebase/merge permanente; mitigado com código do fork em arquivo novo e gancho mínimo nos arquivos do upstream`.
- D3 row, decision column: `Fork público eduardoelphant/go-whatsapp-web-multidevice; branch elphant (padrão do fork) = última tag do upstream + commits do fork, sincronizada por PR com merge commit a cada tag`.
- D4 row, decision column: `Mudança no gateway aditiva (campo ou evento novo) sempre que possível; flag opt-in deixa de ser obrigatória`.
- D10 row, decision column: `Stack gowa criada pela API do Portainer; update e rollback trocando GOWA_VERSION nas variáveis da stack. gw.sh adiado até existir um segundo servidor`.
- §5.1 heading: `### 5.1 Gateway (fork, branch elphant)`; G10 row item: `Infra do fork: branch elphant, PR de sync diário, CI, imagem multi-arch no GHCR; HEALTHCHECK na stack`.
- §5.2: add at the top `> **Adiado (2026-09-24):** com um servidor só, a stack no Portainer cobre instalação, update e rollback. Operação em docs/reference/38-gowa.md.`
- §8.5, text `candidata a PR genérico para o GOWA`: replace with `mantida só no fork`.
- §8.6 last sentence: `Rotina semanal: conferir o PR de sync do fork, fazer merge, criar a tag -elphant.N e trocar GOWA_VERSION; se falhar, voltar GOWA_VERSION (docs/reference/38-gowa.md).`
- §11 last bullet: `Novo docs/reference/38-gowa.md (criado em 2026-09-24).`

- [ ] **Step 4: Validate the stack file on noria (read-only render)**

Run:
```bash
python3 - <<'EOF' | ssh root@<gateway-host> 'GOWA_VERSION=v0.0.0-check GOWA_BASIC_AUTH=user:check docker stack config -c - > /dev/null && echo stack-config-ok'
import re
doc = open("/Users/eduardocarlos/Documents/elphantcrm-whatsapp-gateway/docs/reference/38-gowa.md").read()
print(re.search(r"```yaml\n(.*?)```", doc, re.S).group(1))
EOF
```
Expected: `stack-config-ok`. (`docker stack config` only renders; nothing is deployed or written.)

- [ ] **Step 5: Commit (elphantcrm, local only)**

```bash
cd ~/Documents/elphantcrm-whatsapp-gateway
git add docs/reference/38-gowa.md docs/specs/2026-09-24-whatsapp-gateway.spec.md
git commit -m "docs(reference): operação do gateway GOWA e modelo só fork"
```

---

### Task 4 [GATED]: Publish the branch and enable the fork CI

**Files:** none (GitHub state).

**Interfaces:**
- Consumes: branch `elphant` with Tasks 1-2 committed; `SYNC_TOKEN` created by the owner.

- [ ] **Step 1: Ask the owner for OK**, listing exactly: push `elphant` to `origin`; owner sets the default branch to `elphant`, enables Actions, creates the fine-grained token (repository `eduardoelphant/go-whatsapp-web-multidevice` only; Contents, Pull requests, Workflows: read and write; 1-year expiry) and saves it as the Actions secret `SYNC_TOKEN`.

- [ ] **Step 2: Push**

Run: `git push -u origin elphant`
Expected: branch created on `origin`.

- [ ] **Step 3: Owner actions in the GitHub UI** (Settings → General → Default branch = `elphant`; Settings → Actions → Allow all actions; Settings → Secrets and variables → Actions → `SYNC_TOKEN`). Then verify:

Run: `gh repo view eduardoelphant/go-whatsapp-web-multidevice --json defaultBranchRef --jq .defaultBranchRef.name && gh secret list --repo eduardoelphant/go-whatsapp-web-multidevice`
Expected: `elphant`, and a `SYNC_TOKEN` line (value never shown).

- [ ] **Step 4: CI on the push**

Run: `gh run list --repo eduardoelphant/go-whatsapp-web-multidevice --workflow "Elphant CI" --limit 1` (re-run the push with `gh workflow run "Elphant CI" --ref elphant` if the push predates enabling Actions), then `gh run watch <id> --repo eduardoelphant/go-whatsapp-web-multidevice`.
Expected: `completed success`.

- [ ] **Step 5: Sync with nothing to do**

Run: `gh workflow run "Elphant upstream sync" --repo eduardoelphant/go-whatsapp-web-multidevice --ref elphant`, then watch the run.
Expected: success; no PR opened (`elphant` already contains v9.4.0, the latest upstream tag).

- [ ] **Step 6: Sync against a scratch base (with OK)**

```bash
git switch -c scratch/sync-test v9.3.1
git cherry-pick <Task 1 commit>   # the scratch base needs the picker script
git push origin scratch/sync-test
gh workflow run "Elphant upstream sync" --repo eduardoelphant/go-whatsapp-web-multidevice --ref elphant -f base=scratch/sync-test
```
Expected: a PR `sync: upstream v9.4.0` from `sync/v9.4.0` into `scratch/sync-test`; `Elphant CI` does not run on it (its trigger is PRs into `elphant`), which is fine for this check. Run the workflow again: it prints `A PR for sync/v9.4.0 already exists (open or closed); nothing to do.` Close the PR, then clean up:

```bash
gh pr close sync/v9.4.0 --repo eduardoelphant/go-whatsapp-web-multidevice
git push origin --delete scratch/sync-test sync/v9.4.0
git switch elphant && git branch -D scratch/sync-test
```

Ruling to record: after this cleanup a closed PR for `sync/v9.4.0` remains in history, so the guard would skip a future real v9.4.0 sync; that cannot happen because `elphant` already contains v9.4.0.

---

### Task 5 [GATED]: First image

**Files:** none.

**Interfaces:**
- Produces: `ghcr.io/eduardoelphant/gowa:v9.4.0-elphant.1` (Task 6 deploys it).

- [ ] **Step 1: Ask for OK**: create and push tag `v9.4.0-elphant.1` on the current `elphant` head, then make the `gowa` package public.

- [ ] **Step 2: Tag and push**

```bash
git tag -a v9.4.0-elphant.1 -m "v9.4.0-elphant.1"
git push origin v9.4.0-elphant.1
```
Then watch `Elphant image` with `gh run watch`.
Expected: `test`, both `build` jobs and `manifest` succeed.

- [ ] **Step 3: Owner makes the package public** (GitHub → Packages → `gowa` → Package settings → Change visibility → Public).

- [ ] **Step 4: Check the manifest anonymously**

```bash
token=$(curl -s "https://ghcr.io/token?scope=repository:eduardoelphant/gowa:pull" | python3 -c 'import json,sys; print(json.load(sys.stdin)["token"])')
curl -s -H "Authorization: Bearer $token" \
  -H "Accept: application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json" \
  https://ghcr.io/v2/eduardoelphant/gowa/manifests/v9.4.0-elphant.1 \
  | python3 -c 'import json,sys; print(sorted(m["platform"]["architecture"] for m in json.load(sys.stdin)["manifests"]))'
```
Expected: `['amd64', 'arm64']` (an anonymous pull token proves the package is public).

- [ ] **Step 5: Run it on noria (amd64)**

Run: `ssh root@<gateway-host> 'docker pull -q ghcr.io/eduardoelphant/gowa:v9.4.0-elphant.1 && docker run --rm --entrypoint /app/whatsapp ghcr.io/eduardoelphant/gowa:v9.4.0-elphant.1 --help | head -5'`
Expected: the CLI usage text.

---

### Task 6 [GATED]: Deploy the `gowa` stack

**Files:** `~/.config/elphant/gowa-basic-auth` (local, 600).

**Interfaces:**
- Consumes: stack YAML (Task 3), image tag (Task 5), `~/.config/elphant/portainer-token` (owner).
- Produces: running service `gowa_gowa`; volumes `gowa_storages`, `gowa_statics`.

- [ ] **Step 1: Ask for OK** to create the stack on noria through the Portainer API. The owner creates a Portainer access token (My account → Access tokens) and saves it: `umask 077; mkdir -p ~/.config/elphant; pbpaste > ~/.config/elphant/portainer-token`.

- [ ] **Step 2: Generate the gateway credential**

```bash
umask 077
printf 'crm:%s' "$(openssl rand -hex 24)" > ~/.config/elphant/gowa-basic-auth
wc -c < ~/.config/elphant/gowa-basic-auth
```
Expected: `52`. The owner copies it into the password manager; it is never printed.

- [ ] **Step 3: Find the Swarm endpoint id**

```bash
curl -s -H "X-API-Key: $(cat ~/.config/elphant/portainer-token)" https://<portainer-host>/api/endpoints \
  | python3 -c 'import json,sys; [print(e["Id"], e["Name"], e.get("Type")) for e in json.load(sys.stdin)]'
```
Expected: one line per environment; note the Id of the noria Swarm environment (`ENDPOINT_ID`).

- [ ] **Step 4: Create the stack**

```bash
ENDPOINT_ID=<from step 3>
python3 - "$ENDPOINT_ID" <<'EOF'
import json, os, re, sys, urllib.request
doc = open(os.path.expanduser("~/Documents/elphantcrm-whatsapp-gateway/docs/reference/38-gowa.md")).read()
stack = re.search(r"```yaml\n(.*?)```", doc, re.S).group(1)
token = open(os.path.expanduser("~/.config/elphant/portainer-token")).read().strip()
auth = open(os.path.expanduser("~/.config/elphant/gowa-basic-auth")).read().strip()
body = json.dumps({
    "name": "gowa",
    "swarmID": "<swarm-id>",
    "stackFileContent": stack,
    "env": [
        {"name": "GOWA_VERSION", "value": "v9.4.0-elphant.1"},
        {"name": "GOWA_BASIC_AUTH", "value": auth},
    ],
}).encode()
req = urllib.request.Request(
    f"https://<portainer-host>/api/stacks/create/swarm/string?endpointId={sys.argv[1]}",
    data=body, method="POST",
    headers={"X-API-Key": token, "Content-Type": "application/json"},
)
with urllib.request.urlopen(req) as resp:
    created = json.load(resp)
print("stack", created["Id"], created["Name"])
EOF
```
Expected: `stack <id> gowa`.

- [ ] **Step 5: Service running and healthy**

Run: `ssh root@<gateway-host> 'docker service ps gowa_gowa --format "{{.CurrentState}} {{.Error}}" | head -3; docker ps --filter name=gowa_gowa --format "{{.Status}}"'`
Expected: `Running …` with no error; container status `Up … (healthy)` after ~60 s.

- [ ] **Step 6: Public endpoint checks (no device connected yet)**

```bash
curl -s https://devias.elphant.com.br/health; echo
curl -s -o /dev/null -w '%{http_code}\n' https://devias.elphant.com.br/devices
curl -s -o /dev/null -w '%{http_code}\n' -u "$(cat ~/.config/elphant/gowa-basic-auth)" https://devias.elphant.com.br/devices
curl -sv https://devias.elphant.com.br/health 2>&1 | grep -i 'issuer'
```
Expected: `OK`; `401`; `200`; issuer Let's Encrypt. The container stays healthy with no device (Review Focus 4).

- [ ] **Step 7: `/statics` variants are blocked**

```bash
ssh root@<gateway-host> 'echo probe > /var/lib/docker/volumes/gowa_statics/_data/probe.txt'
for p in /statics/probe.txt /Statics/probe.txt /STATICS/probe.txt //statics/probe.txt; do
  printf '%s %s\n' "$p" "$(curl -s -o /dev/null -w '%{http_code}' "https://devias.elphant.com.br$p")"
done
ssh root@<gateway-host> 'rm /var/lib/docker/volumes/gowa_statics/_data/probe.txt'
```
Expected: every line ends in `404` (Traefik has no route). Any `200` is a finding: stop and fix the rule before continuing.

---

### Task 7 [GATED]: Consistent backup of the gateway

> **As executed:** python3 `Connection.backup` replaced the `sqlite3` CLI (see ledger ruling); the production block is recorded verbatim in elphantcrm `docs/reference/38-gowa.md` §5.

**Files (noria):** `/root/backup/bin/backup.sh` (backup copy `/root/backup/bin/backup.sh.bak-2026-09-24`).

**Interfaces:**
- Consumes: volume `gowa_storages` with `whatsapp.db` and `chatstorage.db` (created by Task 6's first start).

- [ ] **Step 1: Ask for OK**: `apt install sqlite3` on noria (this package only) and the edit to `backup.sh`.

- [ ] **Step 2: Install and keep a copy of the script**

Run: `ssh root@<gateway-host> 'apt-get install -y --no-install-recommends sqlite3 >/dev/null && sqlite3 --version && cp -p /root/backup/bin/backup.sh /root/backup/bin/backup.sh.bak-2026-09-24'`
Expected: a `3.x` version line.

- [ ] **Step 3: Edit the script**

Change the `VOLUMES` line to:

```bash
VOLUMES=(evolution_instances evolution_redis portainer_data volume_swarm_certificates gowa_storages gowa_statics)
```

Insert right before `for volume in "${VOLUMES[@]}"; do`:

```bash
# Bancos SQLite do GOWA: cópia consistente pela API de backup online do
# SQLite, que funciona com o app gravando. O tar do volume, logo abaixo, fica como
# cópia secundária; a restauração usa estes arquivos.
gowa_dir=/var/lib/docker/volumes/gowa_storages/_data
if [ -d "$gowa_dir" ]; then
    for db in "$gowa_dir"/*.db; do
        [ -e "$db" ] || continue
        name=$(basename "$db" .db)
        sqlite3 -cmd ".timeout 10000" "$db" ".backup '$TARGET/gowa-$name.db'"
        if [ "$(sqlite3 "$TARGET/gowa-$name.db" 'PRAGMA integrity_check')" != "ok" ]; then
            log "ERRO: cópia do banco $name do GOWA falhou no integrity_check, envio cancelado"
            exit 1
        fi
        gzip "$TARGET/gowa-$name.db"
        log "gowa $name: $(du -h "$TARGET/gowa-$name.db.gz" | cut -f1)"
    done
fi

```

Apply it with a small Python edit over ssh (read the file, assert each anchor appears once, replace, write), then `bash -n /root/backup/bin/backup.sh`.
Expected: `bash -n` prints nothing.

- [ ] **Step 4: Show the diff**

Run: `ssh root@<gateway-host> 'diff -u /root/backup/bin/backup.sh.bak-2026-09-24 /root/backup/bin/backup.sh'`
Expected: only the `VOLUMES` line and the inserted block.

- [ ] **Step 5: Run once and verify**

Run: `ssh root@<gateway-host> '/root/backup/bin/backup.sh 2>&1 | tail -15; d=/root/backup/daily/$(date +%F); ls -1 $d | grep gowa; for f in $d/gowa-*.db.gz; do gunzip -c $f > /tmp/check.db && sqlite3 /tmp/check.db "PRAGMA integrity_check"; done; rm -f /tmp/check.db'`
Expected: log lines `gowa whatsapp: …`, `gowa chatstorage: …`, `verificação ok`, `envio ao R2 ok`; files `gowa-chatstorage.db.gz`, `gowa-whatsapp.db.gz`, `volume-gowa_statics.tar.gz`, `volume-gowa_storages.tar.gz`; two `ok` lines.

---

### Task 8 [GATED]: Move the lab test instance to noria

**Files:** none in repos. Lab: `~/Documents/whatsapp-gateway-lab/run/storages/`.

**Interfaces:**
- Consumes: running stack (Task 6), `sqlite3` on noria (Task 7).

- [ ] **Step 1: Preconditions** — current time is after 2026-09-25 18:45 (Brasília); the owner confirms the lab process is stopped:

Run: `pgrep -fl "gowa rest --port 3030" || echo stopped`
Expected: `stopped`. Ask for OK to move the session.

- [ ] **Step 2: Stop the gateway and look at what will be overwritten**

Run: `ssh root@<gateway-host> 'docker service scale -d gowa_gowa=0 >/dev/null; sleep 10; docker ps --filter name=gowa_gowa -q | wc -l; cd /var/lib/docker/volumes/gowa_storages/_data && ls -1 && sqlite3 chatstorage.db "select count(*) from devices" && sqlite3 whatsapp.db "select count(*) from whatsmeow_device"'`
Expected: `0` containers; `0` devices and `0` whatsmeow devices (fresh databases from Task 6, safe to replace).

- [ ] **Step 3: Copy the lab files**

```bash
cd ~/Documents/whatsapp-gateway-lab/run/storages
ssh root@<gateway-host> 'cd /var/lib/docker/volumes/gowa_storages/_data && rm -f whatsapp.db whatsapp.db-* chatstorage.db chatstorage.db-*'
scp -q whatsapp.db* chatstorage.db* root@<gateway-host>:/var/lib/docker/volumes/gowa_storages/_data/
ssh root@<gateway-host> 'cd /var/lib/docker/volumes/gowa_storages/_data && chown 20001:20000 whatsapp.db* chatstorage.db* && sqlite3 whatsapp.db "select count(*) from whatsmeow_device"'
```
Expected: `1`.

- [ ] **Step 4: Start and confirm the session**

Run: `ssh root@<gateway-host> 'docker service scale -d gowa_gowa=1 >/dev/null; sleep 40; docker service logs --since 2m gowa_gowa 2>&1 | grep -ciE "Successfully authenticated|Connected"; docker service logs --since 2m gowa_gowa 2>&1 | grep -ciE "failed to decrypt|bad mac|no session|device_removed|StreamReplaced"'`
Expected: first count ≥ 1, second count `0`. Then `curl -s -u "$(cat ~/.config/elphant/gowa-basic-auth)" https://devias.elphant.com.br/devices | python3 -c 'import json,sys; print([(d.get("device"), d.get("state")) for d in json.load(sys.stdin)["results"]])'` shows the device logged in. The owner sends and receives one message from the phone to confirm.

- [ ] **Step 5: Rollback path (only if step 4 fails)**

`ssh root@<gateway-host> 'docker service scale -d gowa_gowa=0'`, then the owner restarts the lab GOWA on the Mac with the unchanged files in `run/storages/` (the command is in the project memory).

---

### Task 9 [GATED]: StreamReplaced on noria (part A Task 10, adapted)

**Risk to state before asking:** during the test two processes hold the same session for about 2 minutes. Messages that reach the second process in that window are acknowledged by WhatsApp there and will not reach noria. Keep the phone quiet during the window. The test keeps noria's state and discards the Mac copy.

- [ ] **Step 1: Ask for OK.**
- [ ] **Step 2: Take a consistent copy to the Mac**

```bash
ssh root@<gateway-host> 'cd /var/lib/docker/volumes/gowa_storages/_data && mkdir -p /root/sr-test && for n in whatsapp chatstorage; do sqlite3 -cmd ".timeout 10000" $n.db ".backup /root/sr-test/$n.db"; done'
mkdir -p ~/Documents/whatsapp-gateway-lab/run-sr/storages
scp -q root@<gateway-host>:/root/sr-test/*.db ~/Documents/whatsapp-gateway-lab/run-sr/storages/
ssh root@<gateway-host> 'rm -rf /root/sr-test'
```

- [ ] **Step 3: Start the Mac copy for ~2 minutes** (binary built from `elphant`):

`cd ~/Documents/whatsapp-gateway-lab/run-sr && ../gowa-elphant rest --port 3032 --db-uri "file:storages/whatsapp.db?_foreign_keys=on" --auto-download-media=false > gowa-sr.log 2>&1` in the background; stop it after 2 minutes.

Expected on noria: `docker service logs --since 3m gowa_gowa 2>&1 | grep -c STREAM_REPLACED` is `1`; `docker ps --filter name=gowa_gowa` still shows the container running (the process did not exit).

- [ ] **Step 4: No reconnect fight**

Wait 6 minutes after the Mac copy stops. Run: `ssh root@<gateway-host> 'docker service logs --since 10m gowa_gowa 2>&1 | grep -ciE "STREAM_REPLACED"'`
Expected: `1` (no second replacement: the 5-minute checker left the device alone).

- [ ] **Step 5: Reconnect noria and discard the Mac copy**

`curl -s -X POST -u "$(cat ~/.config/elphant/gowa-basic-auth)" https://devias.elphant.com.br/devices/<device_id>/reconnect`; confirm the device is logged in again; `rm -rf ~/Documents/whatsapp-gateway-lab/run-sr`.

---

### Task 10 [GATED]: Clean up the lab personal data

- [ ] **Step 1: Ask for OK** once noria has run stably for at least 24 hours.
- [ ] **Step 2: Delete** `~/Documents/whatsapp-gateway-lab/run/`, `~/Documents/whatsapp-gateway-lab/exports/` and any `gowa*.log` there. Keep the scripts (`export-evolution-session.sh`) unless the owner says otherwise.

Run: `ls ~/Documents/whatsapp-gateway-lab/`
Expected: no `run`, no `exports`, no logs.
