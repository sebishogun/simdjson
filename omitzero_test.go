package simdjson

import (
	stdjson "encoding/json"
	"testing"
	"time"
)

type ozStruct struct {
	A int             `json:"a,omitzero"`
	B string          `json:"b,omitzero"`
	C time.Time       `json:"c,omitzero"`
	D []int           `json:"d,omitzero"`
	E *int            `json:"e,omitzero"`
	F int             `json:"f,omitempty"`
	G []int           `json:"g,omitempty"`
	H struct{ X int } `json:"h,omitzero"`
	I map[string]int  `json:"i,omitzero"`
}

// omitzero is not omitempty spelled differently: omitempty drops what looks
// empty and omitzero drops only the zero value, so an empty non-nil slice
// survives one and not the other. The cases below are chosen to separate them.
func TestOmitZeroMatchesStdlib(t *testing.T) {
	n := 5
	var withX ozStruct
	withX.H.X = 1
	cases := []ozStruct{
		{},
		{A: 1, B: "x", C: time.Unix(0, 0).UTC(), D: []int{}, E: &n, F: 2, G: []int{}},
		{D: []int{}, G: []int{}, I: map[string]int{}},
		withX,
	}
	for i, c := range cases {
		got, gErr := Marshal(c)
		want, wErr := stdjson.Marshal(c)
		if (gErr != nil) != (wErr != nil) {
			t.Fatalf("case %d: err %v, stdlib %v", i, gErr, wErr)
		}
		if string(got) != string(want) {
			t.Errorf("case %d:\n got %s\nwant %s", i, got, want)
		}
	}
}
