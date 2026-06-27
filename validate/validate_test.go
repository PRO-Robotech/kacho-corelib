// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package validate

import (
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestPageSize(t *testing.T) {
	cases := []struct {
		name      string
		input     int64
		wantEff   int64
		wantErr   bool
		wantField string
	}{
		{name: "zero -> default", input: 0, wantEff: 50, wantErr: false},
		{name: "one -> one", input: 1, wantEff: 1, wantErr: false},
		{name: "max valid 1000", input: 1000, wantEff: 1000, wantErr: false},
		{name: "negative", input: -1, wantErr: true, wantField: "page_size"},
		{name: "min-overflow", input: -100, wantErr: true, wantField: "page_size"},
		{name: "max+1", input: 1001, wantErr: true, wantField: "page_size"},
		{name: "huge", input: 10000, wantErr: true, wantField: "page_size"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eff, err := PageSize("page_size", tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for input %d, got nil (eff=%d)", tc.input, eff)
				}
				st, ok := status.FromError(err)
				if !ok {
					t.Fatalf("expected gRPC status, got: %v", err)
				}
				if st.Code() != codes.InvalidArgument {
					t.Fatalf("expected InvalidArgument, got %v", st.Code())
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if eff != tc.wantEff {
				t.Fatalf("expected eff=%d, got %d", tc.wantEff, eff)
			}
		})
	}
}

func TestNameVPC(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{name: "empty", input: "", wantErr: false},
		{name: "lowercase", input: "abc"},
		{name: "uppercase", input: "BadCAPS"},
		{name: "underscore", input: "abc_def"},
		{name: "hyphen", input: "abc-def"},
		{name: "mixed", input: "Abc_Def-09"},
		{name: "max-63", input: strings.Repeat("a", 63), wantErr: false},
		{name: "starts-with-digit", input: "1bad", wantErr: true},
		{name: "starts-with-hyphen", input: "-bad", wantErr: true},
		{name: "starts-with-underscore", input: "_bad", wantErr: true},
		{name: "ends-with-hyphen", input: "abc-", wantErr: true},
		{name: "too-long-64", input: strings.Repeat("a", 64), wantErr: true},
		{name: "slash", input: "bad/slash", wantErr: true},
		{name: "dot", input: "bad.dot", wantErr: true},
		{name: "single-letter", input: "x", wantErr: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := NameVPC("name", tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q, got nil", tc.input)
				}
				st, ok := status.FromError(err)
				if !ok {
					t.Fatalf("expected gRPC status, got: %v", err)
				}
				if st.Code() != codes.InvalidArgument {
					t.Fatalf("expected InvalidArgument, got %v", st.Code())
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", tc.input, err)
			}
		})
	}
}

func TestZoneId(t *testing.T) {
	// ZoneId — required-only валидация. Existence-проверка вынесена в сервис
	// (kacho-vpc обращается к таблице `zones`). Любая непустая строка
	// проходит format-уровень; existence отвергается асинхронно сервисом.
	cases := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{name: "empty", input: "", wantErr: true},
		{name: "any-non-empty-a", input: "region-1-a", wantErr: false},
		{name: "any-non-empty-other-region", input: "region-2-a", wantErr: false},
		{name: "any-non-empty-garbage", input: "invalid-zone", wantErr: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ZoneId("zone_id", tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q, got nil", tc.input)
				}
				st, ok := status.FromError(err)
				if !ok {
					t.Fatalf("expected gRPC status, got: %v", err)
				}
				if st.Code() != codes.InvalidArgument {
					t.Fatalf("expected InvalidArgument, got %v", st.Code())
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", tc.input, err)
			}
		})
	}
}
