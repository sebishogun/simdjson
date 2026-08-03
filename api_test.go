package simdjson

import (
	"testing"
	"time"
)

const apiDoc = `{
  "user": {"name": "ada", "id": 42, "since": "2015-06-05T14:32:11Z"},
  "tags": ["a", "b", "c"],
  "nested": {"deep": {"leaf": true}}
}`

func TestValueGetRelative(t *testing.T) {
	d := MustParse([]byte(apiDoc))
	if got := d.Root().Get("user", "name").String(); got != "ada" {
		t.Errorf("Get(user,name) = %q", got)
	}
	// The point of the relative form: hold the subtree, ask it questions.
	u := d.Root().Key("user")
	if got := u.Get("name").String(); got != "ada" {
		t.Errorf("relative Get(name) = %q", got)
	}
	if u.Get("nope").Exists() {
		t.Error("missing key reported as existing")
	}
	if d.Root().Get("nested", "deep", "leaf").Bool() != true {
		t.Error("three-deep Get failed")
	}
}

func TestGetMany(t *testing.T) {
	got := GetMany([]byte(apiDoc),
		[]string{"user", "name"},
		[]string{"user", "id"},
		[]string{"nested", "deep", "leaf"},
		[]string{"absent"},
	)
	if len(got) != 4 {
		t.Fatalf("got %d results", len(got))
	}
	if got[0].String() != "ada" {
		t.Errorf("[0] = %q", got[0].String())
	}
	if got[1].Int() != 42 {
		t.Errorf("[1] = %d", got[1].Int())
	}
	if got[2].Bool() != true {
		t.Errorf("[2] = %v", got[2].Bool())
	}
	if got[3].Exists() {
		t.Error("[3] absent path reported as existing")
	}
	// Bad input gives all-Invalid rather than an error, matching gjson.
	for i, v := range GetMany([]byte(`{`), []string{"a"}, []string{"b"}) {
		if v.Exists() {
			t.Errorf("bad input result %d exists", i)
		}
	}
}

func TestSkip(t *testing.T) {
	for _, c := range []struct {
		in         string
		start, end int
		ok         bool
	}{
		{`{}`, 0, 2, true},
		{`  {"a":1}  `, 2, 9, true},
		{`[1,2,3] trailing`, 0, 7, true},
		{`"str"`, 0, 5, true},
		{`123`, 0, 3, true},
		{`{`, 0, 0, false},
		{``, 0, 0, false},
	} {
		start, end, ok := Skip([]byte(c.in))
		if ok != c.ok || (ok && (start != c.start || end != c.end)) {
			t.Errorf("Skip(%q) = %d, %d, %v; want %d, %d, %v",
				c.in, start, end, ok, c.start, c.end, c.ok)
		}
		if ok {
			if _, err := Parse([]byte(c.in)[start:end]); err != nil {
				t.Errorf("Skip(%q) span %q does not parse: %v", c.in, c.in[start:end], err)
			}
		}
	}
}

func TestMustParsePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("MustParse did not panic on bad input")
		}
	}()
	MustParse([]byte(`{`))
}

func TestRangeAPIs(t *testing.T) {
	d := MustParse([]byte(apiDoc))

	var tags []string
	for i, e := range d.Root().Key("tags").All() {
		if i != len(tags) {
			t.Errorf("All index = %d, want %d", i, len(tags))
		}
		tags = append(tags, e.String())
	}
	if len(tags) != 3 || tags[0] != "a" || tags[2] != "c" {
		t.Errorf("All over array = %v", tags)
	}

	var keys []string
	for k := range d.Root().Key("user").Keys() {
		keys = append(keys, k)
	}
	if len(keys) != 3 {
		t.Errorf("Keys = %v", keys)
	}

	n := 0
	for k, v := range d.Root().Key("user").Members() {
		if k == "id" && v.Int() != 42 {
			t.Errorf("Members id = %d", v.Int())
		}
		n++
	}
	if n != 3 {
		t.Errorf("Members yielded %d", n)
	}

	n = 0
	for range d.Root().Key("tags").Values() {
		n++
	}
	if n != 3 {
		t.Errorf("Values over array yielded %d", n)
	}

	// Wrong kind ranges over nothing rather than panicking.
	for range d.Root().Key("user").All() {
		t.Error("All over an object yielded something")
	}
	for range d.Root().Key("tags").Keys() {
		t.Error("Keys over an array yielded something")
	}

	// Breaking out early has to stop the walk.
	seen := 0
	for range d.Root().Key("tags").Values() {
		seen++
		break
	}
	if seen != 1 {
		t.Errorf("break did not stop the range: saw %d", seen)
	}
}

func TestValueTime(t *testing.T) {
	d := MustParse([]byte(apiDoc))
	got := d.Root().Get("user", "since").Time()
	want := time.Date(2015, 6, 5, 14, 32, 11, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("Time = %v, want %v", got, want)
	}
	if !d.Root().Get("user", "id").Time().IsZero() {
		t.Error("Time on a number was not zero")
	}
	if !d.Root().Get("user", "name").Time().IsZero() {
		t.Error("Time on an unparseable string was not zero")
	}
}
