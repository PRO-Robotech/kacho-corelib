# kacho-corelib — CLAUDE.md

Переиспользуемые горизонтальные Go-пакеты Kachō (нужные 2+ сервисам). Базовые правила
Kachō (`.claude/rules/*`) — локальная копия, синхронизируемая из workspace
(`./sync-tooling.sh`; источник истины — `kacho-workspace/.claude/rules/`, копию здесь не
редактировать). `@import` ниже делает репо самодостаточным и при standalone-клоне.

## Базовые правила Kachō (@import — синканная копия из workspace)

@.claude/rules/00-kacho-core.md
@.claude/rules/api-conventions.md
@.claude/rules/polyrepo.md
@.claude/rules/architecture.md
@.claude/rules/data-integrity.md
@.claude/rules/security.md
@.claude/rules/git-youtrack.md
@.claude/rules/testing.md
@.claude/rules/vault.md
@.claude/rules/ai-tooling.md

## Специфика репо

- Здесь живёт всё горизонтальное: `ids/`, `errors/`, `config/`, `observability/`, `db/`
  (pgx pool + transactor), `grpcsrv/`, `grpcclient/`, `outbox/`, `operations/` (LRO table +
  Worker + Repo), `selector/`, `retry/`, `shutdown/`, `backoff/`, `validate/`, `auth/`,
  `authz/`, `filter/`, `migrations/common/`, `audit/`. Полный регламент — `@.claude/rules/architecture.md`.
- **Доменная** бизнес-логика (VPC ref-validation, Compute reconciler) сюда НЕ выносится —
  она живёт в сервисном репо.
- В build-графе: `replace ../kacho-proto`; сервисы делают `replace ../kacho-corelib`. Изменение
  exported API пакета — обнови vault `packages/<repo>-<pkg>.md` (`@.claude/rules/vault.md`).
