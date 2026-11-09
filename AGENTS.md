# AGENTS.md

Instructions for AI coding agents working in this repository. Tool-neutral: any
agent (OpenCode, Claude Code, Codex, Cursor, others) should follow this file.
Tool-specific files such as `CLAUDE.md` add only tool hooks, not project rules.

## Project

`rss-scraper` is a self-hosted Go service (single binary, Docker image) that
converts RSS/Atom feeds into full-text feeds. Each article is fetched, its main
content extracted (go-readability plus per-site CSS rules), cleaned to minimal
HTML, and served as RSS 2.0 with `content:encoded`.

- Saved feeds: `GET /feed/<token>.xml`. The token is the auth, so readers need no login.
- On-demand: `GET /on-demand?url=...` converts any feed (admin auth required).
- Admin UI and JSON API under `/admin/...` and `/api/...`, behind password or bearer token.
- SQLite storage (pure-Go driver, no cgo), one file under `FTRSS_DATA_DIR`.
- Background scheduler with per-host rate limiting and jitter.

The product spec is `interview/full-text-rss-feed-generator-k7d2f4.md` when present.
If the spec and this file disagree, the spec wins; say so in your summary.

## Layout

```
cmd/rss-scraper/   entrypoint: config, store, server, scheduler
internal/config/   environment-variable config
internal/model/    domain types (Feed, SiteRule, Item)
internal/store/    SQLite data layer + migrations/
internal/feedio/   gofeed parsing, RSS 2.0 + content:encoded output
internal/extract/  rate-limited fetcher, readability + rules, HTML cleanup
internal/scheduler/ per-feed refresh loop
internal/server/   HTTP handlers, auth, API, on-demand
internal/webassets/ embedded templates and static files
scripts/           e2e.sh and fixture site
docs/              UI layout notes
```

## Commands

```sh
go build ./...                 # build everything
go vet ./...                   # static checks
go test -race -count=1 ./...   # unit tests (CI runs this)
sh scripts/e2e.sh              # end-to-end against a locally built binary
docker build -t rss-scraper:dev .   # container image (needs Docker)
```

Run `go vet` and `go test` before reporting a change as done. Run `scripts/e2e.sh`
for changes to HTTP, feed output, extraction, or the scheduler.

## Conventions

- Module path is `rss-scraper`. Imports use `rss-scraper/internal/...`.
- Keep package boundaries: handlers call the store and pipeline, not each other's internals.
- Errors wrap with `%w`. Store methods take `ctx` first.
- Feed output must stay sanitized. Only the bluemonday allowlist in
  `internal/extract` may produce article HTML. Do not emit raw page HTML.
- Environment variables use the `FTRSS_` prefix. Renaming one is a breaking
  config change; document it in the README if you do.
- The SQLite filename `fulltextrss.db` is part of existing deployments. Don't change it casually.
- Templates and static assets live under `internal/webassets/`. Keep their
  `{{define}}` names stable: `internal/webassets/templates/README.md` documents
  the data contract the server codes against.

## Git and pull requests

- Conventional Commits: `type(scope): imperative subject`, lowercase, no period, subject 72 chars or fewer.
  Types: feat, fix, docs, style, refactor, perf, test, build, ci, chore, revert.
- One logical change per commit. Don't mix refactors with features.
- Don't commit build output, logs, or secrets. The `.gitignore` covers `/rss-scraper`,
  `*.db`, and `/data/`.
- `master` is protected. Changes land by pull request, and the required checks
  `test` and `docker-e2e` must pass. Don't push to master directly.
- Auto-merge is for Dependabot PRs only. `dependabot-auto-merge.yml` enables it
  for PRs opened by `dependabot[bot]`, including major bumps, once the required
  checks pass.
- All other PRs (human or agent) are merged by the repo owner. Do not enable
  auto-merge on them, and do not merge them yourself unless the owner asks.
  Opening the PR and reporting its check results is the agent's job.

## CI and publishing

`.github/workflows/ci.yml`:
- `test`: vet and race-enabled unit tests.
- `docker-e2e`: builds the image, runs it, and runs `scripts/e2e.sh` against it.
- `publish`: runs on master pushes and `v*` tags only. Pushes
  `ghcr.io/bottlymozgus/rss-scraper` for linux/amd64 and linux/arm64.

Don't add steps that need secrets to pull-request workflows.

## Boundaries

- Don't change the Go version in `go.mod` or the Docker builder image without
  checking both. The builder must be at least the `go.mod` version, and Dependabot
  manages both.
- Don't disable or skip the e2e check to get a change through.
- Don't add dependencies without a reason. Keep the binary small and the build pure Go.
- Don't store secrets in the repo or in `FTRSS_*` defaults. The example compose
  file uses a placeholder password on purpose.
- Ask before deleting data, changing the database schema destructively, or
  changing the published image name or tags.
