package transpiler

import (
	"bytes"
	"errors"
	"fmt"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"gonext/internal/frontend/ast"
	"gonext/internal/frontend/parser"
	"gonext/internal/frontend/printer"
)

const maxPasses = 16

type state struct {
	fset          *token.FileSet
	file          *ast.File
	norm          *normalized
	enums         map[string]*enumDef
	variantToEnum map[string]*enumDef
	usesGn        bool
}

type output struct {
	code   string
	usesGn bool
}

func TranspileFile(path string) (string, error) {
	out, err := transpileFile(path)
	if err != nil {
		return "", err
	}
	return out.code, nil
}

func TranspileSource(src string) (string, error) {
	out, err := transpile(src, "input.gn")
	if err != nil {
		return "", err
	}
	return out.code, nil
}

func RunFile(path string) error {
	out, err := transpileFile(path)
	if err != nil {
		return err
	}

	tmpDir, err := os.MkdirTemp("", "gonext-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	files := map[string]string{
		"go.mod":  "module gonext\n\ngo 1.21\n",
		"main.go": out.code,
	}
	if out.usesGn {
		files[filepath.Join("gn", "gn.go")] = gnSource
	}
	for name, content := range files {
		target := filepath.Join(tmpDir, name)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
			return err
		}
	}

	cmd := exec.Command("go", "run", ".")
	cmd.Dir = tmpDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func transpileFile(path string) (*output, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return transpile(string(src), filepath.Base(path))
}

func transpile(src, filename string) (*output, error) {
	norm, err := normalize(src)
	if err != nil {
		return nil, err
	}

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filename, norm.src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("after normalization the source does not parse as Go: %w", err)
	}

	st := &state{
		fset:          fset,
		file:          file,
		norm:          norm,
		enums:         norm.enums,
		variantToEnum: map[string]*enumDef{},
	}
	for _, def := range norm.enums {
		for _, v := range def.variants {
			st.variantToEnum[v.name] = def
		}
	}

	if len(norm.defaults) > 0 {
		if err := appendDefaultWrappers(st); err != nil {
			return nil, err
		}
	}

	analyzer, err := newAnalyzer(fset, file.Name.Name)
	if err != nil {
		return nil, err
	}

	for range maxPasses {
		pkg, info := analyzer.check(file)
		r := &rewriter{st: st, pkg: pkg, info: info}
		r.run()
		if len(r.errs) > 0 {
			return nil, errors.Join(r.errs...)
		}
		if !hasMarkers(file) {
			break
		}
		if !r.changed {
			msg := "could not transform the remaining GoNext constructs"
			if len(r.pending) > 0 {
				msg += ":\n  " + strings.Join(r.pending, "\n  ")
			}
			return nil, errors.New(msg)
		}
	}
	if hasMarkers(file) {
		return nil, errors.New("internal error: GoNext constructs remained after the transformation passes")
	}

	var buf bytes.Buffer
	cfg := printer.Config{Mode: printer.UseSpaces | printer.TabIndent, Tabwidth: 8}
	if err := cfg.Fprint(&buf, fset, file); err != nil {
		return nil, fmt.Errorf("cannot format the generated code: %w", err)
	}
	return &output{code: buf.String(), usesGn: st.usesGn}, nil
}

func hasMarkers(file *ast.File) bool {
	found := false
	ast.Inspect(file, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.Ident:
			if strings.HasPrefix(v.Name, markerPrefix) {
				found = true
			}
		case *ast.ShortFuncLit:
			found = true
		}
		return !found
	})
	return found
}
