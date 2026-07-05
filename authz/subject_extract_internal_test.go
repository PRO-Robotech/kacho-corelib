// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package authz

import (
	"context"
	"testing"
)

// stubExtract возвращает фиксированный (subjectFGA, principalID, ok) — позволяет
// напрямую подать каждую комбинацию closed-list'а в isAnonymousSubject без
// конструирования реального ctx/Principal'а.
func stubExtract(subjectFGA, principalID string, ok bool) func(context.Context) (string, string, bool) {
	return func(context.Context) (string, string, bool) {
		return subjectFGA, principalID, ok
	}
}

// TestIsAnonymousSubject_ClosedList — table-driven негативный тест для каждого
// arm'а closed-list'а. Это единственный guard, который под Breakglass=true
// удерживает anonymous/bootstrap-принципалов от прохода (interceptor.go:198-206).
// Каждая строка бьёт по отдельному arm'у: удаление/опечатка любого arm'а роняет
// соответствующую строку (mutation-catch).
func TestIsAnonymousSubject_ClosedList(t *testing.T) {
	cases := []struct {
		name        string
		subjectFGA  string
		principalID string
		ok          bool
		want        bool
	}{
		// extract сигналит "нет принципала" → anonymous.
		{name: "not-ok", subjectFGA: "", principalID: "", ok: false, want: true},
		// ok=true, но принципал вырожденный.
		{name: "empty-principal-id", subjectFGA: "user:", principalID: "", ok: true, want: true},
		{name: "empty-subject", subjectFGA: "", principalID: "usr_x", ok: true, want: true},
		// principal_id closed-list (api-gateway injectAnonymous / bootstrap fallback).
		{name: "principal-anonymous", subjectFGA: "user:anonymous", principalID: "anonymous", ok: true, want: true},
		{name: "principal-bootstrap", subjectFGA: "user:bootstrap", principalID: "bootstrap", ok: true, want: true},
		// subject closed-list (extractor, отдающий system:* subject напрямую).
		{name: "subject-system-anonymous", subjectFGA: "system:anonymous", principalID: "sysid", ok: true, want: true},
		{name: "subject-system-bootstrap", subjectFGA: "system:bootstrap", principalID: "sysid", ok: true, want: true},
		// Настоящие аутентифицированные принципалы — НЕ anonymous.
		{name: "genuine-user", subjectFGA: "user:usr_alice", principalID: "usr_alice", ok: true, want: false},
		{name: "genuine-service-account", subjectFGA: "service_account:sva_x", principalID: "sva_x", ok: true, want: false},
		// Принципал, чей id лишь содержит "anonymous" как подстроку, но не равен — НЕ anonymous.
		{name: "principal-id-superstring", subjectFGA: "user:anonymous_ish", principalID: "anonymous_ish", ok: true, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isAnonymousSubject(stubExtract(tc.subjectFGA, tc.principalID, tc.ok), context.Background())
			if got != tc.want {
				t.Fatalf("isAnonymousSubject(subject=%q, principalID=%q, ok=%v) = %v, want %v",
					tc.subjectFGA, tc.principalID, tc.ok, got, tc.want)
			}
		})
	}
}
