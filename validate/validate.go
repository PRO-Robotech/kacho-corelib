// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// Package validate содержит общие валидаторы полей API Kachō, общие для всех
// сервисов (Folder.Name, Network.Name, Subnet.Name и т. п.).
//
// Все валидаторы возвращают gRPC ошибку `InvalidArgument` с
// `BadRequest.field_violations[]` через `kacho-corelib/errors.InvalidArgument()`.
//
// Контракт валидации полей:
//   - Name: 2..63 символов, regex `^[a-z][-a-z0-9]{0,61}[a-z0-9]$` (короткое
//     имя из строчных букв, цифр и дефисов; начинается с буквы; не оканчивается
//     дефисом). Пустое имя — отдельная проверка `name is required`.
//   - Description: до 256 символов.
//   - Labels: до 64 пар; ключ `^[a-z][-_./\\a-z0-9]{0,62}$` (1..63 байта);
//     значение 0..63 байта.
//   - UpdateMask: каждое поле должно быть известно сервисом; неизвестное —
//     `InvalidArgument`.
package validate

import (
	"net"
	"os"
	"regexp"
	"strings"
	"unicode/utf8"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	coreerrors "github.com/PRO-Robotech/kacho-corelib/errors"
	"github.com/PRO-Robotech/kacho-corelib/ids"
)

// nameRe — строгий regex имени для strict-policy ресурсов (Folder, Cloud).
// Шаблон `/[a-z]([-a-z0-9]{0,61}[a-z0-9])?/`.
//
// Ровно: первый символ — строчная буква; далее — буквы, цифры, дефис; последний
// символ — буква или цифра (не дефис). Длина 2..63.
var nameRe = regexp.MustCompile(`^[a-z]([-a-z0-9]{0,61}[a-z0-9])?$`)

// nameReVPC — нестрогий regex имени для VPC ресурсов
// (Network/Subnet/Address/RouteTable). Шаблон
// `/|[a-zA-Z]([-_a-zA-Z0-9]{0,61}[a-zA-Z0-9])?/`. Допускает:
//   - empty string,
//   - заглавные буквы,
//   - underscore.
//
// VPC.Network/Subnet/Address/RouteTable принимают `BadCAPS`, `abc_def`, `""`,
// но отклоняют имя, начинающееся с цифры или превышающее 63 символа.
var nameReVPC = regexp.MustCompile(`^([a-zA-Z]([-_a-zA-Z0-9]{0,61}[a-zA-Z0-9])?)?$`)

// nameReCompute — нестрогий regex имени для Compute ресурсов
// (Disk/Image/Snapshot/Instance). Шаблон
// `"|[a-z]([-_a-z0-9]{0,61}[a-z0-9])?"` — **lowercase**-only + digits + hyphens +
// underscore, empty allowed, начинается с буквы, не оканчивается дефисом, длина
// 0..63. Отличие от nameReVPC: НЕТ uppercase.
var nameReCompute = regexp.MustCompile(`^([a-z]([-_a-z0-9]{0,61}[a-z0-9])?)?$`)

// nameReGateway — regex имени для VPC Gateway: строгий lowercase + цифры +
// дефисы с разрешенной пустой строкой. Шаблон
// `/|[a-z]([-a-z0-9]{0,61}[a-z0-9])?/`. Без uppercase / underscore (в отличие
// от nameReVPC). Тот же контракт для других strict-policy ресурсов с
// разрешенной пустой строкой.
var nameReGateway = regexp.MustCompile(`^([a-z]([-a-z0-9]{0,61}[a-z0-9])?)?$`)

// labelKeyRe — regex ключа label: строчные + цифры + `-_./\@`. Шаблон
// `[a-z][-_./\\@0-9a-z]*`, где `@` входит в character class.
var labelKeyRe = regexp.MustCompile(`^[a-z][-_./\\@a-z0-9]{0,62}$`)

const (
	// MaxNameLen — максимум для Name полей ресурсов.
	MaxNameLen = 63
	// MaxDescriptionLen — лимит описания.
	MaxDescriptionLen = 256
	// MaxLabels — максимальное число label-пар на ресурс.
	MaxLabels = 64
	// MaxLabelKeyLen — длина ключа label.
	MaxLabelKeyLen = 63
	// MaxLabelValueLen — длина значения label.
	MaxLabelValueLen = 63
	// MaxPageSize — верхняя граница для page_size в List RPC.
	MaxPageSize int64 = 1000
	// DefaultPageSize — значение по-умолчанию, когда клиент не задал page_size.
	DefaultPageSize int64 = 50
)

// Name проверяет, что value соответствует name-контракту для strict-policy
// ресурсов (Cloud, Folder; шаблон `/[a-z]([-a-z0-9]{0,61}[a-z0-9])?/`).
//
// Возвращает err типа InvalidArgument с FieldViolation, либо nil если ok.
// Не проверяет «is required» — это делает caller отдельной проверкой
// `value == ""`, чтобы сообщение было понятным.
func Name(field, value string) error {
	if !nameRe.MatchString(value) {
		return coreerrors.InvalidArgument().
			AddFieldViolation(field, field+` must match ^[a-z]([-a-z0-9]{0,61}[a-z0-9])?$ (lowercase letters, digits, hyphens; starts with letter, ends with letter or digit; 2..63 chars)`).
			Err()
	}
	return nil
}

// NameVPC проверяет, что value соответствует нестрогому name-контракту для
// VPC ресурсов (Network, Subnet, Address, RouteTable; шаблон
// `/|[a-zA-Z]([-_a-zA-Z0-9]{0,61}[a-zA-Z0-9])?/`).
//
// Допускается: empty string, заглавные буквы, underscore. Длина 0..63.
// Имя, начинающееся с цифры или с дефиса, — InvalidArgument.
func NameVPC(field, value string) error {
	if !nameReVPC.MatchString(value) {
		return coreerrors.InvalidArgument().
			AddFieldViolation(field, field+` must match ^([a-zA-Z]([-_a-zA-Z0-9]{0,61}[a-zA-Z0-9])?)?$ (letters, digits, hyphens, underscores; starts with letter; up to 63 chars; empty allowed)`).
			Err()
	}
	return nil
}

// NameCompute проверяет, что value соответствует нестрогому name-контракту для
// Compute ресурсов (Disk, Image, Snapshot, Instance; шаблон
// `"|[a-z]([-_a-z0-9]{0,61}[a-z0-9])?"`).
//
// Допускается: empty string, underscore. Только lowercase (в отличие от NameVPC).
// Начинается с буквы; не оканчивается дефисом; длина 0..63.
func NameCompute(field, value string) error {
	if !nameReCompute.MatchString(value) {
		return coreerrors.InvalidArgument().
			AddFieldViolation(field, field+` must match ^([a-z]([-_a-z0-9]{0,61}[a-z0-9])?)?$ (lowercase letters, digits, hyphens, underscores; starts with letter; up to 63 chars; empty allowed)`).
			Err()
	}
	return nil
}

// NameGateway — name-контракт для Gateway: strict (lowercase + digits +
// hyphens) с разрешенной пустой строкой. Шаблон
// `/|[a-z]([-a-z0-9]{0,61}[a-z0-9])?/`. Без uppercase и underscore (в отличие
// от NameVPC).
func NameGateway(field, value string) error {
	if !nameReGateway.MatchString(value) {
		return coreerrors.InvalidArgument().
			AddFieldViolation(field, field+` must match ^([a-z]([-a-z0-9]{0,61}[a-z0-9])?)?$ (lowercase letters, digits, hyphens; starts with letter; up to 63 chars; empty allowed)`).
			Err()
	}
	return nil
}

// Description проверяет длину поля description (UTF-8).
func Description(field, value string) error {
	if utf8.RuneCountInString(value) > MaxDescriptionLen {
		return coreerrors.InvalidArgument().
			AddFieldViolation(field, field+" length exceeds 256 chars").
			Err()
	}
	return nil
}

// Labels проверяет map labels: число пар, длину и regex ключа, длину значения.
func Labels(field string, labels map[string]string) error {
	if len(labels) > MaxLabels {
		return coreerrors.InvalidArgument().
			AddFieldViolation(field, "too many labels (max 64)").
			Err()
	}
	for k, v := range labels {
		if len(k) == 0 || len(k) > MaxLabelKeyLen || !labelKeyRe.MatchString(k) {
			return coreerrors.InvalidArgument().
				AddFieldViolation(field+"."+k, "invalid label key (1..63 chars, lowercase letters, digits, _-./\\)").
				Err()
		}
		if len(v) > MaxLabelValueLen {
			return coreerrors.InvalidArgument().
				AddFieldViolation(field+"."+k, "label value exceeds 63 chars").
				Err()
		}
	}
	return nil
}

// PageSize проверяет границы page_size в List RPC.
//
// Семантика контракта:
//   - page_size == 0 → допустимо; клиент явно не задал, репозиторий применяет
//     DefaultPageSize. Возвращает (DefaultPageSize, nil).
//   - page_size < 0 или > MaxPageSize → InvalidArgument с FieldViolation;
//     возвращает (0, err). Не silent fallback — это нарушение контракта.
//   - 1..MaxPageSize → возвращает (value, nil).
//
// Возвращаемое effective значение нужно использовать в LIMIT-выражении SQL.
// Каждый репозиторий-метод List должен вызывать PageSize первой строкой
// и пробрасывать err наружу через service.
func PageSize(field string, value int64) (int64, error) {
	if value < 0 || value > MaxPageSize {
		return 0, coreerrors.InvalidArgument().
			AddFieldViolation(field, field+" must be in [0..1000] (0 means default)").
			Err()
	}
	if value == 0 {
		return DefaultPageSize, nil
	}
	return value, nil
}

// ZoneId — format/required-валидация: проверяет, что value не пустой.
//
// Список валидных зон НЕ хардкодится. Existence-валидация (есть ли такая
// зона в БД) — ответственность сервиса, владеющего таблицей `zones`
// (kacho-vpc). Здесь только required-check — формируем единообразный
// FieldViolation для пустого zone_id.
//
// Пустая строка → InvalidArgument c FieldViolation `<field> is required`.
// Непустое значение → nil (caller обязан выполнить existence-check).
func ZoneId(field, value string) error { //nolint:revive // стабильное имя публичного API (потребляется сервисами); переименование — ломающее изменение
	if value == "" {
		return coreerrors.InvalidArgument().
			AddFieldViolation(field, field+" is required").
			Err()
	}
	return nil
}

// dhcpDomainNameRe — RFC 1034/1123-совместимое DNS-имя.
//
// Контракт валидации: отвергаем некорректное имя с текстом
// "Illegal argument Invalid domain name '<value>'" (sync 400 code:3). Длина
// каждой метки 1..63, общая длина <= 253 (без trailing dot).
var dhcpDomainNameRe = regexp.MustCompile(`^([a-zA-Z0-9]([-a-zA-Z0-9]{0,61}[a-zA-Z0-9])?)(\.([a-zA-Z0-9]([-a-zA-Z0-9]{0,61}[a-zA-Z0-9])?))*$`)

// IPAddress проверяет, что value — синтаксически валидный IPv4 или IPv6
// адрес (без CIDR). Используется для DhcpOptions.{domain_name_servers,
// ntp_servers} и подобных IP-полей.
//
// Контракт валидации: отвергаем "not-an-ip" / "pool.ntp.org" с текстом
// "Illegal argument Cannot parse address: <value>".
//
// Возвращает InvalidArgument с FieldViolation либо nil.
func IPAddress(field, value string) error {
	if net.ParseIP(value) == nil {
		return coreerrors.InvalidArgument().
			AddFieldViolation(field, "Cannot parse address: "+value).
			Err()
	}
	return nil
}

// DhcpDomainName проверяет, что value — валидное DNS-имя (RFC 1123).
//
// Пустая строка — OK (поле опциональное). Длина общая <= 253, regex выше.
//
// Текст ошибки: "Invalid domain name '<value>'".
func DhcpDomainName(field, value string) error {
	if value == "" {
		return nil
	}
	if utf8.RuneCountInString(value) > 253 || !dhcpDomainNameRe.MatchString(value) {
		return coreerrors.InvalidArgument().
			AddFieldViolation(field, "Invalid domain name '"+value+"'").
			Err()
	}
	return nil
}

// allowedDdosProviders — whitelist разрешенных провайдеров.
//
// Неизвестный провайдер отвергается с "Illegal argument Invalid DDoS protection
// provider." Пустая строка — OK (опциональное поле).
var allowedDdosProviders = map[string]struct{}{
	"":         {},
	"qrator":   {},
	"advanced": {},
}

// DdosProvider проверяет ddos_protection_provider — whitelist.
func DdosProvider(field, value string) error {
	if _, ok := allowedDdosProviders[value]; !ok {
		return coreerrors.InvalidArgument().
			AddFieldViolation(field, "Invalid DDoS protection provider.").
			Err()
	}
	return nil
}

// SmtpCapability проверяет outgoing_smtp_capability.
//
// Контракт валидации: отвергаем любое непустое значение с
// "Illegal argument Invalid SMTP capability." (обычным tenant'ам нельзя ее
// включить). Empty string — OK.
func SmtpCapability(field, value string) error { //nolint:revive // стабильное имя публичного API (потребляется сервисами); переименование — ломающее изменение
	if value != "" {
		return coreerrors.InvalidArgument().
			AddFieldViolation(field, "Invalid SMTP capability.").
			Err()
	}
	return nil
}

// UpdateMask проверяет, что все field-ы в mask содержатся в known.
//
// Используется в *.Update методах: каждый сервис указывает свой набор
// разрешенных для апдейта полей; все остальное — InvalidArgument.
func UpdateMask(field string, mask []string, known map[string]struct{}) error {
	for _, f := range mask {
		if _, ok := known[f]; !ok {
			return coreerrors.InvalidArgument().
				AddFieldViolation(field, "unknown field in update_mask: "+f).
				Err()
		}
	}
	return nil
}

// resourceIDPrefixes — известные 3-символьные prefix'ы resource-id'ов Kachō.
// Если появится новый домен с новым prefix — добавить сюда.
//
// ВАЖНО: набор обязан покрывать КАЖДЫЙ живой prefix платформы. Family-agnostic
// `ResourceID` возвращает InvalidArgument на любой неизвестный 3-символьный
// prefix, поэтому пропущенный здесь живой prefix превращает ЛЮБОЙ well-formed id
// этого семейства в 400 на authz-edge api-gateway (вместо роутинга к
// сервису-владельцу). Регрессионный guard (validate/validate_resourceid_test.go)
// перечисляет все известные prefix'ы и падает, если какой-то не зарегистрирован тут.
//
// VPC (per-ресурс): Network=net, Subnet=sub, Address=adr, RouteTable=rtb,
// SecurityGroup=sgr, Gateway=gtw, NetworkInterface=nic, AddressPool=apl.
// Op-root VPC = enp (PrefixOperationVPC, для маршрутизации Operation.Get и
// backward-compat существующих enp-операций).
//
// IAM: Account=acc, Project=prj, User=usr, ServiceAccount=sva, Group=grp,
// Role=rol, AccessBinding=acb, Operation(IAM)=iop. (cag/cond/soc/evt — формат
// `<prefix>_<…>` с underscore, не 3-char ids.NewID, и не на публичной
// REST-поверхности под authz — здесь не нужны.)
//
// NLB: LoadBalancer=nlb (== PrefixOperationNLB), Listener=lst, TargetGroup=tgr.
//
// Legacy/прочие домены: Cloud/Folder=b1g, Organization=bpf (оставлены для
// legacy operation-id), Instance/Disk=epd, Image/Snapshot=fd8. Старые enp/e9b
// остаются known — family-agnostic проверка не должна давать InvalidArgument на
// корректные id переходного периода.
// EnvExtraResourceIDPrefixes — имя env-переменной с ДОПОЛНИТЕЛЬНЫМИ известными
// 3-символьными resource-id prefix'ами (comma-separated, напр. "xyz,qqq").
// Читается один раз при инициализации пакета и мёржится в базовый набор.
//
// Назначение — снять blast-radius «новый домен → InvalidArgument на authz-edge
// api-gateway, пока corelib не отредактирован и не перевыпущен»: оператор
// api-gateway задаёт префикс нового семейства через config/env, БЕЗ релиза
// corelib. Базовые платформенные prefix'ы остаются захардкожены (стабильны,
// покрыты регрессионным guard-тестом); config-путь — только расширение вперёд.
const EnvExtraResourceIDPrefixes = "KACHO_EXTRA_RESOURCE_ID_PREFIXES"

// baseResourceIDPrefixes — известные 3-символьные prefix'ы resource-id'ов Kachō,
// стабильное ядро. ЕДИНЫЙ источник — ids.KnownPrefixes() (vpc/nlb/compute/apps/
// registry/iam/legacy префиксы владеет ids), поэтому здесь НЕ дублируется список
// литералов: раньше две независимые копии (ids.knownPrefixes и этот набор) уже
// разошлись (reg/rop были только тут). Расширяется через EnvExtraResourceIDPrefixes
// без правки кода.
var baseResourceIDPrefixes = ids.KnownPrefixes()

// parseResourceIDPrefixes нормализует comma-separated значение env в список
// значимых prefix'ов: обрезает пробелы, приводит к нижнему регистру, отбрасывает
// пустые и НЕ-ровно-3-символьные токены (family-agnostic ResourceID сверяет
// только первые 3 символа id — токен иной длины никогда не сматчился бы, поэтому
// молча его игнорируем как невалидную конфигурацию).
func parseResourceIDPrefixes(csv string) []string {
	if strings.TrimSpace(csv) == "" {
		return nil
	}
	var out []string
	for _, tok := range strings.Split(csv, ",") {
		p := strings.ToLower(strings.TrimSpace(tok))
		if len(p) != 3 {
			continue
		}
		out = append(out, p)
	}
	return out
}

// buildResourceIDPrefixes копирует базовый набор и мёржит в него extra-prefix'ы
// из config (comma-separated csv). Чистая функция — тестируется без env.
func buildResourceIDPrefixes(csv string) map[string]struct{} {
	m := make(map[string]struct{}, len(baseResourceIDPrefixes)+4)
	for k := range baseResourceIDPrefixes {
		m[k] = struct{}{}
	}
	for _, p := range parseResourceIDPrefixes(csv) {
		m[p] = struct{}{}
	}
	return m
}

// resourceIDPrefixes — эффективный набор (base + config-extras), собранный один
// раз при инициализации пакета.
var resourceIDPrefixes = buildResourceIDPrefixes(os.Getenv(EnvExtraResourceIDPrefixes))

// ResourceID проверяет, что resource-id синтаксически валиден — начинается с
// известного 3-символьного prefix Kachō (см. resourceIDPrefixes). Пустой id —
// пропускается (required-проверка / transcoding-роутинг — отдельно).
//
// Контракт: на malformed / нераспознанный resource-id мутирующие и read-RPC
// отдают sync `InvalidArgument` с flat-message `"invalid <resourceType> id '<id>'"`
// (НЕ `NotFound`). Семантика **family-agnostic**: prefix должен быть из известного
// набора, но НЕ обязан совпадать с типом ресурса (`enp`-id, переданный как
// subnet-id, проходит → дальше `repo.Get` → `NotFound`). Длину/алфавит тела
// внутри здесь не проверяем.
//
//	resourceType   — имя ресурса в нижнем регистре ("network", "subnet",
//	                 "security group", "folder", "gateway", "private endpoint", ...).
//	expectedPrefix — prefix семейства этого ресурса (ids.PrefixNetwork и т.п.).
//	                 По контракту проверка family-agnostic (см. выше), поэтому
//	                 значение осознанно НЕ сверяется с id — параметр документирует
//	                 в call-site'е, какое семейство ожидается, не навязывая strict-
//	                 сверку. Это не «зарезервировано на будущее»: family-agnostic —
//	                 конечный контракт (enp-id как subnet-id должен доходить до
//	                 repo.Get → NotFound, а не отбиваться InvalidArgument здесь).
//
// Возвращаемая ошибка — готовый gRPC `status` с нужным flat-message (не
// field-violation builder — в этом случае контракт требует flat-message).
func ResourceID(resourceType, expectedPrefix, id string) error {
	_ = expectedPrefix // family-agnostic по контракту — см. doc выше.
	if id == "" {
		return nil
	}
	if len(id) >= 3 {
		if _, ok := resourceIDPrefixes[id[:3]]; ok {
			return nil
		}
	}
	return status.Errorf(codes.InvalidArgument, "invalid %s id '%s'", resourceType, id)
}
