package simdjson

import "testing"

const pathDoc = `{
  "user": {"name": "ada", "id": 42, "tags": ["x", "y", "z"]},
  "items": [
    {"id": 1, "label": "first"},
    {"id": 2, "label": "second"}
  ],
  "a.b": "dotted key",
  "weird\\key": "backslash key",
  "nested": {"deep": {"deeper": {"leaf": "bottom"}}}
}`

func TestPath(t *testing.T) {
	d := MustParse([]byte(pathDoc))
	for _, c := range []struct{ path, want string }{
		{"user.name", "ada"},
		{"nested.deep.deeper.leaf", "bottom"},
		{"items.0.label", "first"},
		{"items.1.label", "second"},
		{"user.tags.0", "x"},
		{"user.tags.2", "z"},
		{`a\.b`, "dotted key"},
		{"", ""}, // empty path is the root, an object, so String is ""
	} {
		if got := d.Path(c.path).String(); got != c.want {
			t.Errorf("Path(%q) = %q, want %q", c.path, got, c.want)
		}
	}
	if got := d.Path("user.id").Int(); got != 42 {
		t.Errorf("Path(user.id) = %d", got)
	}
}

func TestPathMissing(t *testing.T) {
	d := MustParse([]byte(pathDoc))
	for _, p := range []string{
		"nope", "user.nope", "user.name.deeper", "items.9", "items.9.label",
		"user.tags.99", "user.0", "items.notanumber",
	} {
		if v := d.Path(p); v.Exists() {
			t.Errorf("Path(%q) exists, want missing (kind %v)", p, v.Kind())
		}
	}
}

func TestPathWildcard(t *testing.T) {
	d := MustParse([]byte(pathDoc))
	// * on an object matches a field name.
	if got := d.Path("user.na*").String(); got != "ada" {
		t.Errorf("Path(user.na*) = %q", got)
	}
	if got := d.Path("user.*me").String(); got != "ada" {
		t.Errorf("Path(user.*me) = %q", got)
	}
	if got := d.Path("us*.name").String(); got != "ada" {
		t.Errorf("Path(us*.name) = %q", got)
	}
	// ? is exactly one character.
	if got := d.Path("user.?ame").String(); got != "ada" {
		t.Errorf("Path(user.?ame) = %q", got)
	}
	if d.Path("user.??ame").Exists() {
		t.Error("Path(user.??ame) matched, want no match")
	}
	// A wildcard over an array takes the first element.
	if got := d.Path("items.*.label").String(); got != "first" {
		t.Errorf("Path(items.*.label) = %q", got)
	}
}

func TestPathRelative(t *testing.T) {
	d := MustParse([]byte(pathDoc))
	u := d.Path("user")
	if got := u.Path("name").String(); got != "ada" {
		t.Errorf("relative Path(name) = %q", got)
	}
	if got := u.Path("tags.1").String(); got != "y" {
		t.Errorf("relative Path(tags.1) = %q", got)
	}
}

func TestGetPath(t *testing.T) {
	if got := GetPath([]byte(pathDoc), "user.name").String(); got != "ada" {
		t.Errorf("GetPath = %q", got)
	}
	// Unparseable input is a miss, not a panic.
	if GetPath([]byte(`{`), "a").Exists() {
		t.Error("GetPath on bad input reported a value")
	}
}

func TestMatchPath(t *testing.T) {
	for _, c := range []struct {
		pat, name string
		want      bool
	}{
		{"*", "anything", true},
		{"*", "", true},
		{"a*", "abc", true},
		{"*c", "abc", true},
		{"a*c", "abc", true},
		{"a*c", "ac", true},
		{"a*b*c", "axxbyyc", true},
		{"?", "a", true},
		{"?", "ab", false},
		{"a?c", "abc", true},
		{"a?c", "ac", false},
		{"abc", "abc", true},
		{"abc", "abd", false},
		{"", "", true},
		{"", "a", false},
		// The shape that is quadratic under naive recursion.
		{"*a*a*a*a*b", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", false},
	} {
		if got := matchPath(c.pat, c.name); got != c.want {
			t.Errorf("matchPath(%q, %q) = %v, want %v", c.pat, c.name, got, c.want)
		}
	}
}
