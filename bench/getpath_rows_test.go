package bench

// The get-a-few-fields rows on gjson's OWN published fixture and paths --
// the workload and payload its README's numbers rest on, rotated through the
// same three paths it rotates. Answers are cross-checked between libraries
// before timing.

import (
	"testing"

	"github.com/buger/jsonparser"
	ours "github.com/sebishogun/simdjson"
	"github.com/tidwall/gjson"
	"github.com/valyala/fastjson"
)

var widgetFixture = []byte(`{
  "widget": {
    "debug": "on",
    "window": {
      "title": "Sample Konfabulator Widget",
      "name": "main_window",
      "width": 500,
      "height": 500
    },
    "image": { 
      "src": "Images/Sun.png",
      "hOffset": 250,
      "vOffset": 250,
      "alignment": "center"
    },
    "text": {
      "data": "Click Here",
      "size": 36,
      "style": "bold",
      "vOffset": 100,
      "alignment": "center",
      "onMouseUp": "sun1.opacity = (sun1.opacity / 100) * 90;"
    }
  }
}`)

var widgetPaths = []string{"widget.window.name", "widget.image.hOffset", "widget.text.onMouseUp"}

func BenchmarkWidgetGet(b *testing.B) {
	// Agreement first, typed: gjson's String() coerces numbers, ours does
	// not, so numbers compare as floats.
	for _, p := range widgetPaths {
		g := gjson.GetBytes(widgetFixture, p)
		o := ours.GetPath(widgetFixture, p)
		if !o.Exists() {
			b.Fatalf("%s: ours missing", p)
		}
		if g.Type == gjson.Number {
			if o.Float() != g.Float() {
				b.Fatalf("%s: ours %v, gjson %v", p, o.Float(), g.Float())
			}
		} else if o.String() != g.String() {
			b.Fatalf("%s: ours %q, gjson %q", p, o.String(), g.String())
		}
	}
	b.Run("ours", func(b *testing.B) {
		i := 0
		for b.Loop() {
			v := ours.GetPath(widgetFixture, widgetPaths[i%3])
			i++
			if !v.Exists() {
				b.Fatal("missing")
			}
		}
	})
	b.Run("gjson", func(b *testing.B) {
		i := 0
		for b.Loop() {
			r := gjson.GetBytes(widgetFixture, widgetPaths[i%3])
			i++
			if !r.Exists() {
				b.Fatal("missing")
			}
		}
	})
	b.Run("jsonparser", func(b *testing.B) {
		keys := [][]string{
			{"widget", "window", "name"},
			{"widget", "image", "hOffset"},
			{"widget", "text", "onMouseUp"},
		}
		i := 0
		for b.Loop() {
			_, _, _, err := jsonparser.Get(widgetFixture, keys[i%3]...)
			i++
			if err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("fastjson", func(b *testing.B) {
		var p fastjson.Parser
		segs := [][]string{
			{"widget", "window", "name"},
			{"widget", "image", "hOffset"},
			{"widget", "text", "onMouseUp"},
		}
		i := 0
		for b.Loop() {
			v, err := p.ParseBytes(widgetFixture)
			if err != nil {
				b.Fatal(err)
			}
			if v.Get(segs[i%3]...) == nil {
				b.Fatal("missing")
			}
			i++
		}
	})
	b.Run("ours-second-query", func(b *testing.B) {
		// The index's half of the trade: parse once, then every further
		// path is a walk over positions. gjson re-scans per query.
		d, err := ours.Parse(widgetFixture)
		if err != nil {
			b.Fatal(err)
		}
		i := 0
		for b.Loop() {
			v := d.Path(widgetPaths[i%3])
			i++
			if !v.Exists() {
				b.Fatal("missing")
			}
		}
	})
	b.Run("ours-GetMany3", func(b *testing.B) {
		many := [][]string{
			{"widget", "window", "name"},
			{"widget", "image", "hOffset"},
			{"widget", "text", "onMouseUp"},
		}
		for b.Loop() {
			vs := ours.GetMany(widgetFixture, many...)
			if len(vs) != 3 {
				b.Fatal("missing")
			}
		}
	})
}
