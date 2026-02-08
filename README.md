# Slack Codex Runner

Slack-first service to run Codex jobs against GitHub repositories with human approvals before risky actions and PR creation.

## Overview

This app exposes Slack endpoints (`/slack/commands`, `/slack/interactions`, `/slack/events`) and orchestrates jobs with these states:

- `queued`
- `running`
- `needs_input`
- `needs_approval`
- `needs_review`
- `done`
- `failed`
- `aborted`

Each job uses an isolated workspace under `WORKSPACE_ROOT` and stores state in `DB_PATH`.

## Prerequisites

- Go 1.25.4+
- `git` binary available
- Codex CLI installed and reachable as `CODEX_COMMAND` (default: `codex exec`)
- Slack app with bot token + signing secret
- GitHub token (or GitHub App installation token) with repo write + PR permissions

## Slack Setup

1. Create Slack app.
2. Enable Slash Commands:
- command: `/codex`
- request URL: `https://<your-host>/slack/commands`
3. Enable Interactivity:
- request URL: `https://<your-host>/slack/interactions`
4. Enable Event Subscriptions:
- request URL: `https://<your-host>/slack/events`
- subscribe to `message.channels` and/or `message.groups`.
5. Bot token scopes (minimum):
- `chat:write`
- `commands`
- `channels:history` (if you need thread input via events)
- `groups:history` (if private channels)
6. Install app into workspace.

## GitHub Setup

1. Use GitHub App or token with permissions:
- Contents: Read/Write
- Pull requests: Read/Write
2. Export token to `GITHUB_TOKEN`.
3. Repositories used by `/codex repo=org/repo ...` must be accessible by that token.

## Configuration

| Variable | Required | Default | Description |
|---|---|---|---|
| `ADDR` | no | `:8080` | HTTP listen address |
| `DATA_DIR` | no | `/data` | Persistent data root |
| `DB_PATH` | no | `/data/state.db` | State file path |
| `WORKSPACE_ROOT` | no | `/data/workspaces` | Per-job clone/workspace root |
| `SLACK_BOT_TOKEN` | yes | - | Slack bot token |
| `SLACK_SIGNING_SECRET` | no | empty | Slack request signature validation |
| `CODEX_COMMAND` | no | `codex exec` | Command used to run Codex in non-interactive mode |
| `OPENAI_API_KEY` | yes | - | API key used by Codex CLI |
| `AGENT_OUTPUT_MODE` | no | `structured` | `structured` enforces JSON contract; `raw` keeps plain output |
| `AGENT_OUTPUT_SCHEMA_VERSION` | no | `v1` | Structured output schema version |
| `SLACK_LOG_MODE` | no | `summary` | `summary` sends human-readable summaries; `stream` sends raw line-by-line logs |
| `CODEX_TIMEOUT` | no | `30m` | Max Codex run time |
| `RUNNER_INACTIVITY_TIMEOUT` | no | `45s` | Idle timeout before `needs_input` |
| `MAX_CONCURRENT_JOBS` | no | `2` | Worker concurrency |
| `GITHUB_TOKEN` | yes | - | Token for clone/push/PR |
| `GITHUB_API_BASE_URL` | no | `https://api.github.com` | GitHub API base URL |
| `DEFAULT_BASE_BRANCH` | no | `main` | Default branch if omitted |

## Run Local

```bash
cp .env.example .env
set -a; source .env; set +a
mkdir -p .data/workspaces
ADDR=:8080 DATA_DIR=.data DB_PATH=.data/state.db WORKSPACE_ROOT=.data/workspaces go run ./cmd/bot
```

Health check:

```bash
curl -sS localhost:8080/healthz
```

## Docker

Build and run:

```bash
docker build -f deploy/Dockerfile -t slack-codex-runner .
docker run --rm -p 8080:8080 \
  -e SLACK_BOT_TOKEN \
  -e SLACK_SIGNING_SECRET \
  -e GITHUB_TOKEN \
  -e OPENAI_API_KEY \
  -e CODEX_COMMAND="codex exec" \
  -e AGENT_OUTPUT_MODE=structured \
  -e AGENT_OUTPUT_SCHEMA_VERSION=v1 \
  -e SLACK_LOG_MODE=summary \
  -v $(pwd)/.data:/data \
  slack-codex-runner
```

## Railway Deployment

1. Create service from this repo.
2. Add a persistent volume mounted at `/data`.
3. Set env vars (`SLACK_BOT_TOKEN`, `SLACK_SIGNING_SECRET`, `GITHUB_TOKEN`, `OPENAI_API_KEY`, `CODEX_COMMAND`, `AGENT_OUTPUT_MODE`, `AGENT_OUTPUT_SCHEMA_VERSION`, `SLACK_LOG_MODE`).
4. Expose port `8080` and use healthcheck `/healthz`.
5. Configure Slack URLs with your Railway public URL.

## Usage

Run command in Slack:

```text
/codex repo=my-org/my-repo branch=main "add /health endpoint and tests"
```

During execution:

- If command is risky: job moves to `needs_approval` and asks for **Allow once**.
- If runner is idle: job moves to `needs_input`.
- After code changes: job moves to `needs_review` and exposes actions:
  - `Run tests`
  - `Approve & Create PR`
  - `Abort`
- Default output behavior is structured:
  - Codex receives a JSON contract prompt.
  - Bot retries once if output is not valid JSON.
  - Bot posts a readable Spanish summary in the thread.

For thread input on `needs_input`, post:

```text
job_<id> your additional instruction
```

## Safety Behavior

Policy gate blocks obvious dangerous patterns and flags unknown commands for approval.

Denied examples:

- `rm -rf`
- `sudo`
- `curl | sh`

## Development

Run tests:

```bash
mkdir -p .cache/go-build
GOCACHE=$(pwd)/.cache/go-build go test ./...
```

Main folders:

- `cmd/bot` entrypoint
- `internal/slack` HTTP handlers + Slack API client
- `internal/jobs` queue + state transitions + orchestration
- `internal/runner` command runner with watchdog
- `internal/policy` allow/deny/approval logic
- `internal/git` clone/branch/diff/commit/push
- `internal/github` PR creation
- `internal/storage` persisted state store

## Notes

- Storage uses SQLite at `DB_PATH`.
