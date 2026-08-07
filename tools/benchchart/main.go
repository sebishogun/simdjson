// Command benchchart renders the benchmark snapshot written by benchrunner
// into SVG charts: for each operation family, one ratio chart (every
// library's time as a multiple of this library's, reference line at 1.0) and
// one raw throughput chart in MB/s.
//
// The transparency rule: every library that was measured appears, including
// the rows where another library wins. The ratios are linear, not log —
// grouped bars on a log axis would have no baseline; the table the charts sit
// beside carries the exact numbers.
//
//	go run ./benchchart -in ../docs/bench/compare-2026-08-07.json -out ../docs/figures
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"image/color"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gonum.org/v1/plot"
	"gonum.org/v1/plot/plotter"
	"gonum.org/v1/plot/vg"
)

// run mirrors the benchrunner JSON.
type run struct {
	Machine   string  `json:"machine"`
	GoVersion string  `json:"goVersion"`
	Tier      string  `json:"tier"`
	Date      string  `json:"date"`
	Benches   []bench `json:"benches"`
}

type bench struct {
	Name  string  `json:"name"`
	NsMin float64 `json:"nsMin"`
	Mbps  float64 `json:"mbps,omitempty"`
}

// cell is one library's result on one corpus row.
type cell struct{ ns, mbps float64 }

// chartedFamilies in display order. Everything else classified lands in
// "other" and is carried in the JSON but not drawn.
var chartedFamilies = []string{"parse", "validate", "unmarshal", "marshal", "streaming"}

var familyTitles = map[string]string{
	"parse":     "Parse — time relative to this library",
	"validate":  "Validate — time relative to this library",
	"unmarshal": "Unmarshal into struct — time relative to this library",
	"marshal":   "Marshal — time relative to this library",
	"streaming": "Streaming — time relative to this library",
}

var palette = []color.RGBA{
	{R: 0x1f, G: 0x77, B: 0xb4, A: 0xff}, // this
	{R: 0xff, G: 0x7f, B: 0x0e, A: 0xff},
	{R: 0x2c, G: 0xa0, B: 0x2c, A: 0xff},
	{R: 0xd6, G: 0x27, B: 0x28, A: 0xff},
	{R: 0x94, G: 0x67, B: 0xbd, A: 0xff},
	{R: 0x8c, G: 0x56, B: 0x4b, A: 0xff},
	{R: 0xe3, G: 0x77, B: 0xc2, A: 0xff},
	{R: 0x7f, G: 0x7f, B: 0x7f, A: 0xff},
	{R: 0xbc, G: 0xbd, B: 0x22, A: 0xff},
	{R: 0x17, G: 0xbe, B: 0xcf, A: 0xff},
}

func main() {
	in := flag.String("in", "", "benchrunner JSON snapshot")
	out := flag.String("out", "docs/figures", "output directory for SVGs")
	flag.Parse()
	if *in == "" {
		log.Fatal("usage: benchchart -in snapshot.json -out dir")
	}
	data, err := os.ReadFile(*in)
	if err != nil {
		log.Fatal(err)
	}
	var r run
	if err := json.Unmarshal(data, &r); err != nil {
		log.Fatalf("%s: %v", *in, err)
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		log.Fatal(err)
	}

	// rows[family][corpusKey][library] = ns (min) and mbps of that library.
	rows := map[string]map[string]map[string]cell{}
	for _, b := range r.Benches {
		c := classify(b.Name)
		fm := rows[c.Family]
		if fm == nil {
			fm = map[string]map[string]cell{}
			rows[c.Family] = fm
		}
		cm := fm[c.Corpus]
		if cm == nil {
			cm = map[string]cell{}
			fm[c.Corpus] = cm
		}
		cm[c.Library] = cell{ns: b.NsMin, mbps: b.Mbps}
	}

	footnote := fmt.Sprintf("%s, %s, tier %s, %s", r.Machine, r.GoVersion, r.Tier, r.Date)
	for _, fam := range chartedFamilies {
		fm := rows[fam]
		if len(fm) == 0 {
			continue
		}
		if err := renderRatio(fam, fm, footnote, *out); err != nil {
			log.Fatalf("%s ratio: %v", fam, err)
		}
		if err := renderThroughput(fam, fm, footnote, *out); err != nil {
			log.Fatalf("%s throughput: %v", fam, err)
		}
	}
	fmt.Printf("wrote %d families to %s\n", len(rows), *out)
}

// renderRatio draws, for one family, each library's time as a multiple of
// this library's, per corpus. A corpus appears only where this library was
// measured on it. Bars below the 1.0 line are rows another library wins.
func renderRatio(fam string, fm map[string]map[string]cell, footnote, out string) error {
	// Baselines: ns of "this" per corpus.
	thisNs := map[string]float64{}
	for corpus, cm := range fm {
		if t, ok := cm["this"]; ok {
			thisNs[corpus] = t.ns
		}
	}
	if len(thisNs) == 0 {
		return nil
	}
	corpora := sortedKeys(thisNs)

	libs := map[string]bool{}
	for _, cm := range fm {
		for lib := range cm {
			libs[lib] = true
		}
	}
	libOrder := []string{"this"}
	for lib := range libs {
		if lib != "this" {
			libOrder = append(libOrder, lib)
		}
	}
	sort.Strings(libOrder[1:])

	p := plot.New()
	p.Title.Text = familyTitles[fam]
	p.X.Label.Text = "library"
	p.Y.Label.Text = "time ÷ this library's time (1.0 = level; lower is faster)"
	p.X.Tick.Label.Rotation = 0.4
	p.X.Tick.Label.Font.Size = 9
	p.Legend.Top = true
	p.Legend.Left = true

	width := 0.22 * vg.Centimeter
	step := float64(len(corpora))
	for i, corpus := range corpora {
		var vals plotter.Values
		for _, lib := range libOrder {
			var v float64
			if c, ok := fm[corpus][lib]; ok {
				v = c.ns / thisNs[corpus]
			} else {
				v = 0 // absent: no bar
			}
			vals = append(vals, v)
		}
		bar, err := plotter.NewBarChart(vals, width)
		if err != nil {
			return err
		}
		bar.Offset = width*vg.Length(i) - width*vg.Length(step)/2 + width/2
		bar.Color = palette[i%len(palette)]
		bar.LineStyle.Width = 0
		p.Add(bar)
		p.Legend.Add(corpus, bar)
	}

	// The 1.0 reference line.
	line, err := plotter.NewLine(plotter.XYs{{X: 0, Y: 1}, {X: float64(len(libOrder)), Y: 1}})
	if err != nil {
		return err
	}
	line.LineStyle.Dashes = []vg.Length{2 * vg.Millimeter, 2 * vg.Millimeter}
	p.Add(line)

	p.NominalX(libOrder...)
	maxRatio := 0.0
	for _, cm := range fm {
		for lib, c := range cm {
			if lib == "this" {
				continue
			}
			if base, ok := thisNs[corpusOf(fm, lib)]; ok && c.ns/base > maxRatio {
				maxRatio = c.ns / base
			}
		}
	}
	p.Y.Max = maxRatio * 1.15
	p.Title.Text += " (1.0 = this library; " + footnote + ")"

	return p.Save(8*72, 5*72, filepath.Join(out, fam+"-ratio.svg"))
}

func corpusOf(fm map[string]map[string]cell, lib string) string {
	for corpus, cm := range fm {
		if _, ok := cm[lib]; ok {
			return corpus
		}
	}
	return ""
}

// renderThroughput draws each library's MB/s per corpus, only for rows the
// harness set bytes on.
func renderThroughput(fam string, fm map[string]map[string]cell, footnote, out string) error {
	corpora := sortedKeys(fm)
	libs := map[string]bool{}
	for _, cm := range fm {
		for lib := range cm {
			libs[lib] = true
		}
	}
	libOrder := []string{"this"}
	for lib := range libs {
		if lib != "this" {
			libOrder = append(libOrder, lib)
		}
	}
	sort.Strings(libOrder[1:])

	p := plot.New()
	p.Title.Text = strings.Title(fam) + " — throughput, MB/s"
	p.X.Label.Text = "library"
	p.Y.Label.Text = "MB/s (higher is faster)"
	p.X.Tick.Label.Rotation = 0.4
	p.X.Tick.Label.Font.Size = 9
	p.Legend.Top = true
	p.Legend.Left = true

	width := 0.22 * vg.Centimeter
	step := float64(len(corpora))
	for i, corpus := range corpora {
		var vals plotter.Values
		for _, lib := range libOrder {
			var v float64
			if c, ok := fm[corpus][lib]; ok {
				v = c.mbps
			}
			vals = append(vals, v)
		}
		bar, err := plotter.NewBarChart(vals, width)
		if err != nil {
			return err
		}
		bar.Offset = width*vg.Length(i) - width*vg.Length(step)/2 + width/2
		bar.Color = palette[i%len(palette)]
		bar.LineStyle.Width = 0
		p.Add(bar)
		p.Legend.Add(corpus, bar)
	}
	p.NominalX(libOrder...)
	p.Title.Text += " (" + footnote + ")"

	return p.Save(8*72, 5*72, filepath.Join(out, fam+"-throughput.svg"))
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
