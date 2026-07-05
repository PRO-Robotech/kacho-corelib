// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package filter

import (
	"strings"
	"testing"
)

func TestParse_NameEquals(t *testing.T) {
	ast, err := Parse(`name="default"`, []string{"name"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if ast.Field != "name" || ast.Value != "default" || ast.Op != "=" {
		t.Fatalf("got %+v", ast)
	}
}

func TestParse_Empty(t *testing.T) {
	ast, err := Parse("", []string{"name"})
	if err != nil || ast != nil {
		t.Fatalf("got ast=%v err=%v, expected nil/nil", ast, err)
	}
}

func TestParse_UnknownField(t *testing.T) {
	_, err := Parse(`junk="x"`, []string{"name"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "Unknown field") {
		t.Fatalf("expected Unknown field, got %v", err)
	}
}

func TestParse_NoOperator(t *testing.T) {
	_, err := Parse(`name "x"`, []string{"name"})
	if err == nil || !strings.Contains(err.Error(), "Expected an operator") {
		t.Fatalf("got %v", err)
	}
}

func TestParse_NoQuote(t *testing.T) {
	_, err := Parse(`name=foo`, []string{"name"})
	if err == nil || !strings.Contains(err.Error(), "Expected a string") {
		t.Fatalf("got %v", err)
	}
}

func TestParse_SpacedEquals(t *testing.T) {
	ast, err := Parse(`name = "x"`, []string{"name"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if ast.Value != "x" {
		t.Fatalf("got %v", ast)
	}
}

func TestToSQL(t *testing.T) {
	ast := &FilterAST{Field: "name", Op: "=", Value: "foo"}
	frag, args := ast.ToSQL(3)
	if frag != "name = $3" {
		t.Fatalf("got %q", frag)
	}
	if len(args) != 1 || args[0] != "foo" {
		t.Fatalf("got %v", args)
	}
}

// A FilterAST built directly (bypassing Parse's allowedFields whitelist) with an
// injection payload in Field must NOT splice raw SQL into the WHERE fragment.
// ToSQL concatenates Field (values are parameterised), so Field must be
// identifier-safe or defensively quoted. Regression guard for findings3 SEC #6
// (CWE-89 / SQL injection via unvalidated Field).
func TestToSQL_MaliciousFieldNeutralised(t *testing.T) {
	ast := &FilterAST{Field: `1=1 OR name`, Op: "=", Value: "x"}
	frag, _ := ast.ToSQL(1)
	// The raw injection substring must never appear verbatim as SQL — a safe
	// implementation quotes the whole thing into a single identifier.
	if strings.Contains(frag, "1=1 OR name = $1") {
		t.Fatalf("injection payload spliced into WHERE fragment: %q", frag)
	}
	if !strings.HasPrefix(frag, `"`) {
		t.Fatalf("expected malicious Field to be identifier-quoted, got %q", frag)
	}
}

// A legitimate whitelisted field (produced by Parse) must still be emitted
// verbatim so the safe path is unchanged.
func TestToSQL_LegitFieldVerbatim(t *testing.T) {
	for _, f := range []string{"name", "network_id", "placement_type"} {
		ast := &FilterAST{Field: f, Op: "=", Value: "v"}
		frag, _ := ast.ToSQL(2)
		if frag != f+" = $2" {
			t.Fatalf("legit field %q altered: %q", f, frag)
		}
	}
}
