# kacho-corelib

Общие Go-пакеты для сервисов Kachō.

В sub-phase 0.1: `ids`, `errors`, `db`, `config`, `grpcsrv`, `observability`.
В sub-phase 0.2 добавятся: `watch`, `outbox`, `selector`, `migrations/common`.

См. `kacho-workspace/docs/specs/03-deployment-and-operations.md` §1.

## `operations` — Long-Running Operations + baggage propagation

`operations.Run(callerCtx, repo, opID, fn)` запускает worker-горутину для async-мутаций
(см. workspace `CLAUDE.md` §«API contract — flat resources + Operations»).

`callerCtx` НЕ наследуется по deadline / cancel — handler возвращает Operation клиенту
сразу, request-ctx cancel-ится через миллисекунды, а worker должен жить независимо до
завершения. Однако **observability-values** (OpenTelemetry SpanContext, request-id,
slog logger, tenant/IAM claims, любые `context.WithValue`-ключи) **propagate'ятся** в
worker через [`baggage.Extract`](baggage/baggage.go) — реализовано через
`context.WithoutCancel` (Go 1.21+).

Это исправляет AP-3 из evgeniy-skill §11 / §7 I.3: раньше worker запускался с
`context.Background()` и trace-id, request-id, slog-attrs caller-ctx терялись —
worker-логи и trace-span'ы были оторваны от исходного запроса.

Use-case в сервисе ничего не меняет — sig `operations.Run(ctx, ..., fn)` стабилен;
изменение transparent.
