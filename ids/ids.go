// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// Package ids — генератор short-form идентификаторов ресурсов в формате
// "<3-char prefix><17-char crockford-base32>" (всего 20 символов).
//
// Короткие непрозрачные id с per-domain-префиксом: ресурсы и операции
// идентифицируются короткими строками с префиксом домена, что позволяет
// gateway-у маршрутизировать запросы к нужному backend по первому сегменту id.
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
//   - resource-manager (legacy): cloud, folder, organization
//   - vpc: network, subnet, address, route_table, security_group, gateway,
//     network_interface, address_pool
//
// Каждый VPC-ресурс получает СВОЙ 3-char prefix. Тип ресурса читается по id
// (как в NLB-домене). Routing не ломается: resource-RPC маршрутизируются по
// REST-path, а НЕ по id-prefix.
//
// Operation.id остается с ОТДЕЛЬНЫМ per-domain prefix (PrefixOperationVPC =
// `enp`, PrefixOperationCompute = `epd`, …) — gateway opsproxy маршрутизирует
// Operation.Get по первым 3 символам id, поэтому op-prefix должен быть
// стабильным per-домен (а не per-ресурс).
const (
	PrefixCloud         = "b1g"
	PrefixFolder        = "b1g"
	PrefixOrganization  = "bpf"
	PrefixNetwork       = "net"
	PrefixSubnet        = "sub"
	PrefixAddress       = "adr"
	PrefixRouteTable    = "rtb"
	PrefixSecurityGroup = "sgr"
	PrefixGateway       = "gtw"

	// NetworkInterface и AddressPool — собственные prefix'ы, централизованные здесь.
	PrefixNetworkInterface = "nic"
	PrefixAddressPool      = "apl"

	// AnycastAddressPool — tenant-facing пул anycast-VIP, привязываемый к Network
	// (M:N); собственный resource-prefix, чтобы id-парсеры отличали его от обычного
	// AddressPool (`apl`).
	PrefixAnycastPool = "aap"

	// compute: Instance/Disk делят `epd`, Image/Snapshot делят `fd8` (зеркалит
	// VPC-группировку); все compute-операции получают `epd` (== PrefixInstance),
	// чтобы api-gateway opsproxy мог одним правилом маршрутизировать Operation.Get.
	PrefixInstance = "epd"
	PrefixDisk     = "epd"
	PrefixImage    = "fd8"
	PrefixSnapshot = "fd8"

	// storage (kacho-storage): собственный storage-домен, отдельный от compute.
	// Volume — block-volume нового домена (`vol`); это НЕ epd-Disk, а отдельный
	// ресурс со своим prefix'ом. StorageSnapshot — snapshot storage-домена
	// (`snp`), отдельно от compute PrefixSnapshot (`fd8`). DiskType — человеко-
	// читаемый slug (НЕ NewID-prefix), поэтому своей константы не требует.
	PrefixVolume          = "vol"
	PrefixStorageSnapshot = "snp"

	// nlb: LoadBalancer/Listener/TargetGroup получают каждый свой 3-char
	// префикс — opsproxy в api-gateway маршрутизирует по PrefixOperationNLB
	// (== PrefixLoadBalancer), но resource-prefix у Listener/TargetGroup
	// отдельный, чтобы id-парсеры могли отличать тип ресурса по prefix-у
	// (в отличие от vpc, где Subnet/Address делят `e9b` — там тип
	// определяется контекстом URL-path).
	PrefixLoadBalancer = "nlb"
	PrefixListener     = "lst"
	PrefixTargetGroup  = "tgr"

	// apps (PaaS): Application получает свой 3-char resource-prefix `app`;
	// apps-домен Operation получает отдельный стабильный op-prefix `aop`
	// (декаплен от ресурса, как enp/epd) — api-gateway opsproxy маршрутизирует
	// Operation.Get по первым 3 символам.
	PrefixApplication = "app"

	// registry: Registry (namespace OCI-реестра поверх zot) получает `reg` как
	// resource-prefix; registry-домен Operation — отдельный стабильный op-prefix
	// `rop` (декаплен от ресурса), по которому api-gateway opsproxy маршрутизирует
	// Operation.Get к kacho-registry. Repository/Tag — read-only проекция из zot,
	// собственного id-prefix не имеют (адресуются именем внутри namespace).
	PrefixRegistry     = "reg"
	PrefixOperationReg = "rop"

	// Operation prefix per service-domain — отдельный, стабильный per-домен
	// prefix, по которому gateway opsproxy маршрутизирует Operation.Get.
	//
	// PrefixOperationVPC зафиксирован как `enp` (vpc-op-root), декаплен от
	// PrefixNetwork — opsproxy.prefixToBackend["enp"]="vpc" остается неизменным,
	// существующие enp-операции в БД продолжают роутиться.
	PrefixOperationRM      = PrefixCloud        // resource-manager (legacy): b1g
	PrefixOperationVPC     = "enp"              // vpc op-root (декаплен от PrefixNetwork)
	PrefixOperationCompute = PrefixInstance     // compute: epd
	PrefixOperationNLB     = PrefixLoadBalancer // nlb: nlb
	PrefixOperationApps    = "aop"              // apps op-root (декаплен от PrefixApplication)
	PrefixOperationStorage = "sop"              // storage op-root (декаплен от PrefixVolume; opsproxy sop→storage)
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

	// 17 символов crockford-base32 = 85 бит энтропии. Берем 11 случайных
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

// domainStringPrefixes — 3-символьные prefix'ы доменов, чьи prefix-КОНСТАНТЫ
// живут не в ids, а в internal/ соответствующего сервиса (kacho-iam — downstream
// в build-графе, его internal-константы сюда не импортируются, см. запрет
// «internal не importable»). Перечислены литералами, чтобы ids оставался ЕДИНЫМ
// источником истины и для HasKnownPrefix, и для validate.baseResourceIDPrefixes
// (оба выводятся из knownPrefixes / KnownPrefixes()). Плюс legacy shared vpc
// prefix e9b (backward-compat для id переходного периода).
var domainStringPrefixes = []string{
	// iam: Account/Project/User/ServiceAccount/Group/Role/AccessBinding/Operation/UserOAuthClient
	"acc", "prj", "usr", "sva", "grp", "rol", "acb", "iop", "uoc",
	// legacy shared vpc prefix
	"e9b",
}

// allKnownPrefixValues — ЕДИНСТВЕННЫЙ источник истины: значения всех объявленных
// prefix-констант ids (дубли по значению — b1g/epd/fd8/nlb/enp — представлены
// один раз) плюс domainStringPrefixes. knownPrefixes и KnownPrefixes() строятся
// отсюда; guard-тест (ids_test) сверяет, что каждая Prefix*-константа входит в
// набор, — это ловит drift без ручной синхронизации отдельной map'ы.
func allKnownPrefixValues() []string {
	vals := []string{
		PrefixCloud, PrefixOrganization, PrefixNetwork, PrefixSubnet, PrefixAddress,
		PrefixRouteTable, PrefixSecurityGroup, PrefixGateway, PrefixNetworkInterface,
		PrefixAddressPool, PrefixAnycastPool, PrefixInstance, PrefixImage,
		PrefixVolume, PrefixStorageSnapshot,
		PrefixLoadBalancer, PrefixListener, PrefixTargetGroup,
		PrefixApplication, PrefixRegistry,
		PrefixOperationVPC, PrefixOperationApps, PrefixOperationReg, PrefixOperationStorage,
	}
	return append(vals, domainStringPrefixes...)
}

// knownPrefixes — множество всех известных 3-символьных префиксов проекта,
// выведенное из allKnownPrefixValues(). Источник истины для HasKnownPrefix.
var knownPrefixes = func() map[string]struct{} {
	m := make(map[string]struct{})
	for _, p := range allKnownPrefixValues() {
		m[p] = struct{}{}
	}
	return m
}()

// KnownPrefixes возвращает КОПИЮ множества известных префиксов — потребители
// (напр. validate.baseResourceIDPrefixes) строят свой набор поверх этого, не
// дублируя список литералов и не рискуя drift'ом с HasKnownPrefix.
func KnownPrefixes() map[string]struct{} {
	m := make(map[string]struct{}, len(knownPrefixes))
	for p := range knownPrefixes {
		m[p] = struct{}{}
	}
	return m
}

// HasKnownPrefix проверяет, что id имеет валидную форму ресурс-id: ровно
// totalLen символов, 3-символьный префикс входит в множество объявленных
// префиксов проекта (knownPrefixes), а тело — валидная crockford-base32 строка
// длиной idBodyLen. Используется для acceptance в gateway/proxy без знания
// конкретного типа ресурса.
func HasKnownPrefix(id string) bool {
	if len(id) != totalLen {
		return false
	}
	if _, ok := knownPrefixes[id[:3]]; !ok {
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
