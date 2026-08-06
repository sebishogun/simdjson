package simdjson

// The pathological battery: input shapes that live at the edges — depth,
// giant strings, duplicate keys, escape and unicode density, whitespace
// floods. The bar is agreement with encoding/json (error presence and
// decoded value); where stdlib itself draws an arbitrary line (its 10,000
// depth cap), the test asserts we draw the same one.

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func agreeAll(t *testing.T, name string, doc []byte) {
	t.Helper()
	sv, ov := json.Valid(doc), Valid(doc)
	if sv != ov {
		t.Errorf("%s: Valid ours=%v stdlib=%v", name, ov, sv)
		return
	}
	var oa, sa any
	oerr := Unmarshal(doc, &oa)
	serr := json.Unmarshal(doc, &sa)
	if (oerr == nil) != (serr == nil) {
		t.Errorf("%s: err ours=%v stdlib=%v", name, oerr, serr)
		return
	}
	if oerr == nil && !reflect.DeepEqual(oa, sa) {
		t.Errorf("%s: decoded values differ", name)
	}
}

func TestPathologicalAgreesWithStdlib(t *testing.T) {
	// Depth: both sides of stdlib's cap.
	for _, d := range []int{16, 128, 1024, 9999, 10000, 10001, 20000} {
		doc := strings.Repeat("[", d) + "1" + strings.Repeat("]", d)
		agreeAll(t, "depth-"+itoa(d), []byte(doc))
	}
	// A giant single string, clean and escape-bearing.
	big := strings.Repeat("all work and no play makes a dull payload ", 100000)
	agreeAll(t, "bigstring-clean", []byte(`{"s":"`+big+`"}`))
	agreeAll(t, "bigstring-esc", []byte(`{"s":"`+strings.Repeat(`x\n`, 500000)+`"}`))
	// Duplicate keys: last wins, nested too.
	agreeAll(t, "dupkeys", []byte(`{"a":1,"a":2,"b":{"c":3,"c":4},"a":5}`))
	// Escape-dense and unicode-dense.
	agreeAll(t, "escape-dense", []byte(`{"s":"`+strings.Repeat(`ካ\t\"\\`, 50000)+`"}`))
	agreeAll(t, "unicode-dense", []byte(`{"s":"`+strings.Repeat("héllo wörld 你好 ", 30000)+`"}`))
	// Whitespace floods, between every token and inside nothing.
	sp := strings.Repeat(" \n\t", 20000)
	agreeAll(t, "ws-flood", []byte("{"+sp+`"a"`+sp+":"+sp+"["+sp+"1"+sp+","+sp+"2"+sp+"]"+sp+"}"))
	// Long member chains (wide, not deep).
	var sb strings.Builder
	sb.WriteString("{")
	for i := 0; i < 100000; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(`"k` + itoa(i) + `":` + itoa(i))
	}
	sb.WriteString("}")
	agreeAll(t, "wide-object", []byte(sb.String()))
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
