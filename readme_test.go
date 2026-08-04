package simdjson

// The README names APIs, and names drift.
//
// Two claims in it were false when this was written. It said "No struct
// unmarshalling, no tags, no interfaces, no streaming, no encoding" three
// hundred lines below a list of Marshal, Unmarshal, Decoder and Encoder, and it
// said Decoder.Token was not implemented when token.go had it, tested against
// the standard library and fuzzed. Both were true once. Nothing failed when
// they stopped being true.
//
// A first attempt at fixing that introduced a third: a reference to
// `DecodeArray`, an API this has never had. That is the same defect in the
// other direction, and it is what this test is really for -- a name in the
// README either resolves to something exported or it does not belong there.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"regexp"
	"strings"
	"testing"
)

// exportedNames collects everything a README is allowed to name: exported
// top-level declarations, and exported methods with and without their receiver.
func exportedNames(t *testing.T) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse package: %v", err)
	}
	names := map[string]bool{}
	for _, pkg := range pkgs {
		for _, f := range pkg.Files {
			for _, d := range f.Decls {
				switch decl := d.(type) {
				case *ast.FuncDecl:
					if !decl.Name.IsExported() {
						continue
					}
					names[decl.Name.Name] = true
					if decl.Recv == nil || len(decl.Recv.List) == 0 {
						continue
					}
					// Methods are also citable as Type.Method.
					recv := decl.Recv.List[0].Type
					if star, ok := recv.(*ast.StarExpr); ok {
						recv = star.X
					}
					if id, ok := recv.(*ast.Ident); ok {
						names[id.Name+"."+decl.Name.Name] = true
					}
				case *ast.GenDecl:
					for _, spec := range decl.Specs {
						switch sp := spec.(type) {
						case *ast.TypeSpec:
							if sp.Name.IsExported() {
								names[sp.Name.Name] = true
							}
						case *ast.ValueSpec:
							for _, id := range sp.Names {
								if id.IsExported() {
									names[id.Name] = true
								}
							}
						}
					}
				}
			}
		}
	}
	return names
}

// Things in backticks that look like exported Go names but are not ours. Kept
// explicit and short: every entry is a place the README talks about something
// else, and a long list would mean the test had stopped discriminating.
var readmeForeign = map[string]bool{
	// Standard library and other packages, named as comparisons.
	"Buffer": true, "Reader": true, "Writer": true, "Time": true,
	"Sprintf": true, "Fatal": true, "Error": true, "String": true,
	// Environment variables and build tags.
	"GOSIMD": true, "GOMAXPROCS": true, "GOEXPERIMENT": true, "GOARCH": true,
	// Other libraries' APIs, named in the comparison sections.
	"Get": true, "GetBytes": true, "Result": true, "Iter": true,
	"ParseBytes": true, "GetMany": true, "Object": true, "Array": true,
	// Go types and keywords.
	"Go": true, "JSON": true, "SIMD": true, "AVX2": true, "AVX512": true,
	"NEON": true, "VSX": true, "MB": true, "GB": true, "KiB": true, "GiB": true,
}

// A backticked token that could be a Go name: an exported identifier, or an
// exported identifier qualified by a type. Anything with a space, slash, comma,
// bracket or lowercase start is prose, a tag, a shell command or a Go type.
var readmeIdent = regexp.MustCompile(`^[A-Z][A-Za-z0-9]*(\.[A-Za-z][A-Za-z0-9]*)?$`)

func TestREADMENamesExist(t *testing.T) {
	src, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatalf("README.md: %v", err)
	}
	have := exportedNames(t)

	// Code fences are examples, compiled elsewhere; only prose is checked, so
	// that a fenced block showing another library's API is not a failure.
	text := regexp.MustCompile("(?s)```.*?```").ReplaceAllString(string(src), "")

	seen := map[string]bool{}
	for _, m := range regexp.MustCompile("`([^`\n]+)`").FindAllStringSubmatch(text, -1) {
		name := m[1]
		if seen[name] || !readmeIdent.MatchString(name) || readmeForeign[name] {
			continue
		}
		seen[name] = true
		if have[name] {
			continue
		}
		// Type.Method where the method is ours on some other type still counts:
		// the README often writes Decoder.Decode for a method it shares.
		if i := strings.IndexByte(name, '.'); i >= 0 && have[name[i+1:]] {
			continue
		}
		t.Errorf("README names %q, which this package does not export", name)
	}
	if len(seen) < 20 {
		t.Fatalf("only %d candidate names found in README; the regexp stopped matching", len(seen))
	}
}
