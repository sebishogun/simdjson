// Command structgen emits a simdjson encoder for named struct types.
//
// The compiled encoder walks a table of fields. Generated code has the offsets
// and key bytes as constants, which is worth 12.4% through MarshalTo on a real
// document -- see simdjson's register.go for why, and for the measurement.
//
// Usage:
//
//	go run github.com/sebishogun/simdjson/tools/structgen -types User,Status
//
// It writes simdjson_gen.go in the package directory, with an init that
// registers an encoder for each type. Nothing else changes: Marshal picks the
// registered encoder up for the type wherever it appears.
//
// IT DECLINES WHAT IT CANNOT DO EXACTLY, and says so on stderr. A type it
// declines keeps the reflect encoder, which is correct and slower. Partial
// coverage is fine; wrong coverage is not, because a registered encoder that
// disagrees with Marshal produces wrong JSON with no error at all.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"go/format"
	"go/importer"
	"go/token"
	"go/types"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
)

func main() {
	dir := flag.String("dir", ".", "package directory")
	out := flag.String("o", "simdjson_gen.go", "output file; relative paths are taken against -dir")
	list := flag.String("types", "", "comma-separated struct type names (required)")
	flag.Parse()

	if *list == "" {
		fmt.Fprintln(os.Stderr, "structgen: -types is required")
		os.Exit(2)
	}
	if err := run(*dir, *out, strings.Split(*list, ",")); err != nil {
		fmt.Fprintln(os.Stderr, "structgen:", err)
		os.Exit(1)
	}
}

func run(dir, out string, names []string) error {
	pkg, err := load(dir)
	if err != nil {
		return err
	}
	g := &gen{pkg: pkg, done: map[string]bool{}}
	for _, name := range names {
		name = strings.TrimSpace(name)
		obj := pkg.Scope().Lookup(name)
		if obj == nil {
			return fmt.Errorf("%s: no such type in package %s", name, pkg.Name())
		}
		named, ok := obj.Type().(*types.Named)
		if !ok {
			return fmt.Errorf("%s: not a named type", name)
		}
		if err := g.emitType(named); err != nil {
			fmt.Fprintf(os.Stderr, "structgen: declined %s: %v\n", name, err)
			continue
		}
		g.registered = append(g.registered, name)
	}
	if len(g.registered) == 0 {
		return fmt.Errorf("no types could be generated")
	}
	src, err := g.file()
	if err != nil {
		return err
	}
	if !filepath.IsAbs(out) {
		out = filepath.Join(dir, out)
	}
	return os.WriteFile(out, src, 0o644)
}

func load(dir string) (*types.Package, error) {
	// The source importer wants an import path, so ask the toolchain what this
	// directory is called. A generator runs under go:generate, in a module,
	// where `go list` is always available and always right -- reimplementing
	// module resolution here would be a second answer to a settled question.
	cmd := exec.Command("go", "list", "-f", "{{.ImportPath}}")
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	outb, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list in %s: %v: %s", dir, err, stderr.String())
	}
	path := strings.TrimSpace(string(outb))
	// The source importer reads the package the same way the compiler does, so
	// tags, embedding and export rules are the real ones rather than a
	// re-implementation of them.
	return importer.ForCompiler(token.NewFileSet(), "source", nil).Import(path)
}

type gen struct {
	pkg        *types.Package
	buf        bytes.Buffer
	done       map[string]bool
	registered []string
}

// emitType writes an encoder for named, and for every struct it contains,
// depth first so a nested type is defined before its user.
func (g *gen) emitType(named *types.Named) error {
	st, ok := named.Underlying().(*types.Struct)
	if !ok {
		return fmt.Errorf("not a struct")
	}
	if g.done[named.Obj().Name()] {
		return nil
	}
	g.done[named.Obj().Name()] = true

	fields, err := plan(st)
	if err != nil {
		return err
	}
	// Nested struct types first, so their functions exist.
	for _, f := range fields {
		if f.nested != nil {
			if err := g.emitType(f.nested); err != nil {
				return fmt.Errorf("field %s: %w", f.goName, err)
			}
		}
	}

	name := named.Obj().Name()
	fmt.Fprintf(&g.buf, "\nfunc encode%s(dst []byte, p unsafe.Pointer, o simdjson.Options) []byte {\n", name)
	fmt.Fprintf(&g.buf, "\tv := (*%s)(p)\n\t_ = v\n", name)
	for i, f := range fields {
		open := ","
		if i == 0 {
			open = "{"
		}
		fmt.Fprintf(&g.buf, "\tdst = append(dst, %s...)\n",
			strconv.Quote(open+strconv.Quote(f.jsonName)+":"))
		g.emitValue("v."+f.goName, f)
	}
	if len(fields) == 0 {
		g.buf.WriteString("\tdst = append(dst, '{')\n")
	}
	g.buf.WriteString("\treturn append(dst, '}')\n}\n")
	return nil
}

func (g *gen) emitValue(expr string, f field) {
	switch {
	case f.nested != nil:
		fmt.Fprintf(&g.buf, "\tdst = encode%s(dst, unsafe.Pointer(&%s), o)\n",
			f.nested.Obj().Name(), expr)
	case f.slice != nil:
		// A nil slice is null and an empty one is [], which is
		// encoding/json's rule and not the same distinction Go's range makes.
		// Emitting [] for both was the first thing the differential caught.
		fmt.Fprintf(&g.buf, "\tif %s == nil {\n", expr)
		fmt.Fprintf(&g.buf, "\t\tdst = append(dst, \"null\"...)\n")
		fmt.Fprintf(&g.buf, "\t} else {\n")
		fmt.Fprintf(&g.buf, "\t\tdst = append(dst, '[')\n")
		fmt.Fprintf(&g.buf, "\t\tfor i := range %s {\n", expr)
		fmt.Fprintf(&g.buf, "\t\t\tif i > 0 {\n\t\t\t\tdst = append(dst, ',')\n\t\t\t}\n")
		g.emitValue(fmt.Sprintf("%s[i]", expr), *f.slice)
		fmt.Fprintf(&g.buf, "\t\t}\n\t\tdst = append(dst, ']')\n\t}\n")
	case f.basic == types.String:
		fmt.Fprintf(&g.buf, "\tdst = simdjson.AppendString(dst, %s, o)\n", expr)
	case f.basic == types.Bool:
		fmt.Fprintf(&g.buf, "\tdst = simdjson.AppendBool(dst, %s)\n", expr)
	case isSigned(f.basic):
		fmt.Fprintf(&g.buf, "\tdst = simdjson.AppendInt(dst, int64(%s))\n", expr)
	case isUnsigned(f.basic):
		fmt.Fprintf(&g.buf, "\tdst = simdjson.AppendUint(dst, uint64(%s))\n", expr)
	case f.basic == types.Float32:
		fmt.Fprintf(&g.buf, "\tdst = simdjson.AppendFloat(dst, float64(%s), 32)\n", expr)
	case f.basic == types.Float64:
		fmt.Fprintf(&g.buf, "\tdst = simdjson.AppendFloat(dst, %s, 64)\n", expr)
	}
}

func (g *gen) file() ([]byte, error) {
	var head bytes.Buffer
	head.WriteString("// Code generated by simdjson/tools/structgen. DO NOT EDIT.\n\n")
	fmt.Fprintf(&head, "package %s\n\n", g.pkg.Name())
	head.WriteString("import (\n\t\"unsafe\"\n\n\t\"github.com/sebishogun/simdjson\"\n)\n\n")
	head.WriteString("func init() {\n")
	for _, n := range g.registered {
		fmt.Fprintf(&head, "\tsimdjson.RegisterEncoder[%s](encode%s)\n", n, n)
	}
	head.WriteString("}\n")
	head.Write(g.buf.Bytes())
	return format.Source(head.Bytes())
}

type field struct {
	goName   string
	jsonName string
	basic    types.BasicKind
	nested   *types.Named
	slice    *field
}

// plan describes what to emit for a struct, or refuses.
//
// Every refusal is a shape whose output this cannot be SURE matches Marshal's.
// The cost of being wrong is silent: wrong JSON, no error. So the rule is to
// decline anything not fully understood rather than guess.
func plan(st *types.Struct) ([]field, error) {
	var out []field
	for i := 0; i < st.NumFields(); i++ {
		f := st.Field(i)
		if !f.Exported() {
			continue
		}
		if f.Embedded() {
			return nil, fmt.Errorf("embedded field %s: promotion rules not implemented", f.Name())
		}
		tag := reflect.StructTag(st.Tag(i)).Get("json")
		name, opts, _ := strings.Cut(tag, ",")
		if name == "-" && opts == "" {
			continue
		}
		if opts != "" {
			return nil, fmt.Errorf("field %s: tag option %q not implemented", f.Name(), opts)
		}
		if name == "" {
			name = f.Name()
		}
		if !plainKey(name) {
			return nil, fmt.Errorf("field %s: key %q would need escaping", f.Name(), name)
		}
		fd, err := describe(f.Type())
		if err != nil {
			return nil, fmt.Errorf("field %s: %w", f.Name(), err)
		}
		fd.goName, fd.jsonName = f.Name(), name
		out = append(out, fd)
	}
	return out, nil
}

func describe(t types.Type) (field, error) {
	// A type with its own marshalling is the type's business, not ours, and
	// spotting it needs the method set rather than the shape.
	if hasMarshalMethod(t) {
		return field{}, fmt.Errorf("has its own MarshalJSON or MarshalText")
	}
	switch u := t.Underlying().(type) {
	case *types.Basic:
		switch k := u.Kind(); k {
		case types.String, types.Bool,
			types.Int, types.Int8, types.Int16, types.Int32, types.Int64,
			types.Uint, types.Uint8, types.Uint16, types.Uint32, types.Uint64,
			types.Uintptr, types.Float32, types.Float64:
			return field{basic: k}, nil
		default:
			return field{}, fmt.Errorf("unsupported basic kind %s", u)
		}
	case *types.Struct:
		named, ok := t.(*types.Named)
		if !ok {
			return field{}, fmt.Errorf("anonymous struct")
		}
		return field{nested: named}, nil
	case *types.Slice:
		// A []byte is base64 in JSON, which is a different job.
		if b, ok := u.Elem().Underlying().(*types.Basic); ok && b.Kind() == types.Uint8 {
			return field{}, fmt.Errorf("[]byte encodes as base64")
		}
		elem, err := describe(u.Elem())
		if err != nil {
			return field{}, fmt.Errorf("slice element: %w", err)
		}
		return field{slice: &elem}, nil
	default:
		return field{}, fmt.Errorf("unsupported type %s", t)
	}
}

func hasMarshalMethod(t types.Type) bool {
	for _, name := range []string{"MarshalJSON", "MarshalText"} {
		if m, _, _ := types.LookupFieldOrMethod(t, true, nil, name); m != nil {
			return true
		}
		if m, _, _ := types.LookupFieldOrMethod(types.NewPointer(t), true, nil, name); m != nil {
			return true
		}
	}
	return false
}

// plainKey reports whether a JSON key can be written literally: no byte that
// any Options combination would escape, so one emitted constant is right for
// all of them.
func plainKey(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 || c >= 0x7f || c == '"' || c == '\\' ||
			c == '<' || c == '>' || c == '&' {
			return false
		}
	}
	return true
}

func isSigned(k types.BasicKind) bool {
	switch k {
	case types.Int, types.Int8, types.Int16, types.Int32, types.Int64:
		return true
	}
	return false
}

func isUnsigned(k types.BasicKind) bool {
	switch k {
	case types.Uint, types.Uint8, types.Uint16, types.Uint32, types.Uint64, types.Uintptr:
		return true
	}
	return false
}
