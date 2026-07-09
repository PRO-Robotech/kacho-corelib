// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package validate

// Табличные unit-тесты для общих валидационных примитивов, которые ранее не
// имели покрытия (Name/NameCompute/NameGateway/Description/Labels/IPAddress/
// DhcpDomainName/DdosProvider/SmtpCapability/UpdateMask). Эти функции несут
// security-/parity-критичные контракты (regex label-key, DNS-name, IP-parse,
// DDoS/SMTP whitelist, update_mask discipline: неизвестное поле → InvalidArgument).
// Чистые функции, Postgres не нужен.

import (
	"strings"
	"testing"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// requireInvalidArgument проверяет, что err — gRPC status InvalidArgument.
func requireInvalidArgument(t *testing.T, err error, ctx string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: expected error, got nil", ctx)
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("%s: expected gRPC status, got: %v", ctx, err)
	}
	if st.Code() != codes.InvalidArgument {
		t.Fatalf("%s: expected InvalidArgument, got %v", ctx, st.Code())
	}
}

func TestName_Strict(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{name: "empty-rejected", input: "", wantErr: true}, // strict: пустое не матчит (нужна ≥1 буква)
		{name: "single-letter-ok", input: "a"},
		{name: "lowercase-hyphen-digit", input: "abc-09"},
		{name: "max-63", input: strings.Repeat("a", 63)},
		{name: "uppercase-rejected", input: "Abc", wantErr: true},
		{name: "underscore-rejected", input: "a_b", wantErr: true},
		{name: "starts-digit-rejected", input: "1ab", wantErr: true},
		{name: "ends-hyphen-rejected", input: "ab-", wantErr: true},
		{name: "too-long-64", input: strings.Repeat("a", 64), wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Name("name", tc.input)
			if tc.wantErr {
				requireInvalidArgument(t, err, tc.input)
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", tc.input, err)
			}
		})
	}
}

func TestNameCompute(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{name: "empty-ok", input: ""},
		{name: "lowercase", input: "abc"},
		{name: "underscore-ok", input: "abc_def"},
		{name: "hyphen-ok", input: "abc-def"},
		{name: "max-63", input: strings.Repeat("a", 63)},
		{name: "uppercase-rejected", input: "BadCAPS", wantErr: true},
		{name: "starts-digit-rejected", input: "1bad", wantErr: true},
		{name: "starts-underscore-rejected", input: "_bad", wantErr: true},
		{name: "ends-hyphen-rejected", input: "bad-", wantErr: true},
		{name: "too-long-64", input: strings.Repeat("a", 64), wantErr: true},
		{name: "slash-rejected", input: "a/b", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := NameCompute("name", tc.input)
			if tc.wantErr {
				requireInvalidArgument(t, err, tc.input)
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", tc.input, err)
			}
		})
	}
}

func TestNameGateway(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{name: "empty-ok", input: ""},
		{name: "lowercase-digit-hyphen", input: "gw-1"},
		{name: "single-letter", input: "g"},
		{name: "max-63", input: strings.Repeat("a", 63)},
		{name: "uppercase-rejected", input: "GW", wantErr: true},
		{name: "underscore-rejected", input: "gw_1", wantErr: true},
		{name: "starts-digit-rejected", input: "1gw", wantErr: true},
		{name: "ends-hyphen-rejected", input: "gw-", wantErr: true},
		{name: "too-long-64", input: strings.Repeat("a", 64), wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := NameGateway("name", tc.input)
			if tc.wantErr {
				requireInvalidArgument(t, err, tc.input)
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", tc.input, err)
			}
		})
	}
}

func TestDescription(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{name: "empty-ok", input: ""},
		{name: "at-limit-256", input: strings.Repeat("x", 256)},
		{name: "over-limit-257", input: strings.Repeat("x", 257), wantErr: true},
		{name: "unicode-256-runes-ok", input: strings.Repeat("é", 256)}, // rune count, not bytes
		{name: "unicode-257-runes-rejected", input: strings.Repeat("é", 257), wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Description("description", tc.input)
			if tc.wantErr {
				requireInvalidArgument(t, err, tc.name)
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for %s: %v", tc.name, err)
			}
		})
	}
}

func TestLabels(t *testing.T) {
	// >64 уникальных валидных ключей → триггерит "too many labels".
	tooMany := make(map[string]string, MaxLabels+1)
	for i := 0; i < MaxLabels+1; i++ {
		tooMany["key"+strings.Repeat("a", i)] = "v"
	}

	cases := []struct {
		name    string
		labels  map[string]string
		wantErr bool
	}{
		{name: "empty-ok", labels: map[string]string{}},
		{name: "valid-simple", labels: map[string]string{"env": "prod", "team-a": "x"}},
		{name: "valid-special-key-chars", labels: map[string]string{"a-_./@0": ""}},
		{name: "value-at-63-ok", labels: map[string]string{"k": strings.Repeat("v", MaxLabelValueLen)}},
		{name: "too-many-labels", labels: tooMany, wantErr: true},
		{name: "empty-key-rejected", labels: map[string]string{"": "v"}, wantErr: true},
		{name: "key-starts-digit-rejected", labels: map[string]string{"1bad": "v"}, wantErr: true},
		{name: "key-uppercase-rejected", labels: map[string]string{"Bad": "v"}, wantErr: true},
		{name: "key-too-long-64", labels: map[string]string{strings.Repeat("a", 64): "v"}, wantErr: true},
		{name: "value-too-long-64", labels: map[string]string{"k": strings.Repeat("v", 64)}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Labels("labels", tc.labels)
			if tc.wantErr {
				requireInvalidArgument(t, err, tc.name)
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for %s: %v", tc.name, err)
			}
		})
	}
}

// TestLabels_InvalidKeyMessageListsAtSign локает contract-текст FieldViolation:
// labelKeyRe допускает '@' в ключе (см. valid-special-key-chars), поэтому
// user-facing allowed-set в сообщении обязан перечислять '@'. Без этого doc/msg
// расходятся с regex (doc-truthfulness): контрибьютор, сверяя код с сообщением,
// удалил бы '@' из regex и молча отверг ранее-валидные ключи вроде `owner@team`.
func TestLabels_InvalidKeyMessageListsAtSign(t *testing.T) {
	err := Labels("labels", map[string]string{"Bad": "v"}) // uppercase → invalid key
	requireInvalidArgument(t, err, "invalid-key")
	st, _ := status.FromError(err)
	var desc string
	for _, d := range st.Details() {
		if br, ok := d.(*errdetails.BadRequest); ok {
			for _, fv := range br.GetFieldViolations() {
				desc = fv.GetDescription()
			}
		}
	}
	if !strings.Contains(desc, "@") {
		t.Fatalf("label-key FieldViolation must list '@' in allowed set (regex allows it), got: %q", desc)
	}
}

func TestIPAddress(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{name: "ipv4-ok", input: "10.0.0.1"},
		{name: "ipv4-zeros-ok", input: "0.0.0.0"},
		{name: "ipv6-ok", input: "2001:db8::1"},
		{name: "not-an-ip", input: "not-an-ip", wantErr: true},
		{name: "hostname-rejected", input: "pool.ntp.org", wantErr: true},
		{name: "cidr-rejected", input: "10.0.0.0/8", wantErr: true},
		{name: "empty-rejected", input: "", wantErr: true},
		{name: "octet-overflow-rejected", input: "10.0.0.256", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := IPAddress("addr", tc.input)
			if tc.wantErr {
				requireInvalidArgument(t, err, tc.input)
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", tc.input, err)
			}
		})
	}
}

func TestDhcpDomainName(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{name: "empty-ok", input: ""},
		{name: "simple", input: "example.com"},
		{name: "single-label", input: "localhost"},
		{name: "digits-and-hyphen", input: "a1-b2.c3-d4.example"},
		{name: "over-253-rejected", input: strings.Repeat("a", 254), wantErr: true},
		{name: "leading-dot-rejected", input: ".example.com", wantErr: true},
		{name: "double-dot-rejected", input: "a..b", wantErr: true},
		{name: "underscore-rejected", input: "bad_label.com", wantErr: true},
		{name: "label-ends-hyphen-rejected", input: "bad-.com", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := DhcpDomainName("domain_name", tc.input)
			if tc.wantErr {
				requireInvalidArgument(t, err, tc.input)
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", tc.input, err)
			}
		})
	}
}

func TestDdosProvider(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{name: "empty-ok", input: ""},
		{name: "qrator-ok", input: "qrator"},
		{name: "advanced-ok", input: "advanced"},
		{name: "unknown-rejected", input: "acme", wantErr: true},
		{name: "case-sensitive-rejected", input: "Qrator", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := DdosProvider("ddos_protection_provider", tc.input)
			if tc.wantErr {
				requireInvalidArgument(t, err, tc.input)
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", tc.input, err)
			}
		})
	}
}

func TestSmtpCapability(t *testing.T) {
	// Контракт: любое непустое значение отвергается (tenant не может включить SMTP).
	if err := SmtpCapability("outgoing_smtp_capability", ""); err != nil {
		t.Fatalf("empty must be OK, got: %v", err)
	}
	for _, v := range []string{"enabled", "true", "1", "on"} {
		requireInvalidArgument(t, SmtpCapability("outgoing_smtp_capability", v), v)
	}
}

func TestUpdateMask(t *testing.T) {
	known := map[string]struct{}{"name": {}, "description": {}, "labels": {}}

	t.Run("empty-mask-ok", func(t *testing.T) {
		if err := UpdateMask("update_mask", nil, known); err != nil {
			t.Fatalf("empty mask must be OK, got: %v", err)
		}
		if err := UpdateMask("update_mask", []string{}, known); err != nil {
			t.Fatalf("zero-len mask must be OK, got: %v", err)
		}
	})

	t.Run("all-known-ok", func(t *testing.T) {
		if err := UpdateMask("update_mask", []string{"name", "labels"}, known); err != nil {
			t.Fatalf("known fields must pass, got: %v", err)
		}
	})

	t.Run("unknown-field-rejected", func(t *testing.T) {
		requireInvalidArgument(t, UpdateMask("update_mask", []string{"name", "bogus"}, known), "bogus")
	})

	t.Run("single-unknown-rejected", func(t *testing.T) {
		requireInvalidArgument(t, UpdateMask("update_mask", []string{"zone_id"}, known), "zone_id")
	})
}
