// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package authz

import (
	"context"
	"testing"

	"github.com/PRO-Robotech/kacho-corelib/operations"
)

// TestDefaultSubjectExtractor_RejectsMalformedPrincipalID — principalID,
// приходящий из недоверенного x-kacho-principal-id header'а, содержащий
// FGA-разделители, должен трактоваться как anonymous (ok=false → interceptor
// fail-closed deny), а не собираться в subject-строку с инъекцией.
func TestDefaultSubjectExtractor_RejectsMalformedPrincipalID(t *testing.T) {
	for _, id := range []string{"usr_x#member", "usr_x:usr_y", "usr_x y", "usr_x\ty", "usr_x\ny", "usr_x@e"} {
		ctx := operations.WithPrincipal(context.Background(), operations.Principal{
			Type: "user", ID: id, DisplayName: id,
		})
		if _, _, ok := defaultSubjectExtractor(ctx); ok {
			t.Fatalf("malformed principal id %q must yield ok=false (fail-closed), got ok=true", id)
		}
	}
}

// TestDefaultSubjectExtractor_AcceptsValidPrincipalID — корректный принципал
// проходит (ok=true) и даёт правильный subject.
func TestDefaultSubjectExtractor_AcceptsValidPrincipalID(t *testing.T) {
	ctx := operations.WithPrincipal(context.Background(), operations.Principal{
		Type: "user", ID: "usr_alice", DisplayName: "alice",
	})
	subj, pid, ok := defaultSubjectExtractor(ctx)
	if !ok || subj != "user:usr_alice" || pid != "usr_alice" {
		t.Fatalf("valid principal: got (%q,%q,%v), want (user:usr_alice,usr_alice,true)", subj, pid, ok)
	}
}

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
			got := isAnonymousSubject(context.Background(), stubExtract(tc.subjectFGA, tc.principalID, tc.ok))
			if got != tc.want {
				t.Fatalf("isAnonymousSubject(subject=%q, principalID=%q, ok=%v) = %v, want %v",
					tc.subjectFGA, tc.principalID, tc.ok, got, tc.want)
			}
		})
	}
}
