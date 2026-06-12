package transpiler

import (
	"fmt"
	"go/token"
	"gonext/internal/frontend/ast"
	"gonext/internal/frontend/types"
	"strings"
)

type rewriter struct {
	st      *state
	pkg     *types.Package
	info    *types.Info
	changed bool
	errs    []error
	pending []string
	results []*types.Tuple
}

func (r *rewriter) errorf(pos token.Pos, format string, args ...any) {
	prefix := ""
	if pos.IsValid() {
		prefix = r.st.fset.Position(pos).String() + ": "
	}
	r.errs = append(r.errs, fmt.Errorf("%s%s", prefix, fmt.Sprintf(format, args...)))
}

func (r *rewriter) defer_(pos token.Pos, format string, args ...any) {
	prefix := ""
	if pos.IsValid() {
		prefix = r.st.fset.Position(pos).String() + ": "
	}
	r.pending = append(r.pending, fmt.Sprintf("%s%s", prefix, fmt.Sprintf(format, args...)))
}

func (r *rewriter) run() {
	for _, decl := range r.st.file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Body == nil {
				continue
			}
			r.pushResults(r.funcDeclResults(d))
			r.walkStmtList(d.Body.List)
			r.popResults()
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				if vs, ok := spec.(*ast.ValueSpec); ok {
					r.handleValueSpec(vs)
				}
			}
		}
	}
}

func (r *rewriter) funcDeclResults(d *ast.FuncDecl) *types.Tuple {
	if obj, ok := r.info.Defs[d.Name].(*types.Func); ok {
		return obj.Type().(*types.Signature).Results()
	}
	return nil
}

func (r *rewriter) pushResults(t *types.Tuple) { r.results = append(r.results, t) }
func (r *rewriter) popResults()                { r.results = r.results[:len(r.results)-1] }

func (r *rewriter) currentResults() *types.Tuple {
	if len(r.results) == 0 {
		return nil
	}
	return r.results[len(r.results)-1]
}

func (r *rewriter) walkStmtList(list []ast.Stmt) {
	for i, s := range list {
		list[i] = r.walkStmt(s)
	}
}

func (r *rewriter) walkStmt(s ast.Stmt) ast.Stmt {
	switch v := s.(type) {
	case nil:
		return s
	case *ast.BlockStmt:
		r.walkStmtList(v.List)
	case *ast.ExprStmt:
		v.X = r.walkExpr(v.X)
	case *ast.AssignStmt:
		if v.Tok == token.ASSIGN {
			for i := range v.Rhs {
				if i < len(v.Lhs) {
					if target := r.info.TypeOf(v.Lhs[i]); validType(target) {
						r.instantiateCtor(target, v.Rhs[i])
					}
				}
			}
		}
		for i := range v.Lhs {
			v.Lhs[i] = r.walkExpr(v.Lhs[i])
		}
		for i := range v.Rhs {
			v.Rhs[i] = r.walkExpr(v.Rhs[i])
		}
	case *ast.DeclStmt:
		if gen, ok := v.Decl.(*ast.GenDecl); ok {
			for _, spec := range gen.Specs {
				if vs, ok := spec.(*ast.ValueSpec); ok {
					r.handleValueSpec(vs)
				}
			}
		}
	case *ast.ReturnStmt:
		if results := r.currentResults(); results != nil && results.Len() == len(v.Results) {
			for i := range v.Results {
				r.instantiateCtor(results.At(i).Type(), v.Results[i])
			}
		}
		for i := range v.Results {
			v.Results[i] = r.walkExpr(v.Results[i])
		}
	case *ast.IfStmt:
		v.Init = r.walkStmt(v.Init)
		v.Cond = r.walkExpr(v.Cond)
		r.walkStmtList(v.Body.List)
		v.Else = r.walkStmt(v.Else)
	case *ast.ForStmt:
		v.Init = r.walkStmt(v.Init)
		v.Cond = r.walkExpr(v.Cond)
		v.Post = r.walkStmt(v.Post)
		r.walkStmtList(v.Body.List)
	case *ast.RangeStmt:
		v.X = r.walkExpr(v.X)
		r.walkStmtList(v.Body.List)
	case *ast.SwitchStmt:
		if isMatchMarker(v.Tag) {
			if stmt, ok := r.buildMatch(v); ok {
				return r.walkStmt(stmt)
			}
			return v
		}
		v.Init = r.walkStmt(v.Init)
		v.Tag = r.walkExpr(v.Tag)
		for _, clause := range v.Body.List {
			if cc, ok := clause.(*ast.CaseClause); ok {
				for i := range cc.List {
					cc.List[i] = r.walkExpr(cc.List[i])
				}
				r.walkStmtList(cc.Body)
			}
		}
	case *ast.TypeSwitchStmt:
		v.Init = r.walkStmt(v.Init)
		v.Assign = r.walkStmt(v.Assign)
		for _, clause := range v.Body.List {
			if cc, ok := clause.(*ast.CaseClause); ok {
				r.walkStmtList(cc.Body)
			}
		}
	case *ast.SelectStmt:
		for _, clause := range v.Body.List {
			if cc, ok := clause.(*ast.CommClause); ok {
				cc.Comm = r.walkStmt(cc.Comm)
				r.walkStmtList(cc.Body)
			}
		}
	case *ast.LabeledStmt:
		v.Stmt = r.walkStmt(v.Stmt)
	case *ast.GoStmt:
		v.Call = r.walkExpr(v.Call).(*ast.CallExpr)
	case *ast.DeferStmt:
		v.Call = r.walkExpr(v.Call).(*ast.CallExpr)
	case *ast.SendStmt:
		v.Chan = r.walkExpr(v.Chan)
		v.Value = r.walkExpr(v.Value)
	case *ast.IncDecStmt:
		v.X = r.walkExpr(v.X)
	}
	return s
}

func (r *rewriter) handleValueSpec(vs *ast.ValueSpec) {
	for i := range vs.Values {
		var target types.Type
		if vs.Type != nil {
			target = r.info.TypeOf(vs.Type)
		} else if i < len(vs.Names) {
			target = r.info.TypeOf(vs.Names[i])
		}
		if validType(target) {
			r.instantiateCtor(target, vs.Values[i])
		}
		vs.Values[i] = r.walkExpr(vs.Values[i])
	}
}

func (r *rewriter) walkExpr(e ast.Expr) ast.Expr {
	switch v := e.(type) {
	case nil:
		return e
	case *ast.CallExpr:
		switch fun := v.Fun.(type) {
		case *ast.SelectorExpr:
			fun.X = r.walkExpr(fun.X)
		case *ast.Ident:
		default:
			v.Fun = r.walkExpr(v.Fun)
		}
		for i := range v.Args {
			v.Args[i] = r.walkExpr(v.Args[i])
		}
		return r.rewriteCall(v)
	case *ast.FuncLit:
		var results *types.Tuple
		if sig, ok := r.info.TypeOf(v).(*types.Signature); ok {
			results = sig.Results()
		}
		r.pushResults(results)
		r.walkStmtList(v.Body.List)
		r.popResults()
	case *ast.ShortFuncLit:
		var results *types.Tuple
		if sig, ok := r.info.TypeOf(v).(*types.Signature); ok {
			results = sig.Results()
		}
		r.pushResults(results)
		r.walkStmtList(v.Body.List)
		r.popResults()
		return r.convertShortFuncLit(v)
	case *ast.ParenExpr:
		v.X = r.walkExpr(v.X)
	case *ast.SelectorExpr:
		v.X = r.walkExpr(v.X)
	case *ast.StarExpr:
		v.X = r.walkExpr(v.X)
	case *ast.UnaryExpr:
		v.X = r.walkExpr(v.X)
	case *ast.BinaryExpr:
		v.X = r.walkExpr(v.X)
		v.Y = r.walkExpr(v.Y)
	case *ast.KeyValueExpr:
		v.Key = r.walkExpr(v.Key)
		v.Value = r.walkExpr(v.Value)
	case *ast.CompositeLit:
		for i := range v.Elts {
			v.Elts[i] = r.walkExpr(v.Elts[i])
		}
	case *ast.IndexExpr:
		v.X = r.walkExpr(v.X)
		v.Index = r.walkExpr(v.Index)
	case *ast.IndexListExpr:
		v.X = r.walkExpr(v.X)
		for i := range v.Indices {
			v.Indices[i] = r.walkExpr(v.Indices[i])
		}
	case *ast.SliceExpr:
		v.X = r.walkExpr(v.X)
		v.Low = r.walkExpr(v.Low)
		v.High = r.walkExpr(v.High)
		v.Max = r.walkExpr(v.Max)
	case *ast.TypeAssertExpr:
		v.X = r.walkExpr(v.X)
	}
	return e
}

func isMarkerCall(e ast.Expr, name string) (*ast.CallExpr, bool) {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return nil, false
	}
	id, ok := call.Fun.(*ast.Ident)
	if !ok || id.Name != name {
		return nil, false
	}
	return call, true
}

func isMatchMarker(tag ast.Expr) bool {
	_, ok := isMarkerCall(tag, markerMatch)
	return ok
}

func (r *rewriter) rewriteCall(call *ast.CallExpr) ast.Expr {
	if id, ok := call.Fun.(*ast.Ident); ok && strings.HasPrefix(id.Name, markerPrefix) {
		return call
	}

	sig, def := r.calleeSignature(call)

	if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sig == nil {
		if extSig, handled := r.rewriteExtensionCall(call, sel); handled {
			sig = extSig
		}
	}

	needsRemap := hasNamedArgs(call.Args) || (def != nil && len(call.Args) < len(def.params) && !hasEllipsis(call))
	if needsRemap {
		if sig == nil {
			r.defer_(call.Pos(), "cannot resolve the signature of the called function yet")
			return call
		}
		if !r.remapArgs(call, sig, def) {
			return call
		}
	}

	return call
}

func hasEllipsis(call *ast.CallExpr) bool { return call.Ellipsis.IsValid() }

func hasNamedArgs(args []ast.Expr) bool {
	for _, a := range args {
		if _, ok := isMarkerCall(a, markerNamed); ok {
			return true
		}
	}
	return false
}

func (r *rewriter) calleeSignature(call *ast.CallExpr) (*types.Signature, *defaultsDef) {
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		obj := r.info.Uses[fun]
		if obj == nil {
			obj = r.pkg.Scope().Lookup(fun.Name)
		}
		switch o := obj.(type) {
		case *types.Func:
			sig := o.Type().(*types.Signature)
			var def *defaultsDef
			if o.Parent() == r.pkg.Scope() {
				def = r.st.norm.defaults[fun.Name]
			}
			return sig, def
		case *types.Var:
			if sig, ok := o.Type().Underlying().(*types.Signature); ok {
				return sig, nil
			}
		}
	case *ast.SelectorExpr:
		if sel, ok := r.info.Selections[fun]; ok {
			if sig, ok := sel.Type().(*types.Signature); ok {
				return sig, nil
			}
			return nil, nil
		}
		if obj, ok := r.info.Uses[fun.Sel].(*types.Func); ok {
			return obj.Type().(*types.Signature), nil
		}
		if x, ok := fun.X.(*ast.Ident); ok {
			if imported := r.importedPackage(x.Name); imported != nil {
				if fn, ok := imported.Scope().Lookup(fun.Sel.Name).(*types.Func); ok {
					return fn.Type().(*types.Signature), nil
				}
			}
		}
	}
	if t := r.info.TypeOf(call.Fun); validType(t) {
		if sig, ok := t.Underlying().(*types.Signature); ok {
			return sig, nil
		}
	}
	return nil, nil
}

func (r *rewriter) importedPackage(alias string) *types.Package {
	for _, spec := range r.st.file.Imports {
		p, err := importPathOf(spec)
		if err != nil {
			continue
		}
		var imported *types.Package
		for _, candidate := range r.pkg.Imports() {
			if candidate.Path() == p {
				imported = candidate
				break
			}
		}
		if imported == nil {
			continue
		}
		effective := imported.Name()
		if spec.Name != nil {
			effective = spec.Name.Name
		}
		if effective == alias {
			return imported
		}
	}
	return nil
}

func (r *rewriter) remapArgs(call *ast.CallExpr, sig *types.Signature, def *defaultsDef) bool {
	if sig.Variadic() {
		r.errorf(call.Pos(), "named arguments and default parameter values are not supported for variadic functions")
		return false
	}
	total := sig.Params().Len()
	if def != nil && len(def.params) != total {
		r.errorf(call.Pos(), "internal error: default parameter descriptor does not match the signature of %s", def.name)
		return false
	}

	slots := make([]ast.Expr, total)
	positional := 0
	seenNamed := false
	for _, arg := range call.Args {
		marker, named := isMarkerCall(arg, markerNamed)
		if !named {
			if seenNamed {
				r.errorf(arg.Pos(), "positional arguments must come before named arguments")
				return false
			}
			if positional >= total {
				r.errorf(arg.Pos(), "too many arguments in call")
				return false
			}
			slots[positional] = arg
			positional++
			continue
		}
		seenNamed = true
		name, err := unquote(marker.Args[0].(*ast.BasicLit).Value)
		if err != nil {
			r.errorf(arg.Pos(), "invalid named argument")
			return false
		}
		idx := paramIndex(sig, name)
		if idx < 0 {
			r.errorf(arg.Pos(), "unknown named argument %q", name)
			return false
		}
		if slots[idx] != nil {
			r.errorf(arg.Pos(), "argument %q is specified more than once", name)
			return false
		}
		slots[idx] = marker.Args[1]
	}

	var missing []int
	for i, slot := range slots {
		if slot == nil {
			missing = append(missing, i)
		}
	}
	if len(missing) == 0 {
		call.Args = slots
		r.changed = true
		return true
	}

	if def == nil {
		r.errorf(call.Pos(), "missing argument %q in call", sig.Params().At(missing[0]).Name())
		return false
	}
	for _, i := range missing {
		if i < def.required {
			r.errorf(call.Pos(), "missing required argument %q in call of %s", def.params[i].name, def.name)
			return false
		}
	}

	args := append([]ast.Expr{}, slots[:def.required]...)
	for i := def.required; i < total; i++ {
		if slots[i] == nil {
			typeExpr, err := parseTypeSrc(def.params[i].typeSrc)
			if err != nil {
				r.errorf(call.Pos(), "invalid parameter type %q in %s", def.params[i].typeSrc, def.name)
				return false
			}
			args = append(args, gnNoneExpr(typeExpr))
		} else {
			args = append(args, gnSomeExpr(slots[i]))
		}
	}
	call.Fun = ast.NewIdent(def.name + defaultSuffix)
	call.Args = args
	r.changed = true
	return true
}

func paramIndex(sig *types.Signature, name string) int {
	for i := 0; i < sig.Params().Len(); i++ {
		if sig.Params().At(i).Name() == name {
			return i
		}
	}
	return -1
}

type extensionCandidate struct {
	fn    *types.Func
	alias string
}

func (r *rewriter) rewriteExtensionCall(call *ast.CallExpr, sel *ast.SelectorExpr) (*types.Signature, bool) {
	if x, ok := sel.X.(*ast.Ident); ok {
		if _, isPkg := r.info.Uses[x].(*types.PkgName); isPkg {
			return nil, false
		}
	}
	if !r.isExtensionName(sel.Sel.Name) {
		return nil, false
	}
	if _, isSelection := r.info.Selections[sel]; isSelection {
		return nil, false
	}
	recvType := r.info.TypeOf(sel.X)
	if !validType(recvType) {
		r.defer_(call.Pos(), "receiver type of .%s(...) is not known yet", sel.Sel.Name)
		return nil, false
	}
	recvType = concreteType(recvType)

	var matched *extensionCandidate
	for _, cand := range r.extensionCandidates(sel.Sel.Name) {
		sig := cand.fn.Type().(*types.Signature)
		if sig.Params().Len() == 0 {
			continue
		}
		ok := false
		if sig.TypeParams() != nil {
			ok = unify(sig.Params().At(0).Type(), recvType, bindings{})
		} else {
			ok = types.AssignableTo(recvType, sig.Params().At(0).Type())
		}
		if !ok {
			continue
		}
		if matched != nil {
			r.errorf(call.Pos(), "ambiguous extension method %s for receiver type %s", sel.Sel.Name, recvType)
			return nil, false
		}
		matched = &cand
	}
	if matched == nil {
		r.errorf(call.Pos(), "no extension function %s matches receiver type %s", sel.Sel.Name, recvType)
		return nil, false
	}

	if matched.alias == "" {
		call.Fun = ast.NewIdent(matched.fn.Name())
	} else {
		call.Fun = &ast.SelectorExpr{X: ast.NewIdent(matched.alias), Sel: ast.NewIdent(matched.fn.Name())}
	}
	call.Args = append([]ast.Expr{sel.X}, call.Args...)
	r.changed = true
	return matched.fn.Type().(*types.Signature), true
}

func (r *rewriter) isExtensionName(name string) bool {
	if r.st.norm.extensions[name] {
		return true
	}
	for alias := range r.st.norm.extImports {
		if imported := r.importedPackage(alias); imported != nil {
			if _, ok := imported.Scope().Lookup(name).(*types.Func); ok {
				return true
			}
		}
	}
	return false
}

func (r *rewriter) extensionCandidates(name string) []extensionCandidate {
	var out []extensionCandidate
	if r.st.norm.extensions[name] {
		if fn, ok := r.pkg.Scope().Lookup(name).(*types.Func); ok {
			out = append(out, extensionCandidate{fn: fn})
		}
	}
	for alias := range r.st.norm.extImports {
		imported := r.importedPackage(alias)
		if imported == nil {
			continue
		}
		if fn, ok := imported.Scope().Lookup(name).(*types.Func); ok && fn.Exported() {
			out = append(out, extensionCandidate{fn: fn, alias: alias})
		}
	}
	return out
}

func (r *rewriter) convertShortFuncLit(lit *ast.ShortFuncLit) ast.Expr {
	sig, ok := r.info.TypeOf(lit).(*types.Signature)
	if !ok {
		r.defer_(lit.Pos(), "the expected type of the short anonymous function is not known yet")
		return lit
	}

	params := &ast.FieldList{}
	for i, name := range lit.Params {
		typeExpr, err := typeToExpr(sig.Params().At(i).Type(), r.pkg, r.st.file)
		if err != nil {
			r.errorf(lit.Pos(), "%v", err)
			return lit
		}
		params.List = append(params.List, &ast.Field{
			Names: []*ast.Ident{ast.NewIdent(name.Name)},
			Type:  typeExpr,
		})
	}
	var results *ast.FieldList
	if sig.Results().Len() > 0 {
		results = &ast.FieldList{}
		for i := 0; i < sig.Results().Len(); i++ {
			typeExpr, err := typeToExpr(sig.Results().At(i).Type(), r.pkg, r.st.file)
			if err != nil {
				r.errorf(lit.Pos(), "%v", err)
				return lit
			}
			results.List = append(results.List, &ast.Field{Type: typeExpr})
		}
	}

	r.changed = true
	return &ast.FuncLit{
		Type: &ast.FuncType{Params: params, Results: results},
		Body: lit.Body,
	}
}

func (r *rewriter) instantiateCtor(target types.Type, e ast.Expr) {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return
	}
	fun, ok := call.Fun.(*ast.Ident)
	if !ok {
		return
	}
	def := r.st.variantToEnum[fun.Name]
	if def == nil || len(def.paramNames) == 0 {
		return
	}
	if obj := r.info.Uses[fun]; obj != nil {
		if _, isFunc := obj.(*types.Func); !isFunc {
			return
		}
	}
	named, ok := types.Unalias(target).(*types.Named)
	if !ok || named.Obj().Name() != def.name || named.Obj().Pkg() != r.pkg {
		return
	}
	args := named.TypeArgs()
	if args.Len() != len(def.paramNames) {
		return
	}
	indices := make([]ast.Expr, args.Len())
	for i := range indices {
		expr, err := typeToExpr(args.At(i), r.pkg, r.st.file)
		if err != nil {
			return
		}
		indices[i] = expr
	}
	if len(indices) == 1 {
		call.Fun = &ast.IndexExpr{X: fun, Index: indices[0]}
	} else {
		call.Fun = &ast.IndexListExpr{X: fun, Indices: indices}
	}
	r.changed = true
}

type matchClause struct {
	bindings []string
	guard    ast.Expr
	body     []ast.Stmt
}

func (r *rewriter) buildMatch(sw *ast.SwitchStmt) (ast.Stmt, bool) {
	tag := sw.Tag.(*ast.CallExpr)
	operand := tag.Args[0]
	opType := r.info.TypeOf(operand)
	if !validType(opType) {
		r.defer_(sw.Pos(), "the type of the match operand is not known yet")
		return nil, false
	}
	opType = concreteType(opType)
	lookupType := opType
	if ptr, ok := lookupType.Underlying().(*types.Pointer); ok {
		lookupType = ptr.Elem()
	}
	named, ok := types.Unalias(lookupType).(*types.Named)
	if !ok {
		r.errorf(sw.Pos(), "match requires an enum value, got %s", opType)
		return nil, false
	}
	def := r.st.enums[named.Obj().Name()]
	if def == nil || named.Obj().Pkg() != r.pkg {
		r.errorf(sw.Pos(), "match requires an enum value, got %s", opType)
		return nil, false
	}

	matchObj, _, _ := types.LookupFieldOrMethod(opType, true, r.pkg, "Match")
	matchFn, ok := matchObj.(*types.Func)
	if !ok {
		r.errorf(sw.Pos(), "internal error: enum %s has no generated Match method", def.name)
		return nil, false
	}
	matchSig := matchFn.Type().(*types.Signature)

	perVariant := map[string][]*matchClause{}
	var defaultBody []ast.Stmt
	hasDefault := false
	for _, stmt := range sw.Body.List {
		cc, ok := stmt.(*ast.CaseClause)
		if !ok {
			r.errorf(stmt.Pos(), "unexpected statement inside match")
			return nil, false
		}
		if cc.List == nil {
			hasDefault = true
			defaultBody = cc.Body
			continue
		}
		for _, expr := range cc.List {
			guard, patterns, err := parseMatchPatterns(expr)
			if err != nil {
				r.errorf(expr.Pos(), "%v", err)
				return nil, false
			}
			for _, p := range patterns {
				variant := def.variant(p.variant)
				if variant == nil {
					r.errorf(expr.Pos(), "enum %s has no variant %s", def.name, p.variant)
					return nil, false
				}
				if len(p.bindings) != len(variant.fields) {
					r.errorf(expr.Pos(), "variant %s of enum %s has %d fields, but the pattern binds %d",
						p.variant, def.name, len(variant.fields), len(p.bindings))
					return nil, false
				}
				perVariant[p.variant] = append(perVariant[p.variant], &matchClause{
					bindings: p.bindings,
					guard:    guard,
					body:     cc.Body,
				})
			}
		}
	}

	var callbacks []ast.Expr
	for i, variant := range def.variants {
		clauses := perVariant[variant.name]
		if len(clauses) == 0 && !hasDefault {
			r.errorf(sw.Pos(), "non-exhaustive match: variant %s of enum %s is not handled and there is no default case",
				variant.name, def.name)
			return nil, false
		}
		cbSig, ok := matchSig.Params().At(i).Type().Underlying().(*types.Signature)
		if !ok {
			r.errorf(sw.Pos(), "internal error: unexpected Match signature for enum %s", def.name)
			return nil, false
		}
		callback, err := r.buildMatchCallback(cbSig, clauses, defaultBody)
		if err != nil {
			r.errorf(sw.Pos(), "%v", err)
			return nil, false
		}
		callbacks = append(callbacks, callback)
	}

	r.changed = true
	return &ast.ExprStmt{X: &ast.CallExpr{
		Fun:  &ast.SelectorExpr{X: operand, Sel: ast.NewIdent("Match")},
		Args: callbacks,
	}}, true
}

type matchPattern struct {
	variant  string
	bindings []string
}

func parseMatchPatterns(expr ast.Expr) (ast.Expr, []matchPattern, error) {
	var guard ast.Expr
	if marker, ok := isMarkerCall(expr, markerGuard); ok {
		guard = marker.Args[1]
		expr = marker.Args[0]
	}
	var patterns []matchPattern
	var flatten func(e ast.Expr) error
	flatten = func(e ast.Expr) error {
		switch v := e.(type) {
		case *ast.BinaryExpr:
			if v.Op != token.OR {
				return fmt.Errorf("invalid match pattern")
			}
			if err := flatten(v.X); err != nil {
				return err
			}
			return flatten(v.Y)
		case *ast.Ident:
			patterns = append(patterns, matchPattern{variant: v.Name})
			return nil
		case *ast.CallExpr:
			id, ok := v.Fun.(*ast.Ident)
			if !ok {
				return fmt.Errorf("invalid match pattern")
			}
			p := matchPattern{variant: id.Name}
			for _, arg := range v.Args {
				binding, ok := arg.(*ast.Ident)
				if !ok {
					return fmt.Errorf("match patterns may only bind plain identifiers")
				}
				p.bindings = append(p.bindings, binding.Name)
			}
			patterns = append(patterns, p)
			return nil
		case *ast.ParenExpr:
			return flatten(v.X)
		default:
			return fmt.Errorf("invalid match pattern")
		}
	}
	if err := flatten(expr); err != nil {
		return nil, nil, err
	}
	return guard, patterns, nil
}

func (r *rewriter) buildMatchCallback(cbSig *types.Signature, clauses []*matchClause, defaultBody []ast.Stmt) (ast.Expr, error) {
	fieldCount := cbSig.Params().Len()

	paramNames := make([]string, fieldCount)
	for i := range paramNames {
		paramNames[i] = "_"
	}
	for _, cl := range clauses {
		for i, binding := range cl.bindings {
			if paramNames[i] == "_" && binding != "_" {
				paramNames[i] = binding
			}
		}
	}

	params := &ast.FieldList{}
	for i := range fieldCount {
		typeExpr, err := typeToExpr(cbSig.Params().At(i).Type(), r.pkg, r.st.file)
		if err != nil {
			return nil, err
		}
		params.List = append(params.List, &ast.Field{
			Names: []*ast.Ident{ast.NewIdent(paramNames[i])},
			Type:  typeExpr,
		})
	}

	var stmts []ast.Stmt
	terminal := false
	for idx, cl := range clauses {
		var rebinds []ast.Stmt
		for i, binding := range cl.bindings {
			if binding != "_" && binding != paramNames[i] {
				rebinds = append(rebinds, &ast.AssignStmt{
					Lhs: []ast.Expr{ast.NewIdent(binding)},
					Tok: token.DEFINE,
					Rhs: []ast.Expr{ast.NewIdent(paramNames[i])},
				})
			}
		}
		if cl.guard == nil {
			stmts = append(stmts, rebinds...)
			stmts = append(stmts, cl.body...)
			terminal = true
			break
		}
		followed := idx < len(clauses)-1 || len(defaultBody) > 0
		guardedBody := append([]ast.Stmt{}, cl.body...)
		if followed {
			guardedBody = append(guardedBody, &ast.ReturnStmt{})
		}
		guarded := []ast.Stmt{&ast.IfStmt{Cond: cl.guard, Body: &ast.BlockStmt{List: guardedBody}}}
		if len(rebinds) > 0 {
			guarded = []ast.Stmt{&ast.BlockStmt{List: append(rebinds, guarded...)}}
		}
		stmts = append(stmts, guarded...)
	}
	if !terminal {
		stmts = append(stmts, defaultBody...)
	}

	return &ast.FuncLit{
		Type: &ast.FuncType{Params: params},
		Body: &ast.BlockStmt{List: stmts},
	}, nil
}
