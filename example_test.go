package simdjson_test

import (
	"fmt"

	"github.com/sebishogun/simdjson"
)

// The case this package is for: a few values out of a document, without
// decoding the rest of it.
func Example() {
	data := []byte(`{
		"user": {"name": "ada", "age": 36, "tags": ["math", "engines"]},
		"meta": {"page": 1}
	}`)

	doc, err := simdjson.Parse(data)
	if err != nil {
		fmt.Println("bad json:", err)
		return
	}

	fmt.Println(doc.Get("user", "name").String())
	fmt.Println(doc.Get("user", "age").Int())
	fmt.Println(doc.Get("user", "tags").Index(1).String())
	// Output:
	// ada
	// 36
	// engines
}

// A missing key yields a Value that does not exist rather than an error, so a
// path can be walked without checking every step.
func ExampleValue_Exists() {
	doc, _ := simdjson.Parse([]byte(`{"a":{"b":1}}`))

	fmt.Println(doc.Get("a", "b").Exists())
	fmt.Println(doc.Get("a", "zzz").Exists())
	fmt.Println(doc.Get("nope", "deeper").Exists())
	// Output:
	// true
	// false
	// false
}

// Iterating an array without building one.
func ExampleValue_ForEach() {
	doc, _ := simdjson.Parse([]byte(`{"scores":[10,20,30]}`))

	total := int64(0)
	doc.Get("scores").ForEach(func(v simdjson.Value) bool {
		total += v.Int()
		return true
	})
	fmt.Println(total)
	// Output: 60
}

// Iterating an object's fields.
func ExampleValue_ForEachKey() {
	doc, _ := simdjson.Parse([]byte(`{"a":1,"b":2}`))

	doc.Root().ForEachKey(func(k string, v simdjson.Value) bool {
		fmt.Printf("%s=%d\n", k, v.Int())
		return true
	})
	// Output:
	// a=1
	// b=2
}

// A Parser reuses its index between documents, which is what a server handling
// a stream of payloads wants. The Doc it returns is only valid until the next
// Parse on the same Parser.
func ExampleParser() {
	var p simdjson.Parser

	for _, payload := range [][]byte{
		[]byte(`{"id":1}`),
		[]byte(`{"id":2}`),
	} {
		doc, err := p.Parse(payload)
		if err != nil {
			return
		}
		fmt.Println(doc.Get("id").Int())
	}
	// Output:
	// 1
	// 2
}

// Structure inside a string is text, which is the whole difficulty of stage
// one and is handled before any of it is interpreted.
func ExampleParse_stringsHideStructure() {
	doc, err := simdjson.Parse([]byte(`{"a":"},{\"b\":2},[","c":1}`))
	if err != nil {
		fmt.Println("err:", err)
		return
	}
	fmt.Printf("%q\n", doc.Get("a").String())
	fmt.Println(doc.Get("c").Int())
	// Output:
	// "},{\"b\":2},["
	// 1
}
