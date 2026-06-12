package transpiler

import (
	"gonext/internal/frontend/types"
)

type bindings map[*types.TypeParam]types.Type

func unify(pattern, actual types.Type, bind bindings) bool {
	pattern = types.Unalias(pattern)
	actual = types.Unalias(actual)

	if tp, ok := pattern.(*types.TypeParam); ok {
		if bound, ok := bind[tp]; ok {
			return types.Identical(bound, actual)
		}
		bind[tp] = actual
		return true
	}

	switch p := pattern.(type) {
	case *types.Slice:
		a, ok := actual.(*types.Slice)
		return ok && unify(p.Elem(), a.Elem(), bind)
	case *types.Array:
		a, ok := actual.(*types.Array)
		return ok && p.Len() == a.Len() && unify(p.Elem(), a.Elem(), bind)
	case *types.Pointer:
		a, ok := actual.(*types.Pointer)
		return ok && unify(p.Elem(), a.Elem(), bind)
	case *types.Map:
		a, ok := actual.(*types.Map)
		return ok && unify(p.Key(), a.Key(), bind) && unify(p.Elem(), a.Elem(), bind)
	case *types.Chan:
		a, ok := actual.(*types.Chan)
		return ok && p.Dir() == a.Dir() && unify(p.Elem(), a.Elem(), bind)
	case *types.Signature:
		a, ok := actual.(*types.Signature)
		if !ok || p.Variadic() != a.Variadic() {
			return false
		}
		return unifyTuples(p.Params(), a.Params(), bind) && unifyTuples(p.Results(), a.Results(), bind)
	case *types.Named:
		a, ok := actual.(*types.Named)
		if !ok || p.Obj() != a.Obj() {
			return types.Identical(pattern, actual)
		}
		pargs, aargs := p.TypeArgs(), a.TypeArgs()
		if pargs.Len() != aargs.Len() {
			return false
		}
		for i := 0; i < pargs.Len(); i++ {
			if !unify(pargs.At(i), aargs.At(i), bind) {
				return false
			}
		}
		return true
	default:
		return types.Identical(pattern, actual)
	}
}

func unifyTuples(pattern, actual *types.Tuple, bind bindings) bool {
	if pattern.Len() != actual.Len() {
		return false
	}
	for i := 0; i < pattern.Len(); i++ {
		if !unify(pattern.At(i).Type(), actual.At(i).Type(), bind) {
			return false
		}
	}
	return true
}
