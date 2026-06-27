# kacho-corelib

Переиспользуемые горизонтальные Go-пакеты платформы **Kachō** — самостоятельной
облачной control-plane платформы (IAM, VPC, Compute, Geography, Network Load
Balancer). Здесь живет все инфраструктурное, что нужно двум и более сервисам, —
чтобы не дублировать его в каждом репозитории сервиса.

Это библиотека: исполняемых бинарей и доменной бизнес-логики тут нет (она остается
в репозиториях сервисов). Пакеты узкие, самодостаточные и покрыты тестами.

## Состав

| Пакет | Назначение |
|---|---|
| `ids` | Генератор коротких id ресурсов (`<3-char prefix><17-char crockford-base32>`), источник энтропии — `crypto/rand`. |
| `validate` | Валидаторы входных полей (имя, описание, labels, page-size, resource-id) со стабильными текстами ошибок. |
| `errors` | Builder gRPC-статусов с `BadRequest`/`ResourceInfo`/`LocalizedMessage` details. |
| `filter` / `selector` | Разбор list-фильтров и сборка параметризованных `WHERE`-условий (без конкатенации значений). |
| `safeconv` | Безопасные числовые конверсии без переполнения. |
| `db` | Пул `pgx` с fail-fast Ping и `statement_timeout`; транзактор (`InTx`) с rollback на ошибке/панике. |
| `migrations/common` | Встроенные goose-миграции общих таблиц (operations). |
| `operations` | Long-Running Operations: durable-таблица, bounded worker-pool с per-op timeout и backpressure, reconciler-восстановление после рестарта. |
| `outbox` | Транзакционный outbox: writer (запись в одной TX с мутацией), drainer (at-least-once доставка, классификация poison), reconciler. |
| `baggage` | Перенос observability-значений (trace/request-id/slog) в worker-context без наследования cancel/deadline. |
| `grpcsrv` | Сборка gRPC-сервера: mTLS-credentials (fail-closed), извлечение cert-identity и principal с trust-границей, keepalive. |
| `grpcclient` | Сборка gRPC-клиента: secure-by-default TLS, keepalive. |
| `auth` / `authz` | Перенос caller-identity; PDP-клиент и gRPC-interceptor авторизации (fail-closed, кэш решений, rate-limit). |
| `retry` / `backoff` | Экспоненциальный backoff и retry-обертки для идемпотентных gRPC-вызовов. |
| `shutdown` | Graceful-shutdown manager (LIFO-handlers с per-handler timeout). |
| `observability` | Структурированный slog-логгер; инициализация телеметрии. |
| `resourcelifecycle` | Server-side helper для lifecycle-стрима ресурсных событий. |
| `config` | Загрузка конфигурации сервиса из окружения. |
| `proto` | Сгенерированные Go-stubs универсальных инфраструктурных контрактов (operation, validation, authz-options). |

## Использование

```bash
go get github.com/PRO-Robotech/kacho-corelib@latest
```

```go
import (
    "github.com/PRO-Robotech/kacho-corelib/ids"
    "github.com/PRO-Robotech/kacho-corelib/operations"
)

id := ids.NewID(ids.PrefixNetwork)
operations.Run(ctx, repo, opID, func(ctx context.Context) (*anypb.Any, error) {
    // фоновая работа мутации; worker автономен от request-ctx, но наследует trace
    return result, nil
})
```

## Сборка и проверки

```bash
make test     # go test ./... -race -cover (integration-тесты требуют Docker)
make lint     # golangci-lint
```

- Go 1.25+; Postgres 16+ и Docker нужны для integration-тестов (testcontainers).
- Unit-пакеты гоняются без Docker; integration-пакеты (`db`, `operations`, `outbox`,
  `migrations`, `grpcsrv`) поднимают контейнеры — под нагрузкой запускайте `-p 1`.

## Лицензия

[Business Source License 1.1](LICENSE) © PRO-Robotech. Свободное использование, кроме
прямой или косвенной коммерческой выгоды; по вопросам коммерческой лицензии —
к Licensor. См. также [CONTRIBUTING.md](CONTRIBUTING.md).
