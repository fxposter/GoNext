package parser

import (
	"bytes"
	"go/token"
	"testing"

	"gonext/internal/frontend/ast"
	"gonext/internal/frontend/printer"
)

const shortFuncSrc = `package main

func main() {
	f([]int{1}, (x, _) => { return x * 2 })
	g(() => { done() })
	h((a) => { return (b) => { return a + b } })
	x := (1 + 2) * 3
	y := (z)
	q := (x) // parenthesized ident, not a literal
	_, _, _ = x, y, q
}
`

func TestParseShortFuncLit(t *testing.T) {
	fset := token.NewFileSet()
	file, err := ParseFile(fset, "t.gn", shortFuncSrc, SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	count := 0
	ast.Inspect(file, func(n ast.Node) bool {
		if _, ok := n.(*ast.ShortFuncLit); ok {
			count++
		}
		return true
	})
	if count != 4 {
		t.Fatalf("expected 4 short function literals, found %d", count)
	}

	var buf bytes.Buffer
	cfg := printer.Config{Mode: printer.UseSpaces | printer.TabIndent, Tabwidth: 8}
	if err := cfg.Fprint(&buf, fset, file); err != nil {
		t.Fatalf("print error: %v", err)
	}
	if _, err := ParseFile(fset, "roundtrip.gn", buf.String(), SkipObjectResolution); err != nil {
		t.Fatalf("printed output does not parse back: %v\n%s", err, buf.String())
	}
}

func TestParseShortFuncLitDoesNotMisfire(t *testing.T) {
	const src = `package main

func main() {
	a := (b) // not a literal
	c := (d + e) * f
	g := x.(y)
	if (h) == 0 {
	}
	_, _, _ = a, c, g
}
`
	fset := token.NewFileSet()
	file, err := ParseFile(fset, "t.gn", src, SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	ast.Inspect(file, func(n ast.Node) bool {
		if _, ok := n.(*ast.ShortFuncLit); ok {
			t.Fatal("unexpected short function literal in plain Go source")
		}
		return true
	})
}
