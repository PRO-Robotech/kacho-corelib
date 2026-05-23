// Package drainer реализует универсальный outbox-drainer для Kachō outbox-pattern
// (sub-phase W1.1, KAC-136 / KAC-137, эпик KAC-134 «kacho-iam → production-ready»).
//
// Drainer слушает LISTEN/NOTIFY-канал Postgres, дренит pending rows на старте
// (catch-up), декодирует payload через caller-supplied Decoder[T] и применяет
// каждую row через caller-supplied Applier[T] к target-системе.
//
// Свойства:
//   - идемпотентен (Applier возвращает ErrAlreadyApplied — drainer mark'ит sent_at)
//   - exp-backoff retry на transient errors
//   - permanent errors → mark last_error + attempt_count = MaxAttempts (poisoned-skip)
//   - exactly-once across replicas — атомарный CAS-claim
//     `UPDATE … WHERE sent_at IS NULL RETURNING …`
//   - graceful shutdown по ctx.Done() — дозавершает in-flight apply
//
// API contract см. в acceptance-doc:
//   docs/specs/sub-phase-W1.1-fga-outbox-drainer-acceptance.md §4 (Public Go API).
//
// Реализация (drainer.go, applier.go, decoder.go) — приходит в GREEN-фазе
// (см. KAC-137 W1.1.2 «rpc-implementer»). На момент существования только
// этого doc.go все интеграционные тесты `outbox/drainer/*integration_test.go`
// обязаны падать с compile-error `undefined: drainer.<Type>` — это RED-evidence
// для запрета #11 (test-first) workspace `CLAUDE.md`.
package drainer
