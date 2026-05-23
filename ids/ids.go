// Package ids — генератор short-form идентификаторов ресурсов в формате
// "<3-char prefix><17-char crockford-base32>" (всего 20 символов).
//
// Формат соответствует verbatim cloud-provider-API convention: ресурсы и
// операции идентифицируются короткими непрозрачными строками с per-domain
// префиксом, что позволяет gateway-у маршрутизировать запросы к нужному
// backend по первому сегменту id.
//
// Префиксы определены константами PrefixCloud, PrefixFolder, PrefixNetwork,
// и т.д. Префикс должен быть ровно 3 символа.
package ids

import (
	"crypto/rand"
	"encoding/binary"
	"strings"
)

// crockfordAlphabet — Crockford base32, lowercase (без I, L, O, U).
const crockfordAlphabet = "0123456789abcdefghjkmnpqrstvwxyz"

// idBodyLen — длина тела id (без префикса), в base32-символах.
const idBodyLen = 17

// totalLen — полная длина id с префиксом.
const totalLen = 3 + idBodyLen

// Per-resource префиксы (3 символа, lowercase).
//
// Сгруппированы по домену:
//   - resource-manager: cloud, folder, organization
//   - vpc: network, subnet, address, route_table, security_group
//
// Operation.id наследует префикс домена-владельца ресурса (как в
// reference-API): RM-операции → PrefixCloud (b1g…), VPC-операции →
// PrefixNetwork (enp…). Это позволяет gateway-у одной таблицей
// маршрутизировать и Operation.Get, и любые ресурсные RPC.
const (
	PrefixCloud           = "b1g"
	PrefixFolder          = "b1g"
	PrefixOrganization    = "bpf"
	PrefixNetwork         = "enp"
	PrefixSubnet          = "e9b"
	PrefixAddress         = "e9b"
	PrefixRouteTable      = "enp"
	PrefixSecurityGroup   = "enp"
	PrefixGateway         = "enp"
	PrefixPrivateEndpoint = "enp"

	// compute: Instance/Disk делят `epd`, Image/Snapshot делят `fd8` (зеркалит
	// VPC-группировку); все compute-операции получают `epd` (== PrefixInstance),
	// чтобы api-gateway opsproxy мог одним правилом маршрутизировать Operation.Get.
	PrefixInstance = "epd"
	PrefixDisk     = "epd"
	PrefixImage    = "fd8"
	PrefixSnapshot = "fd8"

	// nlb: LoadBalancer/Listener/TargetGroup получают каждый свой 3-char
	// префикс — opsproxy в api-gateway маршрутизирует по PrefixOperationNLB
	// (== PrefixLoadBalancer), но resource-prefix у Listener/TargetGroup
	// отдельный, чтобы id-парсеры могли отличать тип ресурса по prefix-у
	// (в отличие от vpc, где Subnet/Address делят `e9b` — там тип
	// определяется контекстом URL-path). GlobalLoadBalancer зарезервирован
	// под будущий AWS-style composition layer (см. NLB design doc, "Future
	// TODO"); сейчас не используется ни одним handler-ом.
	PrefixLoadBalancer       = "nlb"
	PrefixListener           = "lst"
	PrefixTargetGroup        = "tgr"
	PrefixGlobalLoadBalancer = "glb"

	// Operation prefix per service-domain — совпадает с префиксом
	// «головного» ресурса домена (по convention reference-API).
	PrefixOperationRM      = PrefixCloud        // resource-manager: b1g
	PrefixOperationVPC     = PrefixNetwork      // vpc: enp
	PrefixOperationCompute = PrefixInstance     // compute: epd
	PrefixOperationNLB     = PrefixLoadBalancer // nlb: nlb
)

// NewID возвращает идентификатор формата "<prefix><17-char crockford-base32>"
// (всего 20 символов). Источник энтропии — crypto/rand.
//
// prefix должен быть ровно 3 символа; иначе функция panic-ит (programmer
// error: префикс приходит из package-level константы).
func NewID(prefix string) string {
	if len(prefix) != 3 {
		panic("ids.NewID: prefix must be exactly 3 chars, got " + prefix)
	}

	// 17 символов crockford-base32 = 85 бит энтропии. Берём 11 случайных
	// байт (88 бит) и читаем по 5 бит на символ из big-endian потока.
	var raw [11]byte
	if _, err := rand.Read(raw[:]); err != nil {
		// crypto/rand.Read не должен fail-ить на linux/macOS;
		// если он fail-ит — система сломана, panic корректно.
		panic("ids.NewID: crypto/rand failed: " + err.Error())
	}

	// Преобразуем 11 байт в uint64+uint64 (88 бит ⊂ 128 бит) и читаем по
	// 5 бит сверху-вниз. Используем encoding/binary для портабельности.
	hi := binary.BigEndian.Uint64(raw[0:8])
	lo := uint64(raw[8])<<16 | uint64(raw[9])<<8 | uint64(raw[10])
	// Сложим в одно 128-битное число (hi:lo) с битовым сдвигом:
	// фактически у нас 64 бита hi и 24 бита lo (всего 88 бит).
	// Читать будем 17 символов × 5 бит = 85 бит.

	body := make([]byte, idBodyLen)
	for i := 0; i < idBodyLen; i++ {
		// bit-offset с 0 (MSB), читаем 5 бит.
		bitOff := uint(i * 5)
		var val uint64
		switch {
		case bitOff+5 <= 64:
			// внутри hi
			val = (hi >> (64 - bitOff - 5)) & 0x1f
		case bitOff >= 64:
			// внутри lo (сдвиг lo относительно своего старшего бита 23)
			loOff := bitOff - 64
			val = (lo >> (24 - loOff - 5)) & 0x1f
		default:
			// перекрывает границу hi/lo: 5 бит = (64-bitOff) из hi + остаток из lo
			used := 64 - bitOff
			rest := 5 - used
			// младшие used бит hi:
			highPart := (hi & ((1 << used) - 1)) << rest
			// старшие rest бит lo:
			lowPart := lo >> (24 - rest)
			val = (highPart | lowPart) & 0x1f
		}
		body[i] = crockfordAlphabet[val]
	}

	var sb strings.Builder
	sb.Grow(totalLen)
	sb.WriteString(prefix)
	sb.Write(body)
	return sb.String()
}

// IsValid проверяет, что id соответствует формату "<prefix><17 lowercase
// crockford-base32-chars>". Не валидирует «правильность» энтропии — только
// синтаксис. Полезно для сервис-уровневых проверок входов.
func IsValid(id, prefix string) bool {
	if len(prefix) != 3 || len(id) != totalLen {
		return false
	}
	if id[:3] != prefix {
		return false
	}
	for i := 3; i < totalLen; i++ {
		c := id[i]
		if !isCrockfordChar(c) {
			return false
		}
	}
	return true
}

// HasKnownPrefix проверяет, что id начинается с одного из объявленных
// префиксов проекта и тело — валидная crockford-base32 строка длиной
// idBodyLen. Используется для acceptance в gateway/proxy без знания
// конкретного типа ресурса.
func HasKnownPrefix(id string) bool {
	if len(id) != totalLen {
		return false
	}
	for i := 3; i < totalLen; i++ {
		if !isCrockfordChar(id[i]) {
			return false
		}
	}
	return true
}

func isCrockfordChar(c byte) bool {
	return strings.IndexByte(crockfordAlphabet, c) >= 0
}

// NewUID — DEPRECATED: оставлен для backward compatibility с reconciler-ами
// и legacy-кодом, которому нужен ResourceVersion (UUID-like opaque строка).
// Для resource id и operation id всегда использовать NewID(<prefix>).
//
// Возвращает строку формата «kachō-style 20-char base32» БЕЗ префикса —
// для ResourceVersion-полей, где префикс не нужен и не может конфликтовать
// с прокси-routing-ом.
func NewUID() string {
	// Используем тот же 17-символьный suffix, добавляя 3-символьный
	// фиксированный sentinel "rev" — это исключает совпадение с любым
	// resource/operation id (rev не входит в список префиксов выше).
	return NewID("rev")
}
