// Package validate содержит общие валидаторы полей API Kachō, общие для всех
// сервисов (Folder.Name, Network.Name, Subnet.Name и т. п.).
//
// Все валидаторы возвращают gRPC ошибку `InvalidArgument` с
// `BadRequest.field_violations[]` через `kacho-corelib/errors.InvalidArgument()`.
//
// Контракт verbatim от YC:
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
	"regexp"
	"unicode/utf8"

	coreerrors "github.com/PRO-Robotech/kacho-corelib/errors"
)

// nameRe — verbatim YC name regex для resource-manager / Folder / Cloud
// (strict-policy resources). Эквивалент YC `/[a-z]([-a-z0-9]{0,61}[a-z0-9])?/`.
//
// Ровно: первый символ — строчная буква; далее — буквы, цифры, дефис; последний
// символ — буква или цифра (не дефис). Длина 2..63.
var nameRe = regexp.MustCompile(`^[a-z]([-a-z0-9]{0,61}[a-z0-9])?$`)

// nameReVPC — verbatim YC permissive regex для VPC ресурсов
// (Network/Subnet/Address/RouteTable). Эквивалент YC
// `/|[a-zA-Z]([-_a-zA-Z0-9]{0,61}[a-zA-Z0-9])?/`. Допускает:
//   - empty string,
//   - заглавные буквы,
//   - underscore.
//
// Согласно probe реального YC API (2026-05-04): VPC.Network/Subnet/Address/
// RouteTable принимают `BadCAPS`, `abc_def`, `""` (HTTP 200 + Operation), но
// отклоняют имя начинающееся с цифры или превышающее 63 символа. См. finding
// YC-DIFF-NAME-VALIDATION.md.
var nameReVPC = regexp.MustCompile(`^([a-zA-Z]([-_a-zA-Z0-9]{0,61}[a-zA-Z0-9])?)?$`)

// labelKeyRe — YC label key regex (строчные + цифры + `-_./\`).
var labelKeyRe = regexp.MustCompile(`^[a-z][-_./\\a-z0-9]{0,62}$`)

// allowedZones — verbatim whitelist зон для Kachō (ровно YC verbatim).
//
// Любой `zoneId` вне этого списка — InvalidArgument; пустой `zoneId` —
// InvalidArgument с сообщением `zone_id is required`.
var allowedZones = map[string]struct{}{
	"ru-central1-a": {},
	"ru-central1-b": {},
	"ru-central1-c": {},
	"ru-central1-d": {},
}

const (
	// MaxNameLen — максимум для Name полей ресурсов (verbatim YC).
	MaxNameLen = 63
	// MaxDescriptionLen — лимит описания.
	MaxDescriptionLen = 256
	// MaxLabels — максимальное число label-пар на ресурс.
	MaxLabels = 64
	// MaxLabelKeyLen — длина ключа label.
	MaxLabelKeyLen = 63
	// MaxLabelValueLen — длина значения label.
	MaxLabelValueLen = 63
	// MaxPageSize — верхняя граница для page_size в List RPC (verbatim YC).
	MaxPageSize int64 = 1000
	// DefaultPageSize — значение по-умолчанию, когда клиент не задал page_size.
	DefaultPageSize int64 = 50
)

// Name проверяет, что value соответствует verbatim YC name-контракту для
// strict-policy ресурсов (resource-manager: Cloud, Folder; YC использует
// `/[a-z]([-a-z0-9]{0,61}[a-z0-9])?/`).
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

// NameVPC проверяет, что value соответствует verbatim YC permissive name-
// контракту для VPC ресурсов (Network, Subnet, Address, RouteTable; YC
// использует `/|[a-zA-Z]([-_a-zA-Z0-9]{0,61}[a-zA-Z0-9])?/`).
//
// Допускается: empty string, заглавные буквы, underscore. Длина 0..63.
// Имя начинающееся с цифры или с дефиса — InvalidArgument. См. finding
// YC-DIFF-NAME-VALIDATION.md.
func NameVPC(field, value string) error {
	if !nameReVPC.MatchString(value) {
		return coreerrors.InvalidArgument().
			AddFieldViolation(field, field+` must match ^([a-zA-Z]([-_a-zA-Z0-9]{0,61}[a-zA-Z0-9])?)?$ (letters, digits, hyphens, underscores; starts with letter; up to 63 chars; empty allowed)`).
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
// Семантика — verbatim YC contract:
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

// ZoneId проверяет, что value — валидное имя зоны Kachō.
//
// Контракт verbatim YC: `ru-central1-{a,b,c,d}`. Пустая строка — отдельная
// проверка `zone_id is required` (FieldViolation), значение вне whitelist —
// `zone_id must be one of: ru-central1-a/b/c/d` (FieldViolation). Возвращает
// InvalidArgument с FieldViolation либо nil.
func ZoneId(field, value string) error {
	if value == "" {
		return coreerrors.InvalidArgument().
			AddFieldViolation(field, field+" is required").
			Err()
	}
	if _, ok := allowedZones[value]; !ok {
		return coreerrors.InvalidArgument().
			AddFieldViolation(field, field+" must be one of: ru-central1-a, ru-central1-b, ru-central1-c, ru-central1-d").
			Err()
	}
	return nil
}

// dhcpDomainNameRe — RFC 1034/1123-совместимое DNS-имя.
//
// Контракт verbatim YC (probe 2026-05-04): YC отказывает на `!!!` с текстом
// "Illegal argument Invalid domain name '<value>'" (sync 400 code:3). Длина
// каждой метки 1..63, общая длина <= 253 (без trailing dot).
var dhcpDomainNameRe = regexp.MustCompile(`^([a-zA-Z0-9]([-a-zA-Z0-9]{0,61}[a-zA-Z0-9])?)(\.([a-zA-Z0-9]([-a-zA-Z0-9]{0,61}[a-zA-Z0-9])?))*$`)

// IPAddress проверяет, что value — синтаксически валидный IPv4 или IPv6
// адрес (без CIDR). Используется для DhcpOptions.{domain_name_servers,
// ntp_servers} и подобных IP-полей.
//
// Контракт verbatim YC (probe 2026-05-04): YC отвергает "not-an-ip" /
// "pool.ntp.org" с текстом "Illegal argument Cannot parse address: <value>".
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
// Verbatim YC text: "Invalid domain name '<value>'".
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

// allowedDdosProviders — verbatim YC whitelist (probe 2026-05-04).
//
// YC отвергает unknown provider с "Illegal argument Invalid DDoS protection
// provider." Пустая строка — OK (опциональное поле).
var allowedDdosProviders = map[string]struct{}{
	"":       {},
	"qrator": {},
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
// Contract verbatim YC (probe 2026-05-04): YC отказывает на любое непустое
// значение с "Illegal argument Invalid SMTP capability." (обычным tenant'ам
// нельзя её включить). Empty string — OK.
func SmtpCapability(field, value string) error {
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
// разрешённых для апдейта полей; всё остальное — InvalidArgument.
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
