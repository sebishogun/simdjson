package simdjson

import (
	"bytes"
	"fmt"
	"io"
	"math/rand"
	"strings"
	"testing"
)

// collectValues drains a stream through Value(), recording per delivery the
// kind, the raw extent, the input offset after it, and the error that ended
// the stream (with how many values came before it). One shape for both the
// staged and the serial run, so the comparison is on everything a caller can
// see.
func collectValues(t *testing.T, src []byte) (kinds []Kind, raws []string, offs []int64, n int, errText string) {
	t.Helper()
	d := NewDecoder(bytes.NewReader(src))
	for {
		v, err := d.Value()
		if err == io.EOF {
			return
		}
		if err != nil {
			errText = err.Error()
			return
		}
		kinds = append(kinds, v.Kind())
		raws = append(raws, string(v.Raw()))
		offs = append(offs, d.InputOffset())
		n++
	}
}

// forceStage arms the staging for every batch; forceSerial makes it
// unreachable. Both restore on cleanup.
func forceStage(t *testing.T) {
	t.Helper()
	ob, oe, os := streamStageMinBytes, streamStageMinElems, streamStageStreak
	streamStageMinBytes, streamStageMinElems, streamStageStreak = 1, 2, 0
	t.Cleanup(func() {
		streamStageMinBytes, streamStageMinElems, streamStageStreak = ob, oe, os
	})
}

func forceSerial(t *testing.T) {
	t.Helper()
	os := streamStageStreak
	streamStageStreak = 1 << 30
	t.Cleanup(func() { streamStageStreak = os })
}

// streamValueDifferential compares a staged Value loop against the serial one
// on the same bytes, across chunk sizes that force batch boundaries onto every
// landing.
func streamValueDifferential(t *testing.T, src []byte) {
	t.Helper()
	// The serial baseline runs at the same chunk size as the staged run:
	// error offsets are batch-relative, so the batch boundary is held fixed
	// and the staging is the only variable.
	for _, chunk := range []int{47, 256, 4096, 64 << 10} {
		old := streamChunk
		streamChunk = chunk
		var wantK []Kind
		var wantR []string
		var wantO []int64
		var wantN int
		var wantE string
		func() {
			forceSerial(t)
			wantK, wantR, wantO, wantN, wantE = collectValues(t, src)
		}()
		func() {
			forceStage(t)
			gotK, gotR, gotO, gotN, gotE := collectValues(t, src)
			if gotN != wantN || gotE != wantE {
				t.Fatalf("chunk %d: staged run: %d values, err %q; serial: %d, %q",
					chunk, gotN, gotE, wantN, wantE)
			}
			for i := range wantK {
				if gotK[i] != wantK[i] || gotR[i] != wantR[i] || gotO[i] != wantO[i] {
					t.Fatalf("chunk %d: value %d: staged (%v, %q, off %d) != serial (%v, %q, off %d)",
						chunk, i, gotK[i], gotR[i], gotO[i], wantK[i], wantR[i], wantO[i])
				}
			}
		}()
		streamChunk = old
	}
}

func TestStreamStageDifferential(t *testing.T) {
	rng := rand.New(rand.NewSource(41))
	var sb strings.Builder
	for i := 0; i < 4000; i++ {
		switch rng.Intn(4) {
		case 0:
			fmt.Fprintf(&sb, `{"id":%d,"name":"user \"%d\" é","tags":["a","b"],"n":%g}`,
				i, i, rng.Float64()*1e6)
		case 1:
			fmt.Fprintf(&sb, `[%d,null,true,{"k":"v\\n"}]`, i)
		case 2:
			fmt.Fprintf(&sb, `{"nested":{"deep":{"deeper":[%d,-0.5e-3]}},"s":"плюс"}`, i)
		case 3:
			fmt.Fprintf(&sb, `{}`)
		}
		sb.WriteString("\n")
	}
	streamValueDifferential(t, []byte(sb.String()))
}

func TestStreamStageInvalidRecord(t *testing.T) {
	// A record the index accepts and the walk rejects, mid-stream: the staged
	// run must deliver the same records before it and the same error text at
	// the same place. "01" inside an array frames and indexes; the validator
	// rejects the leading zero.
	var sb strings.Builder
	for i := 0; i < 300; i++ {
		fmt.Fprintf(&sb, `{"i":%d}`+"\n", i)
	}
	sb.WriteString("[01]\n")
	for i := 0; i < 300; i++ {
		fmt.Fprintf(&sb, `{"j":%d}`+"\n", i)
	}
	streamValueDifferential(t, []byte(sb.String()))
}

func TestStreamStageScalarsDecline(t *testing.T) {
	// Scalars at top level are not the record shape; the walk declines and
	// everything still agrees with serial.
	var sb strings.Builder
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&sb, "{\"i\":%d}\n%d\n\"s%d\"\ntrue\n", i, i, i)
	}
	streamValueDifferential(t, []byte(sb.String()))
}

func TestStreamStageDecodeInterleave(t *testing.T) {
	// A Decode in the middle of a Value loop moves the cursor without the
	// staging; the guard has to drop it rather than mis-deliver.
	forceStage(t)
	old := streamChunk
	streamChunk = 128
	defer func() { streamChunk = old }()

	var sb strings.Builder
	for i := 0; i < 500; i++ {
		fmt.Fprintf(&sb, `{"i":%d}`+"\n", i)
	}
	d := NewDecoder(strings.NewReader(sb.String()))
	for i := 0; i < 500; i++ {
		if i%7 == 3 {
			var m map[string]int
			if err := d.Decode(&m); err != nil {
				t.Fatalf("decode %d: %v", i, err)
			}
			if m["i"] != i {
				t.Fatalf("decode %d: got %v", i, m)
			}
			continue
		}
		v, err := d.Value()
		if err != nil {
			t.Fatalf("value %d: %v", i, err)
		}
		want := fmt.Sprintf(`{"i":%d}`, i)
		if string(v.Raw()) != want {
			t.Fatalf("value %d: %q != %q", i, v.Raw(), want)
		}
	}
	if _, err := d.Value(); err != io.EOF {
		t.Fatalf("tail: %v", err)
	}
}

func TestStreamStageTokenInterleave(t *testing.T) {
	// Token drops the batch and the staging; a Value loop resumed after a
	// tokenized array still delivers everything.
	forceStage(t)
	var sb strings.Builder
	for i := 0; i < 100; i++ {
		fmt.Fprintf(&sb, `{"i":%d}`+"\n", i)
	}
	sb.WriteString("[1,2,3]\n")
	for i := 0; i < 100; i++ {
		fmt.Fprintf(&sb, `{"j":%d}`+"\n", i)
	}
	d := NewDecoder(strings.NewReader(sb.String()))
	for i := 0; i < 100; i++ {
		if _, err := d.Value(); err != nil {
			t.Fatalf("front %d: %v", i, err)
		}
	}
	if tok, err := d.Token(); err != nil || tok != Delim('[') {
		t.Fatalf("open: %v %v", tok, err)
	}
	sum := 0
	for d.More() {
		var n int
		if err := d.Decode(&n); err != nil {
			t.Fatalf("elem: %v", err)
		}
		sum += n
	}
	if tok, err := d.Token(); err != nil || tok != Delim(']') {
		t.Fatalf("close: %v %v", tok, err)
	}
	if sum != 6 {
		t.Fatalf("sum %d", sum)
	}
	for i := 0; i < 100; i++ {
		v, err := d.Value()
		if err != nil {
			t.Fatalf("back %d: %v", i, err)
		}
		want := fmt.Sprintf(`{"j":%d}`, i)
		if string(v.Raw()) != want {
			t.Fatalf("back %d: %q", i, v.Raw())
		}
	}
	if _, err := d.Value(); err != io.EOF {
		t.Fatalf("tail: %v", err)
	}
}
