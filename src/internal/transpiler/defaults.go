package transpiler

import (
	"fmt"
	"go/token"
	"gonext/internal/frontend/ast"
	"gonext/internal/frontend/parser"
	"reflect"
	"strings"
)

const defaultSuffix = "___default"

func gnSomeExpr(value ast.Expr) ast.Expr {
	return &ast.CallExpr{
		Fun:  &ast.SelectorExpr{X: ast.NewIdent("gn"), Sel: ast.NewIdent("Some")},
		Args: []ast.Expr{value},
	}
}

func gnNoneExpr(typeExpr ast.Expr) ast.Expr {
	return &ast.CallExpr{
		Fun: &ast.IndexExpr{
			X:     &ast.SelectorExpr{X: ast.NewIdent("gn"), Sel: ast.NewIdent("None")},
			Index: typeExpr,
		},
	}
}

func parseTypeSrc(src string) (ast.Expr, error) {
	expr, err := parser.ParseExpr(src)
	if err != nil {
		return nil, err
	}
	clearPositions(expr)
	return expr, nil
}

func clearPositions(root ast.Node) {
	ast.Inspect(root, func(n ast.Node) bool {
		if n == nil {
			return false
		}
		v := reflect.ValueOf(n).Elem()
		for _, f := range v.Fields() {
			f := f
			if f.Type() == posType {
				f.SetInt(0)
			}
		}
		return true
	})
}

var posType = reflect.TypeFor[token.Pos]()

func appendDefaultWrappers(st *state) error {
	tokenFile := st.fset.File(st.file.Pos())
	for _, name := range st.norm.defOrder {
		def := st.norm.defaults[name]
		decl := findFuncDecl(st.file, name)
		if decl == nil {
			return fmt.Errorf("internal error: declaration of %s not found", name)
		}
		resultsSrc := ""
		if decl.Type.Results != nil && len(decl.Type.Results.List) > 0 {
			start := tokenFile.Offset(decl.Type.Results.Pos())
			end := tokenFile.Offset(decl.Type.Results.End())
			resultsSrc = " " + st.norm.src[start:end]
		}

		var b strings.Builder
		var params, callArgs []string
		for i, p := range def.params {
			if i < def.required {
				params = append(params, p.name+" "+p.typeSrc)
			} else {
				params = append(params, p.name+"Opt gn.Optional["+p.typeSrc+"]")
			}
			callArgs = append(callArgs, p.name)
		}
		fmt.Fprintf(&b, "func %s%s(%s)%s {\n", name, defaultSuffix, strings.Join(params, ", "), resultsSrc)
		for i := def.required; i < len(def.params); i++ {
			p := def.params[i]
			fmt.Fprintf(&b, "\tvar %s %s\n", p.name, p.typeSrc)
			fmt.Fprintf(&b, "\tif %sOpt.Exists {\n\t\t%s = %sOpt.Value\n\t} else {\n\t\t%s = %s\n\t}\n",
				p.name, p.name, p.name, p.name, p.defaultSrc)
		}
		b.WriteString("\t")
		if resultsSrc != "" {
			b.WriteString("return ")
		}
		fmt.Fprintf(&b, "%s(%s)\n}\n", name, strings.Join(callArgs, ", "))

		wrapper, err := parseSingleDecl(st.fset, b.String())
		if err != nil {
			return fmt.Errorf("function %s: cannot generate the default-values helper: %w", name, err)
		}
		st.file.Decls = append(st.file.Decls, wrapper)
	}
	addImport(st.file, gnImportPath)
	st.usesGn = true
	return nil
}

func findFuncDecl(file *ast.File, name string) *ast.FuncDecl {
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Name.Name == name {
			return fn
		}
	}
	return nil
}

func parseSingleDecl(fset *token.FileSet, src string) (ast.Decl, error) {
	file, err := parser.ParseFile(fset, "generated.go", "package p\n"+src, 0)
	if err != nil {
		return nil, err
	}
	if len(file.Decls) != 1 {
		return nil, fmt.Errorf("expected exactly one declaration")
	}
	return file.Decls[0], nil
}

func addImport(file *ast.File, path string) {
	spec := &ast.ImportSpec{Path: &ast.BasicLit{Kind: token.STRING, Value: `"` + path + `"`}}
	for _, decl := range file.Decls {
		if gen, ok := decl.(*ast.GenDecl); ok && gen.Tok == token.IMPORT {
			gen.Specs = append(gen.Specs, spec)
			if !gen.Lparen.IsValid() && len(gen.Specs) > 1 {
				gen.Lparen = gen.Pos()
				gen.Rparen = gen.End()
			}
			file.Imports = append(file.Imports, spec)
			return
		}
	}
	gen := &ast.GenDecl{Tok: token.IMPORT, Specs: []ast.Spec{spec}}
	file.Decls = append([]ast.Decl{gen}, file.Decls...)
	file.Imports = append(file.Imports, spec)
}
