// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package auth_test

import (
	"context"
	"testing"

	"google.golang.org/grpc/metadata"

	"github.com/PRO-Robotech/kacho-corelib/auth"
	"github.com/PRO-Robotech/kacho-corelib/authz"
	"github.com/PRO-Robotech/kacho-corelib/grpcsrv"
	"github.com/PRO-Robotech/kacho-corelib/operations"
)

// TestPropagateOutgoing_WithPrincipal_AppendsMD — ctx with Principal →
// outgoing MD carries all three x-kacho-principal-* headers with expected
// values.
func TestPropagateOutgoing_WithPrincipal_AppendsMD(t *testing.T) {
	ctx := operations.WithPrincipal(context.Background(), operations.Principal{
		Type:        "user",
		ID:          "usr_alice",
		DisplayName: "alice@example.com",
	})

	out := auth.PropagateOutgoing(ctx)

	md, ok := metadata.FromOutgoingContext(out)
	if !ok {
		t.Fatal("expected outgoing metadata on returned ctx, got none")
	}
	if got := md.Get(grpcsrv.MDKeyPrincipalType); len(got) != 1 || got[0] != "user" {
		t.Errorf("MDKeyPrincipalType = %v, want [user]", got)
	}
	if got := md.Get(grpcsrv.MDKeyPrincipalID); len(got) != 1 || got[0] != "usr_alice" {
		t.Errorf("MDKeyPrincipalID = %v, want [usr_alice]", got)
	}
	if got := md.Get(grpcsrv.MDKeyPrincipalDisplay); len(got) != 1 || got[0] != "alice@example.com" {
		t.Errorf("MDKeyPrincipalDisplay = %v, want [alice@example.com]", got)
	}
}

// TestPropagateOutgoing_EmptyPrincipal_PassThroughUnchanged — fresh ctx with
// no Principal → no MD added, and the returned ctx must not panic on
// consumption.
//
// operations.PrincipalFromContext fallback'ит на SystemPrincipal() для пустого
// ctx; чтобы убедиться что helper НЕ форсит headers для системного
// fallback'а, проверяем явно: empty MD не добавляется только когда principal
// "пустой" (Type=="" && ID==""). SystemPrincipal сам по себе непуст
// (Type="system", ID="bootstrap") → headers форсятся — это ОЖИДАЕМО (worker
// peer-call'ы должны быть атрибутируемы как system, а не вообще без identity).
//
// Этот тест проверяет реально пустой Principal — не путь через PrincipalFromContext.
func TestPropagateOutgoing_EmptyPrincipal_PassThroughUnchanged(t *testing.T) {
	ctx := operations.WithPrincipal(context.Background(), operations.Principal{})

	out := auth.PropagateOutgoing(ctx)

	md, ok := metadata.FromOutgoingContext(out)
	if ok && len(md) > 0 {
		t.Errorf("empty Principal must not add outgoing MD, got %v", md)
	}
}

// TestPropagateOutgoing_NilCtx_NoPanic — defensive: helper не паникует на nil.
func TestPropagateOutgoing_NilCtx_NoPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("PropagateOutgoing(nil) panicked: %v", r)
		}
	}()
	//nolint:staticcheck // тестируем defensive-обработку nil
	_ = auth.PropagateOutgoing(nil)
}

// TestPropagateOutgoing_SystemPrincipalFallback — ctx без явного WithPrincipal
// идет через PrincipalFromContext → возвращает SystemPrincipal() (Type=system,
// ID=bootstrap). Это непустой principal, headers ДОЛЖНЫ форситься (worker
// должен быть атрибутируем).
func TestPropagateOutgoing_SystemPrincipalFallback(t *testing.T) {
	out := auth.PropagateOutgoing(context.Background())

	md, ok := metadata.FromOutgoingContext(out)
	if !ok {
		t.Fatal("expected SystemPrincipal fallback to produce outgoing MD")
	}
	if got := md.Get(grpcsrv.MDKeyPrincipalID); len(got) != 1 || got[0] != "bootstrap" {
		t.Errorf("MDKeyPrincipalID = %v, want [bootstrap] (SystemPrincipal fallback)", got)
	}
	if got := md.Get(grpcsrv.MDKeyPrincipalType); len(got) != 1 || got[0] != "system" {
		t.Errorf("MDKeyPrincipalType = %v, want [system] (SystemPrincipal fallback)", got)
	}
}

// TestSystemPrincipalFor_FormatsCorrectly — builds attributable system
// principal for worker/reconciler contexts.
func TestSystemPrincipalFor_FormatsCorrectly(t *testing.T) {
	p := auth.SystemPrincipalFor("vpc", "reconciler")

	if p.Type != "user" {
		t.Errorf("Type = %q, want %q (FGA tuples key on user-typed subjects)", p.Type, "user")
	}
	if p.ID != "system.vpc-reconciler" {
		t.Errorf("ID = %q, want %q", p.ID, "system.vpc-reconciler")
	}
	if p.DisplayName != "vpc-reconciler" {
		t.Errorf("DisplayName = %q, want %q", p.DisplayName, "vpc-reconciler")
	}
}

// TestSystemPrincipalFor_SurvivesFGASubjectSanitizer — the ID produced by
// SystemPrincipalFor is the officially-recommended identity for worker/reconciler
// peer-calls; it MUST survive the receiving-side FGA subject-sanitizer
// (authz.validSubjectID via authz.FormatSubject). If its separator is an
// FGA-reserved char (':'/'#'/'@'/whitespace), FormatSubject collapses it to
// "user:unknown" (every distinct worker fused into one FGA subject) and the
// per-RPC subject-extractor rejects it as anonymous → fail-closed deny even under
// break-glass. Locks the observable round-trip, not just the raw string.
func TestSystemPrincipalFor_SurvivesFGASubjectSanitizer(t *testing.T) {
	p := auth.SystemPrincipalFor("vpc", "reconciler")

	subj := authz.FormatSubject(p.Type, p.ID)
	if subj == "user:unknown" {
		t.Fatalf("FormatSubject(%q,%q) collapsed to %q — worker identity does not survive the FGA subject-sanitizer", p.Type, p.ID, subj)
	}
	if want := "user:" + p.ID; subj != want {
		t.Errorf("FormatSubject = %q, want %q (id must pass through unmodified)", subj, want)
	}

	// Two distinct workers must map to two distinct FGA subjects (not fused).
	other := auth.SystemPrincipalFor("compute", "expirer")
	otherSubj := authz.FormatSubject(other.Type, other.ID)
	if subj == otherSubj {
		t.Errorf("distinct workers fused into same subject %q — every system worker collapses into one FGA subject", subj)
	}
	if otherSubj == "user:unknown" {
		t.Errorf("FormatSubject(%q,%q) collapsed to %q", other.Type, other.ID, otherSubj)
	}
}

// TestSystemPrincipalFor_EmptyArgs_FallsBackToDefault — empty args
// fall back to bootstrap SystemPrincipal (don't invent garbage "system:-")
// for accidental empty-string callers.
func TestSystemPrincipalFor_EmptyArgs_FallsBackToDefault(t *testing.T) {
	got := auth.SystemPrincipalFor("", "")
	want := operations.SystemPrincipal()
	if got != want {
		t.Errorf("SystemPrincipalFor(\"\", \"\") = %+v, want %+v (fallback to SystemPrincipal)", got, want)
	}
}
