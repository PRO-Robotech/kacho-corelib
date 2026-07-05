<!--
Copyright (c) PRO-Robotech
SPDX-License-Identifier: BUSL-1.1
-->

# Known divergences (accepted by-design)

Этот документ фиксирует осознанные отклонения `kacho-corelib` от общих
архитектурных рубрик (в т.ч. регламента *evgeniy* и Clean-Architecture-запретов
CLAUDE.md), которые команда приняла как by-design, а не как дефект. Каждое
отклонение сопровождается обоснованием и, где применимо, планом эволюции.

Отклонения ниже **осознаны и приняты**; ревью не должно заводить на них новые
issue как на баг. Реальные дефекты (не отклонения) фиксятся, а не документируются.

## 1. Config: env-var struct-tags (envconfig), а не YAML через viper/koanf

**Рубрика:** evgeniy config rule — «YAML-конфиг через viper/koanf».

**Как есть:** `config/config.go` реализует `Load`/`LoadPrefixed` поверх
`github.com/kelseyhightower/envconfig` (env-var struct-tags). Это платформенный
путь конфигурации всех сервисов Kachō (vpc, iam, compute, geo, registry,
api-gateway импортируют `corelib/config`).

**Почему принято:** 12-factor env-config — сознательная платформенная конвенция.
Сервисы деплоятся в Kubernetes, где конфиг приходит из ConfigMap/Secret →
env-переменных; слой файлового YAML-документа поверх этого не даёт выгоды и
добавляет вторую точку истины. Валидация значений выполняется в domain-newtype'ах
на входе, а не в схеме конфиг-документа.

**Эволюция:** если появится потребность в layered/file-config (напр. локальные
оверрайды), koanf/viper-loader вводится в `corelib/config` аддитивно, без слома
env-пути. До тех пор env-config — принятый способ, а не нарушение правила.

## 2. `operations` package-level Worker (Run/ConfigureDefault) — транзитный shim

**Рубрика:** Clean-Architecture «нет глобальных синглтонов вне cmd/».

**Как есть:** `operations/worker.go` держит `var defaultRegistry = NewWorker()` и
свободные функции `Run/Start/Wait/ConfigureDefault`. Инъектируемая альтернатива
— `RunWithWorker(w *Worker, ...)` + `NewWorker(...)` — уже присутствует и является
рекомендованным путём для composition-root'а.

**Почему принято:** свободные функции — backward-compatible shim поверх
инъектируемого `RunWithWorker`, чтобы сервисы могли мигрировать на явный
`*Worker`-DI постепенно. Ленивый старт без goroutine-side-effect при init
сохраняет «нет init-side-effect» для самого пакета.

**Footgun (задокументирован, не баг):** если composition-root вызовет
`operations.Run()` раньше `ConfigureDefault(WithRecorder(...))`, ленивый старт
латчит default-registry с `NopRecorder`, а последующий `ConfigureDefault`
вернёт `ErrWorkerStarted` → live-worker-метрики немы. Корректное использование:
на boot вызывать `ConfigureDefault(...)` **до** первого `Run`, либо использовать
`RunWithWorker` с явно сконфигурированным `*Worker` (полностью обходит глобал).

**Эволюция:** полный отказ от глобала = кросс-репо миграция сервисов на
`RunWithWorker` (kacho-compute/iam/geo/... вызывают `operations.Run`). Это
трекается вне corelib; сам corelib уже предоставляет инъектируемый путь.

## 3. `validate` — доменные name-политики и id-prefix-реестр в горизонтальном пакете

**Рубрика:** corelib — «только горизонтальные cross-cutting concerns».

**Как есть:** `validate/validate.go` централизует per-domain `NameVPC/NameCompute/
NameGateway`, whitelists (`DhcpDomainName`/`DdosProvider`/`SmtpCapability`) и
`resourceIDPrefixes` — карту 3-символьных id-префиксов всех доменов, используемую
`ResourceID` на authz-edge api-gateway.

**Почему принято (пока):** единый разделяемый валидатор даёт консистентную форму
ошибок (`InvalidArgument`, YC-style тексты) на общем edge без дублирования в
каждом сервисе. Реестр префиксов — **contributed-list**: новый домен добавляет
свой префикс сюда одной строкой (см. историю: `aap`, `uoc`, `reg/rop`).

**Известный hazard (задокументирован):** знание id-пространства домена живёт в
разделяемой библиотеке → выпуск нового домена требует правки этой карты +
релиз corelib + бамп в потребителях, прежде чем well-formed id нового домена
перестанет отбиваться `InvalidArgument` на edge. Это осознанный трейд-офф ради
единой формы ошибок.

**Эволюция:** при росте числа доменов реестр префиксов переносится в
gateway-concern (routing-table, наполняемая из service-registration), а
per-domain name-политики — в сервисы-владельцы. До тех пор — принятая
централизация.

## 4. Legacy `grpcsrv.UnaryPrincipalExtract` (безусловное доверие к заголовкам)

**Рубрика:** CWE-290/863 — доверие к client-supplied identity-заголовкам.

**Как есть:** `UnaryPrincipalExtract`/`StreamPrincipalExtract` читают
`x-kacho-principal-*` безусловно, без проверки транспорта/форвардера. Trust-aware
альтернатива — `UnaryCertIdentityExtract` + `UnaryTrustedPrincipalExtract(
WithTrustedForwarders(...))` — присутствует и является рекомендованной для
mTLS-листенеров.

**Почему сохранён:** нужен для insecure dev-листенера (нет client-cert вообще) и
для api-gateway→backend поверх доверенной сети. Удалять примитив нельзя без слома
всех текущих потребителей.

**Митигация в corelib (сделано):** конструкторы выводят одноразовый startup-WARN
о безусловном доверии и о предпочтении trust-aware связки; сам WARN + усиленный
doc-comment повышают шанс корректного монтирования.

**Остаточный риск (кросс-репо, вне corelib):** полное устранение = миграция
composition-root'ов сервисов на trust-aware связку на всех mTLS-листенерах. Это
дисциплина сервисных репо (kacho-vpc/compute/iam/...), а не corelib; corelib уже
предоставляет безопасную связку и предупреждает об опасном примитиве.

## 5. `authz` `AllowSystemPrincipal` — system-bypass гейтится Type+ID, не одним заголовком

**Рубрика:** CWE-863 — доверие к client-supplied principal-id.

**Как есть:** при `AllowSystemPrincipal=true` interceptor пропускает без per-RPC
Check только принципала с полной system-идентичностью
`Principal{Type:"system", ID:"bootstrap"}`. Матч по одному лишь `principalID ==
"bootstrap"` (id derivable из заголовка `x-kacho-principal-id`) — устранён: теперь
дополнительно перечитывается canonical, type-carrying principal из ctx и требуется
`Type=="system"`. Подделанный `{Type:"user", ID:"bootstrap"}` больше не обходит
Check, а проваливается в обычный путь (fail-closed). Регрессионный тест —
`TestInterceptor_AllowSystemPrincipal_RejectsForgedBootstrapType`.

**Почему это лишь частичное усиление (задокументировано):** type-gate закрывает
type-confusion-вектор, но НЕ аутентифицирует peer'а сам по себе. Если оператор
смонтирует non-trust-aware `UnaryPrincipalExtract` на peer-достижимый listener и
включит `AllowSystemPrincipal=true`, атакующий всё ещё может послать
`x-kacho-principal-type: system` + `id: bootstrap`. Это тот же остаточный
header-trust-риск, что и в §4.

**Правило эксплуатации:** `AllowSystemPrincipal` (по умолчанию `false`) включать
ТОЛЬКО на listener'е, куда не дозвонится недоверенный peer, ИЛИ в связке с
trust-aware `UnaryTrustedPrincipalExtract(WithTrustedForwarders(...))` (§4). Corelib
энфорсит форму идентичности; аутентичность транспорта — дисциплина composition-root'а.

## 6. `validate.resourceIDPrefixes` — env читается при package-init

**Рубрика:** Clean-Architecture — «wiring/чтение env только в composition-root».

**Как есть:** `var resourceIDPrefixes = buildResourceIDPrefixes(os.Getenv(
EnvExtraResourceIDPrefixes))` вычисляется один раз при импорте пакета и замораживается
в package-global на весь процесс.

**Почему принято (пока):** реестр префиксов — статическая платформенная константа
(3-символьные id-префиксы доменов, §3), а `KACHO_EXTRA_RESOURCE_ID_PREFIXES` —
опциональный аддитивный оверрайд для нового домена до его выпуска. Значение не
меняется в рантайме и одинаково для всех потребителей → package-level кэш
семантически корректен.

**Известный hazard (задокументирован):** env, выставленный ПОСЛЕ импорта пакета
(напр. в тесте), игнорируется — значение залатчено при init. Тесты, которым нужен
иной набор префиксов, должны выставлять env до первого импорта `validate` (или не
полагаться на этот путь). Перенос в конструктор (`NewResourceIDValidator(extra
[]string)`) — эволюция вместе с §3 (реестр → gateway-concern), меняет API
потребителей и делается тем же кросс-репо шагом.

## 7. `operations.Repo.Get`/`List` — unscoped по умолчанию (ownership-scoped вариант отдельным интерфейсом)

**Рубрика:** CWE-639 (IDOR) — «безопасный путь должен быть путём по умолчанию».

**Как есть:** экспортируемый `Repo` интерфейс несёт unscoped `Get(ctx, id)` и
`List(ctx, filter)` (фильтрует только по caller-supplied `AccountID`/`ResourceID`
без enforced ownership-предиката); ownership-scoped `GetOwned/ListOwned/CancelOwned`
живут на отдельном `OwnedOperationRepo` (реализован конкретным `pgRepo`, predicate
внутри SQL WHERE). Godoc `Repo.Get` и `Repo.List` несут явное IDOR-предупреждение и
указывают на `GetOwned`/`ListOwned` для tenant-facing `OperationService.Get`/`List`;
unscoped-путь — только для доверенных internal-вызовов.

**Почему интерфейс не меняли (contract-safe residual):** повышение
ownership-scoped-аксессоров в основной `Repo`-интерфейс (или добавление `Owner`-арга
в `Get`/`List`) — ломающее изменение Go-интерфейса: все реализации/моки
`operations.Repo` в сервисных репо (vpc/compute/iam/...) перестанут компилироваться.
Это кросс-репо API-миграция, вне scope contract-safe прохода. Runtime-риск закрыт
doc-hardening'ом + наличием `GetOwned`/`ListOwned`; сервисные handler'ы
OperationService.Get/List уже используют owner-scoped путь (`OwnerFromPrincipal` →
`GetOwned`/`ListOwned`/audit).

**Эволюция:** при следующем допустимом изменении Go-API `operations` — сделать
owner-scoped аксессоры дефолтными членами `Repo`, а unscoped — явно именованными
(`GetUnscoped`/`ListUnscoped`) для доверенных internal-вызовов (reconciler/worker).

## 8. `operations.Repo` — единый read+write порт, без CQRS Reader/Writer-разделения

**Рубрика:** godzila/evgeniy — CQRS-разделённые порты (Reader / tx-bound Writer).

**Как есть:** `operations/repo.go` держит один `Repo`-интерфейс, смешивающий чтение
(`Get`/`List`/`GetOwned`/`ListOwned`) и мутации (`Create`/`CreateWithPrincipal`/
`MarkDone`/`MarkError`/`Cancel`/`CancelOwned`). Единственная реализация — `pgRepo`.

**Почему интерфейс не разбит (contract-safe residual):** разбиение `Repo` на
`OperationReader` + `OperationWriter` — ломающее изменение Go-API: сигнатуры
инъекции и все моки `operations.Repo` в сервисных репо (vpc/compute/iam/geo/...)
перестанут удовлетворять новому набору портов и не скомпилируются. Чисто аддитивное
добавление узких интерфейсов *поверх* `Repo` (без миграции потребителей) породило бы
неиспользуемые типы — speculative generality, которую LEAN-проход запрещает
плодить. Поэтому разделение откладывается до координированной кросс-репо
Go-API-миграции (тот же шаг, что §7).

**Митигация:** транзакционная граница write-путей выражена на уровне реализации
(`pgRepo` writer-методы работают в переданной tx через `db.Transactor`), а не
на уровне порта; корректность atomicity этим не страдает.

## 9. `authz` — два раздельных subject-TTL-кеша (`Cache` и `listObjectsCache`)

**Рубрика:** DRY / corelib reuse — «примитив извлекается один раз».

**Как есть:** пакет `authz` держит два двухуровневых (`subject → map[key]entry`)
TTL-кеша: `Cache` (cache.go) для positive Check-результатов и `listObjectsCache`
(listobjects.go) для ListObjects-результатов. Скелет (two-level map, lazy TTL-expiry,
InvalidateBySubject через O(1) outer-delete, Size, инъектируемый `now`) внешне схож.

**Почему НЕ слиты в generic `subjectTTLCache[V]` (осознанно, не баг):** несмотря на
внешнее сходство, три аспекта расходятся содержательно, и унификация под общий
generic либо потеряла бы поведение, либо ослабила бы гарантию:

- **Политика эвикции разная.** `Cache.evictLocked` — expired-first, затем произвольная
  до low-water (`maxEntries*7/8`); `listObjectsCache.evictIfNeededLocked` — batch-LRU
  по expiry (10% oldest). Это не случайный дрейф, а разные бюджеты (Check — 100k
  мелких entry; ListObjects — 10k тяжёлых id-слайсов).
- **Учёт размера разный** по той же причине (инкрементальный `count` против rescan).
- **Safety-critical clobber-guard есть только у `Cache`.** `Cache.evictIfStale`
  удаляет stale-entry под write-lock ТОЛЬКО если `expiresAt.Equal(observed)` —
  защита от выбрасывания свежего positive-результата, записанного конкурентно между
  `RUnlock`/`Lock`. Протаскивание этого через generic + eviction-callback усложняет
  и рискует ослабить именно эту конкурентную гарантию (LEAN-запрет «без ослабления
  гарантий»).

Выгода DRY (≈по 40 строк скелета) не перевешивает риск для конкурентного пути
authz-кеша. Дублирование скелета — принятый трейд-офф; поведенческий контракт
каждого кеша покрыт своими тестами (`cache_test.go`, `listobjects_test.go`).

**Эволюция:** если у кешей сойдутся политика эвикции и модель учёта, извлечь
`subjectTTLCache[V]` с сохранением clobber-guard в общем `get`-пути.

## 10. `grpcsrv` keepalive-behavioral-тест использует реальное 16s idle-окно

**Рубрика:** testing.md — «нет time.Sleep-based синхронизации / real-clock
зависимости».

**Как есть:** `TestNewServer_AcceptsIdleKeepalive` (keepalive_integration_test.go)
делает `time.Sleep(16*time.Second)` и проверяет, что idle-conn остаётся `Ready` и
follow-up RPC проходит. Тест `-short`-gated (скипается в быстром CI).

**Почему окно нельзя схлопнуть (contract-safe residual):** тест верифицирует
**реальную production-конфигурацию** сервера — `DefaultKeepaliveEnforcement`
(`MinTime=5s`) против клиента, пингующего каждые `6s`, через окно `> 2` ping-
интервалов. Это единственный способ подтвердить, что server-enforcement НЕ строже
клиентского keepalive (иначе прилетел бы `GOAWAY too_many_pings` и inter-service
idle-conn стуллился бы). Уменьшить окно до сотен мс невозможно test-only: клиентский
ping < `MinTime=5s` сам спровоцировал бы `GOAWAY` и **инвертировал** бы проверку;
а параметризация keepalive-констант под тест — это правка прод-кода фабрики
`NewServer`, меняющая то самое поведение, которое тест валидирует.

**Митигация:** тест `-short`-gated, поэтому обычный быстрый CI его не гоняет;
16s-окно платится только в полном integration-прогоне, где интеграционные тесты и так
поднимают testcontainers. Real-clock-зависимость здесь — не флейк-паттерн, а
неотъемлемое свойство проверки таймингового контракта keepalive.

**Эволюция:** если появится тестовая фабрика с инъектируемым (но по-прежнему
production-репрезентативным) keepalive-профилем, окно можно пропорционально сжать,
сохранив соотношение client-ping < server-MinTime.
