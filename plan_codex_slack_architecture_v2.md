# Plan de desarrollo (v2): controlar Codex CLI desde Slack (Railway + Docker + Go)

> **Idea central:** Slack es la **fuente de verdad** (UI + control + aprobaciones + respuestas). Codex trabaja en un workspace remoto y **cualquier duda, conflicto o acción riesgosa se resuelve por Slack** antes de aplicar cambios.

---

## 0) Principios de diseño (nuevos en v2)

1. **Slack-first**: todo Job vive en un thread de Slack (logs, preguntas, decisiones, aprobaciones).
2. **Human-in-the-loop por defecto**: jamás push directo a `main` sin aprobación explícita.
3. **Policy Gate**: comandos, acciones irreversibles y acceso a red están controlados por el servicio, no por texto.
4. **Jobs reanudables**: si Codex pregunta o queda esperando input, el job pasa a `needs_input` y se reanuda con la respuesta del usuario en Slack.
5. **Aislamiento por job**: cada ejecución usa un workspace aislado para evitar conflictos entre jobs.
6. **Auditoría**: siempre se produce `diff` + resumen + comandos ejecutados y se publica en Slack.

---

## 1) Arquitectura (alto nivel)

### Componentes
1. **Slack Bot (Go, Socket Mode)**
   - Recibe: `/codex ...`, menciones, botones, modals.
   - Mantiene un thread por Job y actualiza estados.

2. **Orquestador de Jobs**
   - Cola + worker pool.
   - Persistencia recomendada: SQLite en `/data/state.db` para reanudar estados.

3. **Runner Codex CLI con “I/O bridge”**
   - Ejecuta Codex dentro de un workspace.
   - Dos modos:
     - **No-interactivo** (preferido): prompts y ejecución controlada.
     - **Interactivo** (fallback): **PTY** para capturar prompts y permitir respuestas desde Slack.

4. **Policy Engine (Gate)**
   - Decide si se permite:
     - ejecutar un comando,
     - acceder a red,
     - leer/escribir rutas,
     - usar herramientas (git, go test, etc.).
   - Implementa:
     - allowlist/denylist,
     - *approve once* (aprobación puntual por Slack),
     - timeouts y límites.

5. **Workspace persistente (Railway Volume)**
   - Montado en `/data` en runtime.
   - Layout:
     - `/data/repos/<org>/<repo>/.git` (cache bare o mirror opcional)
     - `/data/workspaces/<job-id>/<repo>/...` (worktree o clone ligero)

6. **Integración GitHub**
   - Preferible: **GitHub App** (tokens de instalación).
   - Flujo: crear branch por job → push → abrir PR → comentar resultados.

---

## 2) Flujo de usuario en Slack (v2)

### Inicio
- Rod ejecuta:
  - `/codex repo=miorg/mirepo branch=main "Agrega endpoint /health + tests"`

### Thread por job
El bot crea un thread con:
- Estado: `queued → running`
- Contexto: repo, branch, objetivo
- Botones:
  - **View diff**
  - **Run tests**
  - **Approve & Create PR**
  - **Abort**

### Si Codex pregunta (clarificación)
- Estado: `needs_input`
- Bot publica:
  - “Codex necesita una respuesta:” + texto exacto (o resumido) + opciones
- Rod responde en el thread (o en modal).
- El servicio envía la respuesta al stdin/PTY y:
  - `needs_input → running`

### Si Codex solicita acción riesgosa
Ejemplos: comando fuera de allowlist, push directo a main, tocar archivos sensibles.
- Estado: `needs_approval`
- Bot publica:
  - “Solicitud de acción: <acción>”
  - Botones: **Allow once**, **Deny**, **Allow always (scope repo)** (opcional, si querés)
- Sin aprobación: no se ejecuta.

### Final
- Estado: `needs_review`
- Bot publica:
  - resumen del cambio
  - `git diff --stat`
  - links/botones:
    - View full diff (archivo o snippet)
    - Run tests
    - Create PR

---

## 3) Manejo de escenarios difíciles (clave de la v2)

### Escenario A: Codex queda esperando input (sin output)
**Detección**
- Watchdog: si no hay output por N segundos y el proceso sigue vivo → `needs_input`.
**Acción**
- Pregunta a Slack: “¿cómo continúo?” + “Responder” / “Cancelar”.

### Escenario B: Codex opera en modo interactivo (TTY)
**Solución**
- Ejecutar con PTY y puente de I/O:
  - stdout → Slack (buffered/summarized)
  - Slack reply → stdin PTY
**Guardas**
- límite de turnos (ej: máx 10 interacciones)
- timeout por espera de respuesta (ej: 10 min)
- botón Abort siempre disponible

### Escenario C: Conflictos de Git (merge/rebase)
**Estrategia**
- Cada job trabaja en su branch.
- Si hay conflictos al actualizar desde `main`:
  - se pasa a `needs_input` con opciones:
    - “Rebase automáticamente”
    - “Merge main”
    - “Abort”
  - Si conflicto persiste, mostrar archivos en conflicto y pedir decisión por Slack:
    - “Preferir ours/theirs por archivo”
    - “Abrir editor: pegar resolución” (modal con texto)
**Regla**
- Nunca resolver conflictos silenciosamente si cambia comportamiento crítico.

### Escenario D: Tests fallan
- Estado: `needs_input` o `needs_review` (según política).
- Bot publica:
  - comando ejecutado
  - resumen de error (primeras N líneas)
  - opciones:
    - “Intentar fix automáticamente”
    - “Ignorar y abrir PR igual”
    - “Abort”
- Recomendación: abrir PR con etiqueta “tests failing” solo con aprobación.

### Escenario E: Acceso a secretos / credenciales
- Codex **no** debe pedir tokens por texto.
- El servicio:
  - provee tokens mínimos vía env vars (solo si necesario),
  - y jamás los imprime en logs.
- Cualquier solicitud de “pasame token” → se bloquea y se pregunta en Slack.

### Escenario F: Concurrencia (múltiples jobs)
- Lock por repo/branch (o por repo) para operaciones críticas.
- Workspaces aislados por job.
- Política: límite de jobs simultáneos (configurable).

---

## 4) Plan de desarrollo por etapas (v2)

### Etapa 0 — Spike técnico
- Socket Mode + /codex
- Runner Codex capturando stdout/stderr
- Watchdog de “espera input”
- Prototipo PTY (fallback)

### Etapa 1 — Bot + UX de thread (Slack-first)
- Thread por job
- Botones: view diff / run tests / approve / abort
- Estados y mensajes consistentes

### Etapa 2 — Policy Gate (seguridad)
- Allowlist comandos (ej: `go test`, `go fmt`, `golangci-lint`, `git status`, `git diff`)
- Denylist (rm, sudo, curl|sh, etc.)
- “Approve once” por Slack para excepciones

### Etapa 3 — Workspaces aislados + Git flow robusto
- `git worktree` o clone por job
- Branch naming estándar
- Manejo de conflictos con “resolución via Slack”

### Etapa 4 — GitHub PR flow
- GitHub App (preferido)
- Crear PR, comentar resumen, adjuntar logs/diff

### Etapa 5 — Observabilidad + reanudabilidad
- SQLite en volumen
- rehidratación de jobs tras restart
- logs por job + correlación

---

## 5) Diseño de estados (máquina de estados)

- `queued`
- `running`
- `needs_input` (Codex pregunta / conflicto / tests fallan y requiere decisión)
- `needs_approval` (acción riesgosa)
- `needs_review` (diff listo para revisar)
- `done`
- `failed`
- `aborted`

Transiciones típicas:
- queued → running
- running → needs_input → running
- running → needs_approval → running
- running → needs_review → done
- cualquier estado → aborted

---

## 6) Diseño de módulos (Go)

- `cmd/bot/main.go` (socketmode + router)
- `internal/slack/`
  - blocks + modals + botones + thread utils
- `internal/jobs/`
  - queue + state machine + storage (sqlite)
- `internal/runner/`
  - process runner + timeouts
  - PTY bridge (fallback)
  - output buffering + summarizer para Slack
- `internal/policy/`
  - allowlist/denylist
  - approval prompts
- `internal/git/`
  - clone/fetch/mirror
  - worktree per job
  - diff/commit/push
  - conflict helpers
- `internal/github/`
  - auth GitHub App
  - create PR + comment + status

---

## 7) Checklist (v2)

- [ ] Thread por job (Slack-first)
- [ ] `needs_input` + reanudar job con respuesta Slack
- [ ] PTY fallback para prompts interactivos
- [ ] Policy gate + approvals (approve once)
- [ ] Workspaces aislados por job
- [ ] Manejo de conflictos por Slack
- [ ] Falla de tests con opciones por Slack
- [ ] PR flow (GitHub App)
- [ ] Persistencia (sqlite) + reanudabilidad
- [ ] Auditoría: diff + comandos ejecutados + logs

---

## 8) Próximos artefactos útiles
- Dockerfile ejemplo (Go + git + codex + pty deps)
- `main.go` mínimo con `slack-go/socketmode`
- interfaz `Runner` + `PolicyEngine` + máquina de estados
- templates de Slack Blocks (status, approvals, diff)
