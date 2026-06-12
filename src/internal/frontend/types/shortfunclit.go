// Copyright 2026 GoNext. All rights reserved.
// This file is a GoNext extension of the vendored go/types and is not part of
// the upstream sources.

// Type checking of GoNext short function literals "(a, b) => { ... }".
//
// A short literal carries no parameter or result types of its own: they are
// recovered from the expected function type of the context, following the
// contextual type recovery scheme of the GoNext transpiler. Two kinds of
// context are supported:
//
//   - assignment-like contexts (variable initialization, assignment, return
//     statements) provide the expected type through the *target mechanism the
//     checker already uses for those positions (see exprInternal);
//
//   - call arguments receive their expected type from the (possibly generic)
//     signature of the callee in Checker.arguments. For generic callees the
//     type parameters are first partially inferred from the remaining
//     arguments; a result type that is still an unresolved type parameter is
//     recovered from the literal's body, after which the literal participates
//     in the regular inference like any explicitly typed function argument.

package types

import (
	"gonext/internal/frontend/ast"
	. "gonext/internal/frontend/typeserrors"
)

// shortFuncLit type-checks a short function literal in an assignment-like
// context whose expected type is provided by T.
func (check *Checker) shortFuncLit(x *operand, e *ast.ShortFuncLit, T *target) {
	if T == nil || T.sig == nil {
		check.errorf(e, CannotInferTypeArgs, "cannot infer the parameter types of the short function literal: the context does not provide a function type")
		x.mode = invalid
		x.typ = Typ[Invalid]
		return
	}
	sig := check.shortFuncSig(e, T.sig)
	if sig == nil {
		x.mode = invalid
		x.typ = Typ[Invalid]
		return
	}
	x.mode = value
	x.typ = sig
}

// shortFuncSig builds the signature of a short function literal from the
// expected signature, declares the parameters in a new scope, recovers the
// result type from the body when the expected result is an unresolved type
// parameter, and schedules the body for the usual deferred checking.
func (check *Checker) shortFuncSig(e *ast.ShortFuncLit, expected *Signature) *Signature {
	if expected.Variadic() {
		check.errorf(e, CannotInferTypeArgs, "short function literals cannot implement the variadic function type %s", expected)
		return nil
	}
	if expected.Params().Len() != len(e.Params) {
		check.errorf(e, WrongArgCount, "short function literal has %d parameters, but the expected type %s has %d", len(e.Params), expected, expected.Params().Len())
		return nil
	}

	scope := NewScope(check.scope, e.Pos(), e.End(), "function")
	scopePos := e.Body.Pos()
	params := make([]*Var, len(e.Params))
	for i, name := range e.Params {
		ptype := expected.Params().At(i).Type()
		if containsTypeParam(ptype) {
			check.errorf(name, CannotInferTypeArgs, "cannot recover the type of parameter %s: the expected function type %s is generic", name.Name, expected)
			return nil
		}
		par := NewParam(name.Pos(), check.pkg, name.Name, ptype)
		check.declare(scope, name, par, scopePos)
		params[i] = par
	}

	sig := NewSignatureType(nil, nil, nil, NewTuple(params...), nil, false)
	sig.scope = scope

	if n := expected.Results().Len(); n > 0 {
		vars := make([]*Var, n)
		for i := range n {
			rtype := expected.Results().At(i).Type()
			if containsTypeParam(rtype) {
				if n != 1 {
					check.errorf(e, CannotInferTypeArgs, "cannot recover the result types of the short function literal from the generic type %s", expected)
					return nil
				}
				rtype = check.inferShortFuncResult(sig, e.Body)
				if rtype == nil {
					check.errorf(e, CannotInferTypeArgs, "cannot recover the result type of the short function literal from its context or body")
					return nil
				}
			}
			vars[i] = NewVar(nopos, check.pkg, "", rtype)
		}
		sig.results = NewTuple(vars...)
	}

	// Schedule the body like an ordinary function literal (see funcLit).
	if !check.conf.IgnoreFuncBodies && e.Body != nil {
		decl := check.decl
		iota := check.iota
		check.later(func() {
			check.funcBody(decl, "<short function literal>", sig, e.Body, iota)
		}).describef(e, "short func literal")
	}
	return sig
}

// inferShortFuncResult recovers the result type of a short function literal
// from its body: the parameters are already declared in sig.scope, and the
// type of the first well-typed single-value return expression decides.
func (check *Checker) inferShortFuncResult(sig *Signature, body *ast.BlockStmt) Type {
	defer func(env environment) {
		check.environment = env
	}(check.environment)
	check.environment = environment{
		decl:    check.decl,
		scope:   sig.scope,
		version: check.version,
		iota:    check.iota,
		sig:     sig,
	}

	var result Type
	ast.Inspect(body, func(n ast.Node) bool {
		switch ret := n.(type) {
		case *ast.FuncLit, *ast.ShortFuncLit:
			return false // returns of nested literals do not count
		case *ast.ReturnStmt:
			if result == nil && len(ret.Results) == 1 {
				var x operand
				check.expr(nil, &x, ret.Results[0])
				if x.mode != invalid && isValid(x.typ) {
					result = Default(x.typ)
				}
			}
			return false
		}
		return result == nil
	})
	return result
}

// shortFuncArg returns the short function literal of a call argument operand
// that has not been resolved yet (see genericExprList), or nil.
func shortFuncArg(arg *operand) *ast.ShortFuncLit {
	if lit, ok := arg.expr.(*ast.ShortFuncLit); ok && !isValid(arg.typ) {
		return lit
	}
	return nil
}

// resolveShortFuncArgs resolves short function literals among call arguments
// once the expected parameter types are known. For generic callees the type
// parameters are first partially inferred from the other arguments; literals
// resolved here then take part in the regular inference as fully typed
// function arguments. It reports whether checking of the call may proceed.
func (check *Checker) resolveShortFuncArgs(call *ast.CallExpr, sigParams *Tuple, tparams []*TypeParam, targs []Type, args []*operand) bool {
	found := false
	for _, arg := range args {
		if shortFuncArg(arg) != nil {
			found = true
			break
		}
	}
	if !found {
		return true
	}

	// Partial inference from the already typed arguments, so that the
	// expected function types become as concrete as possible.
	var smap substMap
	if len(tparams) > 0 {
		u := newUnifier(tparams, targs, check.allowVersion(go1_21))
		for i, arg := range args {
			if i >= sigParams.Len() {
				break
			}
			if shortFuncArg(arg) != nil || arg.mode == invalid || !isValid(arg.typ) {
				continue
			}
			u.unify(sigParams.vars[i].typ, Default(arg.typ), assign)
		}
		smap = make(substMap)
		for _, tpar := range tparams {
			if t := u.at(tpar); t != nil {
				smap[tpar] = t
			}
		}
	}

	for i, arg := range args {
		lit := shortFuncArg(arg)
		if lit == nil || i >= sigParams.Len() {
			continue
		}
		ptype := sigParams.vars[i].typ
		if len(smap) > 0 {
			ptype = check.subst(call.Pos(), ptype, smap, nil, check.context())
		}
		expected, _ := ptype.Underlying().(*Signature)
		if expected == nil {
			check.errorf(lit, IncompatibleAssign, "cannot use a short function literal as a value of type %s", ptype)
			arg.mode = invalid
			return false
		}
		sig := check.shortFuncSig(lit, expected)
		if sig == nil {
			arg.mode = invalid
			return false
		}
		arg.mode = value
		arg.typ = sig
		check.record(arg)
	}
	return true
}

// containsTypeParam reports whether t contains unresolved type parameters.
func containsTypeParam(t Type) bool {
	switch v := Unalias(t).(type) {
	case *TypeParam:
		return true
	case *Slice:
		return containsTypeParam(v.Elem())
	case *Array:
		return containsTypeParam(v.Elem())
	case *Pointer:
		return containsTypeParam(v.Elem())
	case *Map:
		return containsTypeParam(v.Key()) || containsTypeParam(v.Elem())
	case *Chan:
		return containsTypeParam(v.Elem())
	case *Signature:
		return tupleContainsTypeParam(v.Params()) || tupleContainsTypeParam(v.Results())
	case *Named:
		args := v.TypeArgs()
		for i := 0; i < args.Len(); i++ {
			if containsTypeParam(args.At(i)) {
				return true
			}
		}
		return false
	default:
		return false
	}
}

func tupleContainsTypeParam(tuple *Tuple) bool {
	for i := 0; i < tuple.Len(); i++ {
		if containsTypeParam(tuple.At(i).Type()) {
			return true
		}
	}
	return false
}
