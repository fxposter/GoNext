package types_test

import (
	"go/token"
	"strings"
	"testing"

	"gonext/internal/frontend/ast"
	"gonext/internal/frontend/parser"
	. "gonext/internal/frontend/types"
)

func checkShortFunc(t *testing.T, src string) (*Info, error) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "t.gn", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	info := &Info{Types: map[ast.Expr]TypeAndValue{}}
	conf := Config{}
	_, err = conf.Check("main", fset, []*ast.File{file}, info)
	return info, err
}

func litTypes(info *Info) []string {
	var out []string
	for e, tv := range info.Types {
		if _, ok := e.(*ast.ShortFuncLit); ok {
			out = append(out, tv.Type.String())
		}
	}
	return out
}

func TestShortFuncLitPlainCall(t *testing.T) {
	info, err := checkShortFunc(t, `package main

func filter(xs []int, f func(int) bool) []int { return xs }

func main() {
	filter([]int{1, 2}, (x) => { return x > 1 })
}
`)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	types := litTypes(info)
	if len(types) != 1 || types[0] != "func(x int) bool" {
		t.Fatalf("got %v", types)
	}
}

func TestShortFuncLitGenericCallWithResultInference(t *testing.T) {
	info, err := checkShortFunc(t, `package main

func itoa(v int) string { return "" }

func mapSlice[T any, R any](xs []T, f func(T, int) R) []R {
	out := make([]R, 0, len(xs))
	for i, x := range xs {
		out = append(out, f(x, i))
	}
	return out
}

func main() {
	ss := mapSlice([]int{1, 2}, (v, _) => { return itoa(v * v) })
	var check []string = ss
	_ = check
}
`)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	types := litTypes(info)
	if len(types) != 1 || types[0] != "func(v int, _ int) string" {
		t.Fatalf("got %v", types)
	}
}

func TestShortFuncLitAssignmentContext(t *testing.T) {
	info, err := checkShortFunc(t, `package main

type Pred func(int) bool

func main() {
	var p Pred = (x) => { return x%2 == 0 }
	_ = p
}
`)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	types := litTypes(info)
	if len(types) != 1 || types[0] != "func(x int) bool" {
		t.Fatalf("got %v", types)
	}
}

func TestShortFuncLitNoContextError(t *testing.T) {
	_, err := checkShortFunc(t, `package main

func main() {
	f := (x) => { return x }
	_ = f
}
`)
	if err == nil || !strings.Contains(err.Error(), "does not provide a function type") {
		t.Fatalf("expected context error, got %v", err)
	}
}

func TestShortFuncLitGenericParamError(t *testing.T) {
	_, err := checkShortFunc(t, `package main

func twice[A any](f func(A) A) func(A) A { return f }

func main() {
	twice((x) => { return x })
}
`)
	if err == nil || !strings.Contains(err.Error(), "cannot recover the type of parameter") {
		t.Fatalf("expected generic-context error, got %v", err)
	}
}
