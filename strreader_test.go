package simdjson

import "strings"

func newStringReader(s string) *strings.Reader { return strings.NewReader(s) }
