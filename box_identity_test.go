package simdjson

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
)

// TestBoxFloatIdentity pins the slab-boxed float to a convT64 one: DeepEqual
// against stdlib, %T, assertion and switch all see float64.
func TestBoxFloatIdentity(t *testing.T) {
	in := []byte(`{"a":[1.5,2,-0.25,1e10],"b":3.75}`)
	var got, want any
	if err := Unmarshal(in, &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(in, &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%v != %v", got, want)
	}
	m := got.(map[string]any)
	for _, e := range m["a"].([]any) {
		if fmt.Sprintf("%T", e) != "float64" {
			t.Fatalf("element type %T", e)
		}
		if _, ok := e.(float64); !ok {
			t.Fatal("type assertion failed")
		}
	}
	switch m["b"].(type) {
	case float64:
	default:
		t.Fatalf("switch sees %T", m["b"])
	}
}
