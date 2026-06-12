package transpiler

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

type enumDef struct {
	name       string
	typeParams string
	paramNames []string
	variants   []enumVariant
}

type enumVariant struct {
	name   string
	fields []string
}

func (d *enumDef) typeArgs() string {
	if len(d.paramNames) == 0 {
		return ""
	}
	return "[" + strings.Join(d.paramNames, ", ") + "]"
}

func (d *enumDef) variantStruct(variant string) string {
	return lowerFirst(d.name) + "_" + variant
}

func (d *enumDef) variant(name string) *enumVariant {
	for i := range d.variants {
		if d.variants[i].name == name {
			return &d.variants[i]
		}
	}
	return nil
}

func (d *enumDef) hasFieldVariants() bool {
	for _, v := range d.variants {
		if len(v.fields) > 0 {
			return true
		}
	}
	return false
}

func callbackName(variant string) string {
	return lowerFirst(variant) + "Func"
}

func lowerFirst(s string) string {
	r, size := utf8.DecodeRuneInString(s)
	return string(unicode.ToLower(r)) + s[size:]
}

func (d *enumDef) generate() string {
	var b strings.Builder
	tp, ta := d.typeParams, d.typeArgs()

	fmt.Fprintf(&b, "type %s%s struct {\n\tinner any\n}\n\n", d.name, tp)

	for _, v := range d.variants {
		vt := d.variantStruct(v.name)
		fmt.Fprintf(&b, "type %s%s struct {\n", vt, tp)
		for fi, ft := range v.fields {
			fmt.Fprintf(&b, "\tf%d %s\n", fi, ft)
		}
		fmt.Fprintf(&b, "}\n\n")

		var params, inits []string
		for fi, ft := range v.fields {
			params = append(params, fmt.Sprintf("v%d %s", fi, ft))
			inits = append(inits, fmt.Sprintf("f%d: v%d", fi, fi))
		}
		fmt.Fprintf(&b, "func %s%s(%s) %s%s {\n\treturn %s%s{inner: %s%s{%s}}\n}\n\n",
			v.name, tp, strings.Join(params, ", "), d.name, ta,
			d.name, ta, vt, ta, strings.Join(inits, ", "))
	}

	var callbacks []string
	for _, v := range d.variants {
		callbacks = append(callbacks, fmt.Sprintf("%s func(%s)", callbackName(v.name), strings.Join(v.fields, ", ")))
	}
	fmt.Fprintf(&b, "func (self %s%s) Match(%s) {\n", d.name, ta, strings.Join(callbacks, ", "))
	if d.hasFieldVariants() {
		b.WriteString("\tswitch v := self.inner.(type) {\n")
	} else {
		b.WriteString("\tswitch self.inner.(type) {\n")
	}
	for _, v := range d.variants {
		fmt.Fprintf(&b, "\tcase %s%s:\n\t\t%s(", d.variantStruct(v.name), ta, callbackName(v.name))
		var args []string
		for fi := range v.fields {
			args = append(args, fmt.Sprintf("v.f%d", fi))
		}
		b.WriteString(strings.Join(args, ", "))
		b.WriteString(")\n")
	}
	b.WriteString("\t}\n}\n\n")

	for _, v := range d.variants {
		vt := d.variantStruct(v.name)
		fmt.Fprintf(&b, "func (self %s%s) Is%s() bool {\n\t_, ok := self.inner.(%s%s)\n\treturn ok\n}\n\n",
			d.name, ta, v.name, vt, ta)
		if len(v.fields) == 0 {
			fmt.Fprintf(&b, "func (self %s%s) %s() bool {\n\treturn self.Is%s()\n}\n\n",
				d.name, ta, v.name, v.name)
			continue
		}
		results := []string{"ok bool"}
		rets := []string{"true"}
		for fi, ft := range v.fields {
			results = append(results, fmt.Sprintf("f%d %s", fi, ft))
			rets = append(rets, fmt.Sprintf("v.f%d", fi))
		}
		fmt.Fprintf(&b, "func (self %s%s) %s() (%s) {\n\tv, isSet := self.inner.(%s%s)\n\tif !isSet {\n\t\treturn\n\t}\n\treturn %s\n}\n\n",
			d.name, ta, v.name, strings.Join(results, ", "), vt, ta, strings.Join(rets, ", "))
	}

	return b.String()
}
