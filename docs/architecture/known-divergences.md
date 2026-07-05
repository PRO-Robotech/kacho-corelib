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
