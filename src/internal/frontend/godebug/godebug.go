// Package godebug is a minimal stand-in for internal/godebug, sufficient for
// the vendored go/types: every setting reports its default value.
package godebug

type Setting struct{ name string }

func New(name string) *Setting { return &Setting{name: name} }

func (s *Setting) Name() string { return s.name }

func (s *Setting) Value() string { return "" }

func (s *Setting) IncNonDefault() {}
