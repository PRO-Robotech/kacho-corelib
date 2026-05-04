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
