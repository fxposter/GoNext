package transpiler

import (
	"fmt"
	"go/build"
	"go/token"

	"gonext/internal/frontend/ast"
	"gonext/internal/frontend/parser"
	"gonext/internal/frontend/srcimporter"
	"gonext/internal/frontend/types"
)

const preludeBody = `
func __gnx_named[T any](name string, value T) T {
	return value
}

func __gnx_match[T any](value T) T {
	return value
}

func __gnx_guard[T any](pattern T, cond bool) T {
	return pattern
}
`

const gnImportPath = "gonext/gn"

const gnSource = `package gn

// Optional carries an explicitly provided value of an argument whose
// parameter declares a default value. A zero Optional means "use the default".
type Optional[T any] struct {
	Exists bool
	Value  T
}

func Some[T any](value T) Optional[T] {
	return Optional[T]{Exists: true, Value: value}
}

func None[T any]() Optional[T] {
	return Optional[T]{}
}
`

type analyzer struct {
	fset    *token.FileSet
	imp     types.Importer
	prelude *ast.File
}

type gnImporter struct {
	fset  *token.FileSet
	base  types.Importer
	gnPkg *types.Package
}

func (g *gnImporter) Import(path string) (*types.Package, error) {
	if path != gnImportPath {
		return g.base.Import(path)
	}
	if g.gnPkg == nil {
		file, err := parser.ParseFile(g.fset, "gn.go", gnSource, 0)
		if err != nil {
			return nil, err
		}
		conf := types.Config{Importer: g.base}
		pkg, err := conf.Check(gnImportPath, g.fset, []*ast.File{file}, nil)
		if err != nil {
			return nil, err
		}
		g.gnPkg = pkg
	}
	return g.gnPkg, nil
}

func newAnalyzer(fset *token.FileSet, pkgName string) (*analyzer, error) {
	prelude, err := parser.ParseFile(fset, "gnx_prelude.go", "package "+pkgName+"\n"+preludeBody, 0)
	if err != nil {
		return nil, fmt.Errorf("internal error: prelude does not parse: %w", err)
	}
	base := srcimporter.New(&build.Default, fset, map[string]*types.Package{})
	return &analyzer{
		fset:    fset,
		imp:     &gnImporter{fset: fset, base: base},
		prelude: prelude,
	}, nil
}

func (a *analyzer) check(file *ast.File) (*types.Package, *types.Info) {
	info := &types.Info{
		Types:      map[ast.Expr]types.TypeAndValue{},
		Defs:       map[*ast.Ident]types.Object{},
		Uses:       map[*ast.Ident]types.Object{},
		Selections: map[*ast.SelectorExpr]*types.Selection{},
	}
	conf := types.Config{
		Error:    func(error) {},
		Importer: a.imp,
	}
	pkg, _ := conf.Check(file.Name.Name, a.fset, []*ast.File{file, a.prelude}, info)
	return pkg, info
}

func validType(t types.Type) bool {
	if t == nil {
		return false
	}
	basic, ok := t.(*types.Basic)
	return !ok || basic.Kind() != types.Invalid
}

func concreteType(t types.Type) types.Type {
	return types.Default(t)
}

func typeToExpr(t types.Type, pkg *types.Package, file *ast.File) (ast.Expr, error) {
	s := types.TypeString(t, fileQualifier(pkg, file))
	expr, err := parser.ParseExpr(s)
	if err != nil {
		return nil, fmt.Errorf("cannot express type %s in the generated code: %w", s, err)
	}
	clearPositions(expr)
	return expr, nil
}

func fileQualifier(pkg *types.Package, file *ast.File) types.Qualifier {
	aliases := map[string]string{}
	for _, spec := range file.Imports {
		p, err := importPathOf(spec)
		if err != nil {
			continue
		}
		alias := ""
		if spec.Name != nil {
			alias = spec.Name.Name
		}
		aliases[p] = alias
	}
	return func(other *types.Package) string {
		if other == pkg {
			return ""
		}
		if alias, ok := aliases[other.Path()]; ok && alias != "" {
			return alias
		}
		return other.Name()
	}
}

func importPathOf(spec *ast.ImportSpec) (string, error) {
	return unquote(spec.Path.Value)
}

func unquote(s string) (string, error) {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1], nil
	}
	return "", fmt.Errorf("invalid import path %s", s)
}
