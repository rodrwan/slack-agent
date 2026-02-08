# AGENTS.md - Continuidad de desarrollo de `slack-codex`

## 1) Objetivo del proyecto
Servicio Slack-first para ejecutar trabajos de Codex contra repos GitHub, con:
- estados de job (`queued`, `running`, `needs_input`, `needs_approval`, `needs_review`, `done`, `failed`, `aborted`),
- aprobaciones humanas para acciones riesgosas,
- workspaces aislados por job,
- flujo de branch + PR,
- auditoría básica por eventos/logs.

## 2) Estado actual (implementado)
Estructura base implementada y funcional:
- `cmd/bot/main.go`: arranque del servicio HTTP.
- `internal/slack/`: handlers HTTP (`/slack/commands`, `/slack/interactions`, `/slack/events`) + cliente Slack API.
- `internal/jobs/`: orquestación de cola/workers + transiciones de estado.
- `internal/runner/`: ejecución de comandos con watchdog por inactividad.
- `internal/policy/`: allow/deny/needs_approval.
- `internal/git/`: clone/fetch/branch/diff/commit/push.
- `internal/github/`: creación de PR por API.
- `internal/storage/sqlite.go`: persistencia real SQLite via `modernc.org/sqlite`.
- `deploy/Dockerfile`: imagen de despliegue.
- `README.md`: guía operativa e integración.
- `.env.example`: variables base.
- `.gitignore`: ignora `.cache/`.

## 3) Decisiones importantes ya tomadas
1. Storage quedó en SQLite real (no JSON fallback).
2. Cache de Go se usa local al repo para evitar problemas de permisos sandbox:
- `GOCACHE=$(pwd)/.cache/go-build`
- `.cache/` está ignorado por git.
3. Integración Slack actual es por endpoints HTTP (Events/Interactions/Commands), no Socket Mode.
4. Multi-repo soportado por comando `/codex repo=org/repo ...` usando una credencial GitHub global actual.

## 4) Variables de entorno relevantes
Ver `.env.example` y `README.md`. Críticas:
- `SLACK_BOT_TOKEN`
- `SLACK_SIGNING_SECRET`
- `GITHUB_TOKEN`
- `CODEX_COMMAND`
- `DB_PATH`
- `WORKSPACE_ROOT`

## 5) Comandos de desarrollo (recordatorio)
### Tests
```bash
mkdir -p .cache/go-build
GOCACHE=$(pwd)/.cache/go-build go test ./...
```

### Ejecutar local
```bash
cp .env.example .env
set -a; source .env; set +a
mkdir -p .data/workspaces
ADDR=:8080 DATA_DIR=.data DB_PATH=.data/state.db WORKSPACE_ROOT=.data/workspaces go run ./cmd/bot
```

## 6) Estado de permisos/dependencias en este entorno
- Ya se autorizó ejecutar `go mod` con permiso persistente.
- Si faltan módulos en una sesión nueva, usar `go mod tidy`.
- Si `go get` vuelve a requerir red fuera de sandbox, solicitar aprobación de comando con escalación.

## 7) Pendientes recomendados (siguiente iteración)
Prioridad alta:
1. Migrar Slack HTTP a Socket Mode (si se quiere alinear 100% con el plan v2 original).
2. Implementar GitHub App multi-installation (en lugar de token global) para control fino por repo/org.
3. Fortalecer flujo `needs_input` con modal/respuesta más natural (hoy se usa formato `job_<id> ...` en thread).

Prioridad media:
1. Endurecer policy engine (scope por repo, reglas configurables, aprobación persistente por scope).
2. Manejo avanzado de conflictos git en interacción Slack.
3. Más tests de integración en `internal/jobs` y `internal/slack`.

## 8) Riesgos conocidos
- El comando ejecutado para Codex hoy depende de `CODEX_COMMAND` y quoting simple.
- El flujo de input en thread es funcional pero todavía básico.
- El flow de PR asume permisos correctos de `GITHUB_TOKEN` en el repo destino.

## 9) Criterio de "listo para demo"
- `/codex ...` crea job y thread en Slack.
- Job pasa por estados correctamente.
- `needs_approval` y `needs_input` pausan/reanudan.
- `Approve & Create PR` hace push de branch y abre PR.
- `go test ./...` pasa usando `GOCACHE=$(pwd)/.cache/go-build`.

## 10) Convenciones para próximas sesiones
- No usar cache global del sistema para Go en este entorno.
- Mantener `.cache/` fuera de git.
- Antes de cambios grandes: correr `go test ./...`.
- Después de cambios grandes: actualizar `README.md` y este `AGENTS.md`.
