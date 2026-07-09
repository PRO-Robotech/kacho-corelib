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

func TestParse_UnterminatedQuote(t *testing.T) {
	// Value opens a quote but never closes it → the scanner runs off the end.
	// Must reject with "Expected closing quote", not parse into a FilterAST.
	// Regression guard for the filter/filter.go closing-quote reject branch.
	ast, err := Parse(`name = "foo`, []string{"name"})
	if ast != nil {
		t.Fatalf("expected nil AST for unterminated quote, got %+v", ast)
	}
	if err == nil || !strings.Contains(err.Error(), "Expected closing quote") {
		t.Fatalf("expected Expected closing quote, got %v", err)
	}
}

func TestParse_TrailingGarbage(t *testing.T) {
	// A well-formed equals followed by trailing tokens after the closing quote
	// must reject with "Unexpected token" (no AND/OR support yet), never silently
	// accept the leading clause. Regression guard for the trailing-token reject.
	ast, err := Parse(`name = "foo" extra`, []string{"name"})
	if ast != nil {
		t.Fatalf("expected nil AST for trailing garbage, got %+v", ast)
	}
	if err == nil || !strings.Contains(err.Error(), "Unexpected token") {
		t.Fatalf("expected Unexpected token, got %v", err)
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

// A field name must start with a letter or underscore — the exact identifier
// shape safeFieldRe (`^[a-zA-Z_]…`) and ToSQL's verbatim path promise. A
// digit-leading name must be rejected by Parse, otherwise it would be accepted
// yet fail safeFieldRe and get double-quoted by ToSQL (breaking the
// "legit Parse field is emitted verbatim" invariant). Regression guard for the
// Parse-vs-safeFieldRe first-char agreement.
func TestParse_DigitLeadingFieldRejected(t *testing.T) {
	// Whitelist the digit-leading name so the only thing that can reject it is
	// the first-char identifier rule, not the allowedFields check.
	ast, err := Parse(`3name = "x"`, []string{"3name"})
	if ast != nil {
		t.Fatalf("expected nil AST for digit-leading field, got %+v", ast)
	}
	if err == nil || !strings.Contains(err.Error(), "Expected a field name") {
		t.Fatalf("expected Expected a field name, got %v", err)
	}
}

// Every field name Parse accepts must pass safeFieldRe unchanged, so ToSQL
// emits it verbatim (never the defensive pgx.Identifier.Sanitize path). Locks
// the comment invariant "легитимные поля из Parse всегда проходят без изменений".
func TestParse_AcceptedFieldIsSafeVerbatim(t *testing.T) {
	for _, f := range []string{"name", "_x", "a1", "schema.table"} {
		ast, err := Parse(f+`="v"`, []string{f})
		if err != nil {
			t.Fatalf("Parse rejected legit field %q: %v", f, err)
		}
		if !safeFieldRe.MatchString(ast.Field) {
			t.Fatalf("field %q accepted by Parse but fails safeFieldRe", ast.Field)
		}
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
// identifier-safe or defensively quoted. Regression guard against CWE-89
// (SQL injection via unvalidated Field).
func TestToSQL_MaliciousFieldNeutralised(t *testing.T) {
	ast := &FilterAST{Field: `1=1 OR name`, Op: "=", Value: "x"}
	frag, _ := ast.ToSQL(1)
	// The raw injection substring must never appear unchanged as SQL — a safe
	// implementation quotes the whole thing into a single identifier.
	if strings.Contains(frag, "1=1 OR name = $1") {
		t.Fatalf("injection payload spliced into WHERE fragment: %q", frag)
	}
	if !strings.HasPrefix(frag, `"`) {
		t.Fatalf("expected malicious Field to be identifier-quoted, got %q", frag)
	}
}

// A legitimate whitelisted field (produced by Parse) must still be emitted
// as-is so the safe path is unchanged.
func TestToSQL_LegitFieldVerbatim(t *testing.T) {
	for _, f := range []string{"name", "network_id", "placement_type"} {
		ast := &FilterAST{Field: f, Op: "=", Value: "v"}
		frag, _ := ast.ToSQL(2)
		if frag != f+" = $2" {
			t.Fatalf("legit field %q altered: %q", f, frag)
		}
	}
}
