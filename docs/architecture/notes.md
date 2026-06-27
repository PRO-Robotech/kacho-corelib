<!--
Copyright (c) PRO-Robotech
SPDX-License-Identifier: BUSL-1.1
-->

# Design notes & known limitations

Этот документ фиксирует осознанные архитектурные решения и известные ограничения
библиотеки. Он адресован контрибьюторам и потребителям пакетов.

## Design decisions

- **Worker отвязан от request-context.** `operations.Run` запускает фоновую работу
  мутации, не наследуя cancel/deadline вызывающего request'а (handler сразу
  возвращает `Operation`), но наследуя observability-значения через `baggage`.
  Поверх накладывается собственный per-op timeout (`WithOperationTimeout`, дефолт
  4m, строго меньше grace reconciler'а) — зависший вызов не держит слот пула.
- **Backpressure без потери данных.** При переполнении in-memory backlog'а
  (`WithMaxBacklog`) задача не кладется в память: операция уже durable в БД и
  добирается reconciler'ом. Это ограничивает память под перегрузкой ценой задержки.
- **Доверие к forwarded-principal — на границе.** `grpcsrv` принимает
  `x-kacho-principal-*` только от mTLS-verified peer'а; через
  `WithTrustedForwarders` доверие можно сузить до allow-list'а cert-identity
  доверенных форвардеров (edge-gateway). Legacy `UnaryPrincipalExtract` (без
  trust-проверки) помечен Deprecated.
- **Авторизация fail-closed.** Не-замапленный RPC, недоступность PDP, отсутствие
  principal'а — все это deny; name-based исключений для «internal»-RPC нет (exempt
  только явным флагом в карте).

## Known limitations

- **Outbox: нет встроенного retention.** `outbox`-таблица растет монотонно; helper
  компакции (сохраняющий anti-race инвариант reconciler'а) пока не предоставлен.
  Для больших объемов потребителю следует предусмотреть периодическую очистку
  доставленных строк с сохранением последнего intent на ресурс.
- **Outbox: эталонные индексы под claim.** Запросы claim (`ORDER BY attempt_count,
  id`) и проверки intent (`ORDER BY id DESC`) выигрывают от partial-индексов; их
  состав потребитель задает в своей схеме под профиль нагрузки.
- **Outbox drainer: Apply внутри claim-транзакции.** Внешний вызов доставки
  выполняется внутри claim-tx (held-lock как лизинг для HA-dedup). Не выставляйте
  `idle_in_transaction_session_timeout` ниже `ApplyTimeout`.
- **Телеметрия трейсов.** `observability.InitOtel` при заданном endpoint'е логирует
  предупреждение и не экспортирует трейсы — OTLP-exporter в этой сборке не подключен
  (активен структурированный slog). Реальный экспорт — на стороне потребителя.
- **`selector`/`filter` — defense-in-depth по whitelist.** SQL-условия
  параметризованы по значениям; имена полей берутся из whitelist при разборе.
  Не передавайте имена полей из недоверенного источника в обход разбора.
