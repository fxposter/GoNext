package transpiler

import (
	"fmt"
	"go/scanner"
	"go/token"
	"gonext/internal/frontend/ast"
	"gonext/internal/frontend/parser"
	"path"
	"sort"
	"strconv"
	"strings"
)

const markerPrefix = "__gnx_"

const (
	markerNamed = markerPrefix + "named"
	markerMatch = markerPrefix + "match"
	markerGuard = markerPrefix + "guard"
)

type normalized struct {
	src        string
	enums      map[string]*enumDef
	extensions map[string]bool
	extImports map[string]bool
	defaults   map[string]*defaultsDef
	defOrder   []string
}

type defaultsDef struct {
	name     string
	required int
	params   []defaultParam
}

type defaultParam struct {
	name       string
	typeSrc    string
	defaultSrc string
}

func normalize(src string) (*normalized, error) {
	n := &normalized{
		enums:      map[string]*enumDef{},
		extensions: map[string]bool{},
		extImports: map[string]bool{},
		defaults:   map[string]*defaultsDef{},
	}
	steps := []func(string) (string, error){
		n.rewriteEnums,
		n.rewriteExtensions,
		n.rewriteDefaultParams,
		n.rewriteMatches,
		n.rewriteNamedArgs,
	}
	var err error
	for _, step := range steps {
		if src, err = step(src); err != nil {
			return nil, err
		}
	}
	n.src = src
	return n, nil
}

type tok struct {
	off int
	end int
	tk  token.Token
	lit string
}

func scanAll(src string) ([]tok, error) {
	fset := token.NewFileSet()
	file := fset.AddFile("input.gn", fset.Base(), len(src))
	var errs scanner.ErrorList
	var s scanner.Scanner
	s.Init(file, []byte(src), func(pos token.Position, msg string) {
		errs.Add(pos, msg)
	}, scanner.ScanComments)

	var out []tok
	for {
		pos, tk, lit := s.Scan()
		if tk == token.EOF {
			break
		}
		if tk == token.COMMENT {
			continue
		}
		off := file.Offset(pos)
		out = append(out, tok{off: off, end: off + tokenLength(tk, lit), tk: tk, lit: lit})
	}
	if len(errs) > 0 {
		return nil, errs.Err()
	}
	return out, nil
}

func tokenLength(tk token.Token, lit string) int {
	switch {
	case tk == token.SEMICOLON && lit == "\n":
		return 0
	case lit != "":
		return len(lit)
	default:
		return len(tk.String())
	}
}

type edit struct {
	start, end int
	text       string
}

func applyEdits(src string, edits []edit) string {
	sort.Slice(edits, func(i, j int) bool { return edits[i].start < edits[j].start })
	var out strings.Builder
	last := 0
	for _, e := range edits {
		out.WriteString(src[last:e.start])
		out.WriteString(e.text)
		last = e.end
	}
	out.WriteString(src[last:])
	return out.String()
}

func matchForward(toks []tok, i int, open, close token.Token) (int, error) {
	depth := 0
	for j := i; j < len(toks); j++ {
		switch toks[j].tk {
		case open:
			depth++
		case close:
			depth--
			if depth == 0 {
				return j, nil
			}
		}
	}
	return 0, fmt.Errorf("unbalanced %q starting at offset %d", open.String(), toks[i].off)
}

func matchBackward(toks []tok, i int, open, close token.Token) (int, error) {
	depth := 0
	for j := i; j >= 0; j-- {
		switch toks[j].tk {
		case close:
			depth++
		case open:
			depth--
			if depth == 0 {
				return j, nil
			}
		}
	}
	return 0, fmt.Errorf("unbalanced %q ending at offset %d", close.String(), toks[i].off)
}

type depthTracker struct{ paren, brack, brace int }

func (d *depthTracker) update(tk token.Token) {
	switch tk {
	case token.LPAREN:
		d.paren++
	case token.RPAREN:
		d.paren--
	case token.LBRACK:
		d.brack++
	case token.RBRACK:
		d.brack--
	case token.LBRACE:
		d.brace++
	case token.RBRACE:
		d.brace--
	}
}

func (d *depthTracker) zero() bool { return d.paren == 0 && d.brack == 0 && d.brace == 0 }

func splitTokens(toks []tok, sep token.Token) [][2]int {
	var parts [][2]int
	var d depthTracker
	start := 0
	for i, t := range toks {
		if t.tk == sep && d.zero() {
			parts = append(parts, [2]int{start, i})
			start = i + 1
			continue
		}
		d.update(t.tk)
	}
	parts = append(parts, [2]int{start, len(toks)})
	return parts
}

func isDeclBoundary(toks []tok, i int) bool {
	if i == 0 {
		return true
	}
	switch toks[i-1].tk {
	case token.SEMICOLON, token.RBRACE, token.LBRACE, token.COLON:
		return true
	}
	return false
}

func (n *normalized) rewriteEnums(src string) (string, error) {
	toks, err := scanAll(src)
	if err != nil {
		return "", err
	}
	var edits []edit
	for i := 0; i < len(toks); i++ {
		if toks[i].tk != token.TYPE {
			continue
		}
		j := i + 1
		if j >= len(toks) || toks[j].tk != token.IDENT {
			continue
		}
		name := toks[j].lit
		k := j + 1
		typeParams := ""
		if k < len(toks) && toks[k].tk == token.LBRACK {
			closeIdx, err := matchForward(toks, k, token.LBRACK, token.RBRACK)
			if err != nil {
				return "", err
			}
			typeParams = src[toks[k].off:toks[closeIdx].end]
			k = closeIdx + 1
		}
		if k >= len(toks) || toks[k].tk != token.IDENT || toks[k].lit != "enum" {
			continue
		}
		braceIdx := k + 1
		if braceIdx >= len(toks) || toks[braceIdx].tk != token.LBRACE {
			return "", fmt.Errorf("enum %s: missing body", name)
		}
		closeIdx, err := matchForward(toks, braceIdx, token.LBRACE, token.RBRACE)
		if err != nil {
			return "", fmt.Errorf("enum %s: %w", name, err)
		}
		def, err := parseEnumDef(src, toks[braceIdx+1:closeIdx], name, typeParams)
		if err != nil {
			return "", err
		}
		n.enums[name] = def
		edits = append(edits, edit{toks[i].off, toks[closeIdx].end, def.generate()})
		i = closeIdx
	}
	return applyEdits(src, edits), nil
}

func parseEnumDef(src string, body []tok, name, typeParams string) (*enumDef, error) {
	paramNames, err := typeParamNames(typeParams)
	if err != nil {
		return nil, fmt.Errorf("enum %s: %w", name, err)
	}
	def := &enumDef{name: name, typeParams: typeParams, paramNames: paramNames}

	for i := 0; i < len(body); i++ {
		if body[i].tk == token.SEMICOLON {
			continue
		}
		if body[i].tk != token.IDENT {
			return nil, fmt.Errorf("enum %s: unexpected %q in body", name, body[i].tk)
		}
		variant := enumVariant{name: body[i].lit}
		if i+1 < len(body) && body[i+1].tk == token.LPAREN {
			closeIdx, err := matchForward(body, i+1, token.LPAREN, token.RPAREN)
			if err != nil {
				return nil, fmt.Errorf("enum %s: %w", name, err)
			}
			fields := body[i+2 : closeIdx]
			for _, part := range splitTokens(fields, token.COMMA) {
				if part[0] == part[1] {
					continue
				}
				fieldSrc := strings.TrimSpace(src[fields[part[0]].off:fields[part[1]-1].end])
				if _, err := parser.ParseExpr(fieldSrc); err != nil {
					return nil, fmt.Errorf("enum %s, variant %s: invalid field type %q", name, variant.name, fieldSrc)
				}
				variant.fields = append(variant.fields, fieldSrc)
			}
			i = closeIdx
		}
		def.variants = append(def.variants, variant)
	}
	if len(def.variants) == 0 {
		return nil, fmt.Errorf("enum %s: must declare at least one variant", name)
	}
	return def, nil
}

func typeParamNames(typeParams string) ([]string, error) {
	if typeParams == "" {
		return nil, nil
	}
	file, err := parser.ParseFile(token.NewFileSet(), "probe.go", "package p\ntype probe"+typeParams+" int", 0)
	if err != nil {
		return nil, fmt.Errorf("invalid type parameters %q", typeParams)
	}
	gen, ok := file.Decls[0].(*ast.GenDecl)
	if !ok || len(gen.Specs) == 0 {
		return nil, fmt.Errorf("invalid type parameters %q", typeParams)
	}
	spec, ok := gen.Specs[0].(*ast.TypeSpec)
	if !ok || spec.TypeParams == nil {
		return nil, fmt.Errorf("invalid type parameters %q", typeParams)
	}
	var names []string
	for _, field := range spec.TypeParams.List {
		for _, name := range field.Names {
			names = append(names, name.Name)
		}
	}
	return names, nil
}

func (n *normalized) rewriteExtensions(src string) (string, error) {
	toks, err := scanAll(src)
	if err != nil {
		return "", err
	}
	var edits []edit
	for i := range toks {
		t := toks[i]
		if t.tk == token.IDENT && t.lit == "extension" && i+1 < len(toks) && toks[i+1].tk == token.FUNC && isDeclBoundary(toks, i) {
			j := i + 2
			if j >= len(toks) || toks[j].tk != token.IDENT {
				return "", fmt.Errorf("extension functions must be plain named functions (offset %d)", t.off)
			}
			n.extensions[toks[j].lit] = true
			edits = append(edits, edit{t.off, toks[i+1].off, ""})
			continue
		}
		if t.tk == token.IMPORT && i+1 < len(toks) && toks[i+1].tk == token.IDENT && toks[i+1].lit == "extension" {
			edits = append(edits, edit{toks[i+1].off, toks[i+2].off, ""})
			j := i + 2
			switch toks[j].tk {
			case token.STRING:
				alias, err := importAlias("", toks[j].lit)
				if err != nil {
					return "", err
				}
				n.extImports[alias] = true
			case token.IDENT:
				if j+1 >= len(toks) || toks[j+1].tk != token.STRING {
					return "", fmt.Errorf("invalid import extension declaration at offset %d", t.off)
				}
				n.extImports[toks[j].lit] = true
			case token.LPAREN:
				closeIdx, err := matchForward(toks, j, token.LPAREN, token.RPAREN)
				if err != nil {
					return "", err
				}
				for k := j + 1; k < closeIdx; k++ {
					switch toks[k].tk {
					case token.SEMICOLON:
					case token.IDENT:
						if k+1 < closeIdx && toks[k+1].tk == token.STRING {
							n.extImports[toks[k].lit] = true
							k++
						}
					case token.STRING:
						alias, err := importAlias("", toks[k].lit)
						if err != nil {
							return "", err
						}
						n.extImports[alias] = true
					default:
						return "", fmt.Errorf("unsupported import extension form at offset %d", toks[k].off)
					}
				}
			default:
				return "", fmt.Errorf("invalid import extension declaration at offset %d", t.off)
			}
		}
	}
	return applyEdits(src, edits), nil
}

func importAlias(alias, quotedPath string) (string, error) {
	if alias != "" {
		return alias, nil
	}
	p, err := strconv.Unquote(quotedPath)
	if err != nil {
		return "", fmt.Errorf("invalid import path %s", quotedPath)
	}
	return path.Base(p), nil
}

func (n *normalized) rewriteDefaultParams(src string) (string, error) {
	toks, err := scanAll(src)
	if err != nil {
		return "", err
	}
	var edits []edit
	for i := 0; i < len(toks); i++ {
		if toks[i].tk != token.FUNC {
			continue
		}
		topLevel := isDeclBoundary(toks, i)
		j := i + 1
		hasRecv := false
		if j < len(toks) && toks[j].tk == token.LPAREN {
			closeIdx, err := matchForward(toks, j, token.LPAREN, token.RPAREN)
			if err != nil {
				return "", err
			}
			hasRecv = true
			j = closeIdx + 1
		}
		if j >= len(toks) || toks[j].tk != token.IDENT {
			continue
		}
		name := toks[j].lit
		j++
		hasTypeParams := false
		if j < len(toks) && toks[j].tk == token.LBRACK {
			closeIdx, err := matchForward(toks, j, token.LBRACK, token.RBRACK)
			if err != nil {
				return "", err
			}
			hasTypeParams = true
			j = closeIdx + 1
		}
		if j >= len(toks) || toks[j].tk != token.LPAREN {
			continue
		}
		closeIdx, err := matchForward(toks, j, token.LPAREN, token.RPAREN)
		if err != nil {
			return "", err
		}
		params := toks[j+1 : closeIdx]
		if !hasAssign(params) {
			i = closeIdx
			continue
		}
		switch {
		case !topLevel:
			return "", fmt.Errorf("default parameter values are only supported on top-level functions: %s", name)
		case hasRecv:
			return "", fmt.Errorf("methods cannot have default parameter values: %s", name)
		case hasTypeParams:
			return "", fmt.Errorf("generic functions cannot have default parameter values: %s", name)
		}
		def, stripped, err := parseDefaultParams(src, params, name)
		if err != nil {
			return "", err
		}
		n.defaults[name] = def
		n.defOrder = append(n.defOrder, name)
		edits = append(edits, edit{toks[j].end, toks[closeIdx].off, stripped})
		i = closeIdx
	}
	return applyEdits(src, edits), nil
}

func hasAssign(toks []tok) bool {
	var d depthTracker
	for _, t := range toks {
		if t.tk == token.ASSIGN && d.zero() {
			return true
		}
		d.update(t.tk)
	}
	return false
}

func parseDefaultParams(src string, params []tok, fnName string) (*defaultsDef, string, error) {
	def := &defaultsDef{name: fnName, required: -1}
	var plain []string
	for _, part := range splitTokens(params, token.COMMA) {
		toks := params[part[0]:part[1]]
		if len(toks) == 0 {
			continue
		}
		if len(toks) < 2 || toks[0].tk != token.IDENT {
			return nil, "", fmt.Errorf("function %s: every parameter must be declared as \"name type\" when default values are used", fnName)
		}
		p := defaultParam{name: toks[0].lit}
		rest := toks[1:]
		assignIdx := -1
		var d depthTracker
		for k, t := range rest {
			if t.tk == token.ASSIGN && d.zero() {
				assignIdx = k
				break
			}
			d.update(t.tk)
		}
		if assignIdx < 0 {
			p.typeSrc = strings.TrimSpace(src[rest[0].off:rest[len(rest)-1].end])
		} else {
			if assignIdx == 0 || assignIdx == len(rest)-1 {
				return nil, "", fmt.Errorf("function %s: invalid default value for parameter %s", fnName, p.name)
			}
			p.typeSrc = strings.TrimSpace(src[rest[0].off:rest[assignIdx-1].end])
			p.defaultSrc = strings.TrimSpace(src[rest[assignIdx+1].off:rest[len(rest)-1].end])
		}
		if p.defaultSrc == "" && def.required >= 0 {
			return nil, "", fmt.Errorf("function %s: required parameter %s follows a parameter with a default value", fnName, p.name)
		}
		if p.defaultSrc != "" && def.required < 0 {
			def.required = len(def.params)
		}
		def.params = append(def.params, p)
		plain = append(plain, p.name+" "+p.typeSrc)
	}
	if def.required < 0 {
		def.required = len(def.params)
	}
	return def, strings.Join(plain, ", "), nil
}

func (n *normalized) rewriteMatches(src string) (string, error) {
	toks, err := scanAll(src)
	if err != nil {
		return "", err
	}
	var edits []edit

	for i := range toks {
		t := toks[i]
		if t.tk != token.IDENT || t.lit != "match" || !isDeclBoundary(toks, i) {
			continue
		}
		braceIdx := -1
		var d depthTracker
	scanExpr:
		for j := i + 1; j < len(toks); j++ {
			switch {
			case toks[j].tk == token.LBRACE && d.zero():
				braceIdx = j
				break scanExpr
			case toks[j].tk == token.SEMICOLON && d.zero():
				break scanExpr
			}
			d.update(toks[j].tk)
		}
		if braceIdx < 0 || braceIdx == i+1 {
			continue
		}
		first := braceIdx + 1
		for first < len(toks) && toks[first].tk == token.SEMICOLON {
			first++
		}
		if first >= len(toks) || (toks[first].tk != token.CASE && toks[first].tk != token.DEFAULT) {
			continue
		}
		edits = append(edits,
			edit{t.off, t.end, "switch " + markerMatch + "("},
			edit{toks[braceIdx].off, toks[braceIdx].off, ") "})
	}

	for i := range toks {
		if toks[i].tk != token.CASE {
			continue
		}
		colonIdx, ifIdx := -1, -1
		var d depthTracker
	scanHeader:
		for j := i + 1; j < len(toks); j++ {
			switch {
			case toks[j].tk == token.COLON && d.zero():
				colonIdx = j
				break scanHeader
			case toks[j].tk == token.IF && d.zero():
				ifIdx = j
			case (toks[j].tk == token.SEMICOLON || toks[j].tk == token.LBRACE) && d.zero():
				break scanHeader
			}
			d.update(toks[j].tk)
		}
		if colonIdx < 0 || ifIdx < 0 {
			continue
		}
		patterns := strings.TrimSpace(src[toks[i].end:toks[ifIdx].off])
		cond := strings.TrimSpace(src[toks[ifIdx].end:toks[colonIdx].off])
		if patterns == "" || cond == "" {
			return "", fmt.Errorf("invalid match guard at offset %d", toks[i].off)
		}
		edits = append(edits, edit{toks[i].end, toks[colonIdx].off,
			fmt.Sprintf(" %s(%s, %s)", markerGuard, patterns, cond)})
	}

	return applyEdits(src, edits), nil
}

func (n *normalized) rewriteNamedArgs(src string) (string, error) {
	toks, err := scanAll(src)
	if err != nil {
		return "", err
	}
	var edits []edit
	var stack []token.Token
	for i, t := range toks {
		switch t.tk {
		case token.LPAREN, token.LBRACK, token.LBRACE:
			stack = append(stack, t.tk)
			continue
		case token.RPAREN, token.RBRACK, token.RBRACE:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			continue
		case token.ASSIGN:
		default:
			continue
		}
		if len(stack) == 0 || stack[len(stack)-1] != token.LPAREN {
			continue
		}
		if i < 2 || toks[i-1].tk != token.IDENT {
			continue
		}
		switch toks[i-2].tk {
		case token.LPAREN, token.COMMA:
		default:
			continue
		}
		if i+1 >= len(toks) {
			continue
		}
		endIdx := -1
		var d depthTracker
	scanValue:
		for j := i + 1; j < len(toks); j++ {
			switch {
			case (toks[j].tk == token.COMMA || toks[j].tk == token.RPAREN) && d.zero():
				endIdx = j - 1
				break scanValue
			}
			d.update(toks[j].tk)
		}
		if endIdx < i+1 {
			return "", fmt.Errorf("invalid named argument %q at offset %d", toks[i-1].lit, toks[i-1].off)
		}
		edits = append(edits,
			edit{toks[i-1].off, toks[i+1].off, markerNamed + "(" + strconv.Quote(toks[i-1].lit) + ", "},
			edit{toks[endIdx].end, toks[endIdx].end, ")"})
	}
	return applyEdits(src, edits), nil
}
