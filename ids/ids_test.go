// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package ids

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewID_Length(t *testing.T) {
	id := NewID(PrefixNetwork)
	require.Len(t, id, 20)
}

func TestNewID_PrefixApplied(t *testing.T) {
	id := NewID(PrefixCloud)
	require.True(t, strings.HasPrefix(id, PrefixCloud), "id %q must start with %q", id, PrefixCloud)
}

func TestNewID_BodyIsCrockfordBase32(t *testing.T) {
	id := NewID(PrefixSubnet)
	body := id[3:]
	require.Len(t, body, 17)
	for i := 0; i < len(body); i++ {
		require.True(t, isCrockfordChar(body[i]),
			"body[%d]=%q is not crockford-base32 char (id=%q)", i, body[i], id)
	}
}

func TestNewID_Unique(t *testing.T) {
	seen := make(map[string]bool, 10000)
	for i := 0; i < 10000; i++ {
		id := NewID(PrefixNetwork)
		require.False(t, seen[id], "duplicate id %q at iter %d", id, i)
		seen[id] = true
	}
}

func TestNewID_PanicsOnBadPrefix(t *testing.T) {
	require.Panics(t, func() { NewID("ab") })
	require.Panics(t, func() { NewID("abcd") })
	require.Panics(t, func() { NewID("") })
}

func TestIsValid_OK(t *testing.T) {
	id := NewID(PrefixNetwork)
	require.True(t, IsValid(id, PrefixNetwork))
}

func TestIsValid_WrongPrefix(t *testing.T) {
	id := NewID(PrefixNetwork)
	require.False(t, IsValid(id, PrefixSubnet))
}

func TestIsValid_WrongLength(t *testing.T) {
	require.False(t, IsValid("enp123", PrefixNetwork))
	require.False(t, IsValid("enp"+strings.Repeat("a", 18), PrefixNetwork))
}

func TestIsValid_BadChars(t *testing.T) {
	// I, L, O, U — запрещены crockford
	require.False(t, IsValid("enp"+strings.Repeat("i", 17), PrefixNetwork))
	require.False(t, IsValid("enp"+strings.Repeat("l", 17), PrefixNetwork))
	require.False(t, IsValid("enp"+strings.Repeat("o", 17), PrefixNetwork))
	require.False(t, IsValid("enp"+strings.Repeat("u", 17), PrefixNetwork))
	// uppercase запрещен в нашей нормализации
	require.False(t, IsValid("ENP"+strings.Repeat("a", 17), PrefixNetwork))
}

func TestHasKnownPrefix_AcceptsValid(t *testing.T) {
	for _, p := range []string{
		PrefixCloud, PrefixFolder, PrefixOrganization,
		PrefixNetwork, PrefixSubnet, PrefixAddress,
		PrefixRouteTable, PrefixSecurityGroup, PrefixGateway,
		PrefixNetworkInterface, PrefixAddressPool, PrefixAnycastPool,
		PrefixLoadBalancer, PrefixListener, PrefixTargetGroup,
	} {
		id := NewID(p)
		require.True(t, HasKnownPrefix(id), "id=%q (prefix=%q)", id, p)
	}
}

// NLB-prefixes должны проходить per-prefix IsValid и иметь корректную длину
// (20 chars). PrefixOperationNLB — alias на PrefixLoadBalancer, поэтому
// проверяем, что id, сгенерированный одним, валидируется как другой
// (api-gateway opsproxy маршрутизирует по этому свойству).
func TestIsValid_NLBPrefixes(t *testing.T) {
	for _, p := range []string{
		PrefixLoadBalancer, PrefixListener, PrefixTargetGroup,
	} {
		id := NewID(p)
		require.Len(t, id, 20, "prefix=%q", p)
		require.True(t, IsValid(id, p), "id=%q (prefix=%q)", id, p)
	}
}

func TestPrefixOperationNLB_AliasesLoadBalancer(t *testing.T) {
	require.Equal(t, PrefixLoadBalancer, PrefixOperationNLB,
		"opsproxy в api-gateway полагается на этот alias")
	id := NewID(PrefixOperationNLB)
	require.True(t, IsValid(id, PrefixLoadBalancer))
}

func TestHasKnownPrefix_RejectsBadShape(t *testing.T) {
	require.False(t, HasKnownPrefix("short"))
	require.False(t, HasKnownPrefix("enp_with_underscore"))
	require.False(t, HasKnownPrefix(""))
}

// TestKnownPrefixes_EveryConstantIsMember — guard против drift'а: КАЖДАЯ
// объявленная Prefix*-константа обязана входить в knownPrefixes. Это ловит
// повтор бага reg/rop (константа объявлена, но забыта в наборе → HasKnownPrefix
// ложно отвергал well-formed reg-id, расходясь с validate.ResourceID).
// Перечисление констант compiler-checked (rename/remove роняет компиляцию).
func TestKnownPrefixes_EveryConstantIsMember(t *testing.T) {
	consts := map[string]string{
		"PrefixCloud":            PrefixCloud,
		"PrefixFolder":           PrefixFolder,
		"PrefixOrganization":     PrefixOrganization,
		"PrefixNetwork":          PrefixNetwork,
		"PrefixSubnet":           PrefixSubnet,
		"PrefixAddress":          PrefixAddress,
		"PrefixRouteTable":       PrefixRouteTable,
		"PrefixSecurityGroup":    PrefixSecurityGroup,
		"PrefixGateway":          PrefixGateway,
		"PrefixNetworkInterface": PrefixNetworkInterface,
		"PrefixAddressPool":      PrefixAddressPool,
		"PrefixAnycastPool":      PrefixAnycastPool,
		"PrefixInstance":         PrefixInstance,
		"PrefixDisk":             PrefixDisk,
		"PrefixImage":            PrefixImage,
		"PrefixSnapshot":         PrefixSnapshot,
		"PrefixLoadBalancer":     PrefixLoadBalancer,
		"PrefixListener":         PrefixListener,
		"PrefixTargetGroup":      PrefixTargetGroup,
		"PrefixApplication":      PrefixApplication,
		"PrefixRegistry":         PrefixRegistry,
		"PrefixVolume":           PrefixVolume,
		"PrefixStorageSnapshot":  PrefixStorageSnapshot,
		"PrefixOperationStorage": PrefixOperationStorage,
		"PrefixOperationReg":     PrefixOperationReg,
		"PrefixOperationRM":      PrefixOperationRM,
		"PrefixOperationVPC":     PrefixOperationVPC,
		"PrefixOperationCompute": PrefixOperationCompute,
		"PrefixOperationNLB":     PrefixOperationNLB,
		"PrefixOperationApps":    PrefixOperationApps,
	}
	for name, val := range consts {
		if _, ok := knownPrefixes[val]; !ok {
			t.Errorf("%s = %q is declared but missing from knownPrefixes — "+
				"HasKnownPrefix would falsely reject well-formed %q ids", name, val, val)
		}
	}
	// reg/rop — регресс-гуард на конкретный найденный drift.
	require.True(t, HasKnownPrefix(NewID(PrefixRegistry)), "reg-id must be known")
	require.True(t, HasKnownPrefix(NewID(PrefixOperationReg)), "rop-id must be known")
}

// TestStoragePrefixes_DistinctAndKnown — storage-домен (kacho-storage) получает
// собственные prefix'ы: Volume (`vol`, block-volume, НЕ epd-Disk),
// StorageSnapshot (`snp`, отдельно от compute PrefixSnapshot `fd8`) и op-root
// `sop` (декаплен от ресурса, как enp/aop). Проверяем: NewID валиден через
// IsValid, HasKnownPrefix знает их, и они не совпадают ни с одним существующим
// resource/op-prefix'ом (в т.ч. compute epd/fd8).
func TestStoragePrefixes_DistinctAndKnown(t *testing.T) {
	require.Equal(t, "vol", PrefixVolume)
	require.Equal(t, "snp", PrefixStorageSnapshot)
	require.Equal(t, "sop", PrefixOperationStorage)

	// Storage-prefix'ы отличны от compute (Disk `epd`, Snapshot `fd8`) — это
	// отдельный домен, не переиспользование compute-констант.
	require.NotEqual(t, PrefixDisk, PrefixVolume, "vol must differ from compute Disk epd")
	require.NotEqual(t, PrefixSnapshot, PrefixStorageSnapshot, "snp must differ from compute Snapshot fd8")

	// Попарная уникальность storage-prefix'ов + отсутствие коллизий со всеми
	// уже известными префиксами проекта.
	known := KnownPrefixes()
	storage := []string{PrefixVolume, PrefixStorageSnapshot, PrefixOperationStorage}
	seen := map[string]bool{}
	for _, p := range storage {
		require.Lenf(t, p, 3, "storage prefix %q must be 3 chars", p)
		require.Falsef(t, seen[p], "duplicate storage prefix %q", p)
		seen[p] = true
	}

	// NewID → валидная форма, HasKnownPrefix true, registered в KnownPrefixes.
	for _, p := range storage {
		id := NewID(p)
		require.Lenf(t, id, 20, "prefix=%q", p)
		require.Truef(t, IsValid(id, p), "id %q must be valid for prefix %q", id, p)
		require.Truef(t, HasKnownPrefix(id), "id %q must pass HasKnownPrefix", id)
		_, ok := known[p]
		require.Truef(t, ok, "prefix %q must be registered in KnownPrefixes()", p)
	}
}

// TestKnownPrefixes_ReturnsCopy — KnownPrefixes() отдаёт копию: мутация
// результата не протекает в internal knownPrefixes.
func TestKnownPrefixes_ReturnsCopy(t *testing.T) {
	got := KnownPrefixes()
	got["zzz"] = struct{}{}
	if _, ok := knownPrefixes["zzz"]; ok {
		t.Fatalf("KnownPrefixes() must return a copy; mutation leaked into internal set")
	}
}

func TestNewUID_LegacyShapeStable(t *testing.T) {
	uid := NewUID()
	require.Len(t, uid, 20)
	require.True(t, strings.HasPrefix(uid, "rev"), "legacy NewUID has rev-prefix sentinel")
}

// TestVPCResourcePrefixes_DistinctKAC271 — каждый VPC-ресурс получает СВОЙ
// 3-char prefix. Operation-prefix VPC остается отдельным (`enp`) — gateway
// opsproxy маршрутизирует Operation.Get по нему, поэтому он не должен совпадать
// ни с одним ресурсным.
func TestVPCResourcePrefixes_DistinctKAC271(t *testing.T) {
	require.Equal(t, "net", PrefixNetwork)
	require.Equal(t, "sub", PrefixSubnet)
	require.Equal(t, "adr", PrefixAddress)
	require.Equal(t, "rtb", PrefixRouteTable)
	require.Equal(t, "sgr", PrefixSecurityGroup)
	require.Equal(t, "gtw", PrefixGateway)
	require.Equal(t, "nic", PrefixNetworkInterface)
	require.Equal(t, "apl", PrefixAddressPool)
	require.Equal(t, "aap", PrefixAnycastPool)

	// 9 ресурсных префиксов VPC — попарно различны.
	vpc := []string{
		PrefixNetwork, PrefixSubnet, PrefixAddress, PrefixRouteTable,
		PrefixSecurityGroup, PrefixGateway, PrefixNetworkInterface, PrefixAddressPool,
		PrefixAnycastPool,
	}
	seen := map[string]bool{}
	for _, p := range vpc {
		require.Lenf(t, p, 3, "prefix %q must be 3 chars", p)
		require.Falsef(t, seen[p], "duplicate VPC resource prefix %q", p)
		seen[p] = true
	}

	// Operation-prefix VPC — отдельный, routable в gateway opsproxy, и НЕ
	// совпадает ни с одним ресурсным VPC-префиксом.
	require.Equal(t, "enp", PrefixOperationVPC)
	require.Falsef(t, seen[PrefixOperationVPC],
		"PrefixOperationVPC %q must differ from every VPC resource prefix", PrefixOperationVPC)

	// Сгенерированные id с новыми префиксами — валидной формы и known-prefix.
	for _, p := range append(vpc, PrefixOperationVPC) {
		id := NewID(p)
		require.True(t, IsValid(id, p), "id %q must be valid for prefix %q", id, p)
		require.True(t, HasKnownPrefix(id), "id %q must pass HasKnownPrefix", id)
	}
}
