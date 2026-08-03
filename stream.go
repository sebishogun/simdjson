package simdjson

// Reading and writing JSON over a stream: one value at a time out of an
// io.Reader, one value at a time into an io.Writer.
//
// This is the shape most JSON in a service actually arrives in — a request
// body, a log file, a line-delimited export — and it is the one case the
// whole-document design here does not cover on its own, because a whole
// document is exactly what a stream does not give you.
//
// The problem is finding where one value ends without parsing it, since the
// bytes after it may not have arrived. That is a different question from the
// one the structural index answers, and it is answered here by a scan that
// looks only at quotes, backslashes and brackets — vectorised, because those
// are sparse and simd.IndexAny jumps between them rather than walking.

import (
	"bytes"
	"encoding/json"
	"io"
	"reflect"
	"unsafe"

	"github.com/sebishogun/simd"
)

// A Decoder reads JSON values from a stream, one call to Decode per value.
//
// Values may be separated by whitespace or by nothing at all; newline-delimited
// JSON is the case where they are separated by exactly one newline, and needs
// no special handling here.
type Decoder struct {
	r   io.Reader
	buf []byte // unconsumed input, buf[off:] is what is left
	off int
	// consumed counts the bytes handed back to the caller before buf[0], so
	// InputOffset can report a position in the stream rather than in the
	// buffer.
	consumed int64
	err      error // the reader's error, once it has one

	// One index over as many whole values as the buffer holds, rather than one
	// per value.
	//
	// Building it is five vector passes and a pool fetch, which is the right
	// price for a megabyte and an absurd one for the hundred-byte record a
	// line-delimited stream is made of — per value it measured four times
	// slower than goccy. Per buffer it is amortised over every value in the
	// buffer, which is where this design was always going to win: a structural
	// index costs the same whether one value reads it or six hundred do.
	doc  *Doc
	data []byte // the slice doc indexes, which is buf[base:limit]
	base int
	cur  int // read position within data
	ix   *index

	// single drops back to indexing one value at a time, after a chunk turned
	// out to hold something the index rejects.
	single bool

	useNumber       bool
	disallowUnknown bool

	// Token's position in the syntax, which Decode has to respect: after Token
	// has returned an opening bracket, the next Decode reads an element of that
	// container rather than a top-level value, and there is a comma in front of
	// it after the first one.
	tstack   []byte // open brackets, as the characters themselves
	inItem   bool   // the enclosing container already holds something
	wantKey  bool   // that container is an object and the next item is a key
	afterKey bool   // the last token was a key, so a colon comes before its value

	// The decoder for the type last decoded into. decoderFor is a sync.Map
	// keyed by reflect.Type; consulting it per value was 15% of a stream.
	fnTyp reflect.Type
	fn    decodeFn

	// vstack holds the open brackets the framing scan has seen, so a closer can
	// be checked against its opener. Reused across calls; a stack allocated per
	// value would cost more than the scan.
	vstack []byte
}

// NewDecoder returns a Decoder reading from r.
//
// It may read more from r than it needs to answer a call to Decode; whatever it
// has read and not used is available from [Decoder.Buffered].
func NewDecoder(r io.Reader) *Decoder {
	return &Decoder{r: r}
}

// UseNumber makes Decode store a number in an any as a [Number] -- the digits
// as they were written -- rather than a float64, which cannot hold all of them.
func (d *Decoder) UseNumber() { d.useNumber = true }

// DisallowUnknownFields makes Decode report an error when the input names a
// field the destination struct does not.
func (d *Decoder) DisallowUnknownFields() { d.disallowUnknown = true }

// InputOffset returns the position in the stream just after the most recently
// decoded value.
func (d *Decoder) InputOffset() int64 { return d.consumed + int64(d.off) }

// Buffered returns a reader over the bytes read from the underlying reader and
// not yet consumed by Decode.
func (d *Decoder) Buffered() io.Reader { return bytes.NewReader(d.buf[d.off:]) }

// decodeAt is Doc.decodeAt with the type lookup cached across calls, which for
// a stream of one shape means done once.
func (d *Decoder) decodeAt(i int, out any) (int, error) {
	rv := reflect.ValueOf(out)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return 0, &json.InvalidUnmarshalError{Type: reflect.TypeOf(out)}
	}
	if el := rv.Elem(); el.CanAddr() {
		if t := el.Type(); d.fnTyp != t {
			d.fn, d.fnTyp = decoderFor(t), t
		}
		return d.fn(unsafe.Pointer(el.UnsafeAddr()), d.doc, i)
	}
	return d.doc.decodeAt(i, out)
}

// More reports whether there is another element in the array or object being
// read, or another value in the stream.
func (d *Decoder) More() bool {
	c, err := d.peek()
	return err == nil && c != ']' && c != '}'
}

// Token is not implemented. The rest of the Decoder is; Token is a different
// shape of API — a cursor over the syntax rather than over the values — and
// half of it would be worse than none.
//
// Decoding into a [encoding/json.RawMessage], or [Parse] and a walk over the
// [Value], covers what it is usually reached for.

// peek returns the next non-whitespace byte without consuming it, refilling as
// it has to.
func (d *Decoder) peek() (byte, error) {
	for {
		for ; d.off < len(d.buf); d.off++ {
			if !isJSONSpace[d.buf[d.off]] {
				return d.buf[d.off], nil
			}
		}
		if err := d.fill(); err != nil {
			return 0, err
		}
	}
}

// Decode reads the next JSON value from the stream and stores it in v.
//
// It returns [io.EOF] when the stream holds no further value, which is what
// ends a read loop.
func (d *Decoder) Decode(out any) error {
	// Inside a container, an element after the first has a comma in front of
	// it. Token consumes its own separators; Decode has to consume this one,
	// because the framing scan that follows only knows how to start at a value.
	if len(d.tstack) > 0 && d.inItem {
		// Stepped over inside the batch when there is one. Consuming it through
		// the buffer would mean dropping the index, and dropping the index once
		// per element is how a batch becomes no batch at all: an array of
		// thirteen million small elements went from 4.4 seconds to 507.
		if d.doc != nil {
			i := d.doc.skip(d.cur)
			if i < len(d.data) && d.data[i] == ',' {
				d.cur = i + 1
				d.off = d.base + d.cur
			} else {
				d.doc, d.data = nil, nil
			}
		}
		if d.doc == nil {
			c, err := d.peek()
			if err != nil {
				return err
			}
			if c == ',' {
				d.off++
			}
		}
	}
	for {
		if d.doc != nil {
			i := d.doc.skip(d.cur)
			if i < len(d.data) {
				next, err := d.decodeAt(i, out)
				if next > d.cur {
					d.cur, d.off = next, d.base+next
				}
				if err == nil && len(d.tstack) > 0 {
					// The index stays. Token clears it when it runs, and Decode
					// wants it for the next element of the batch.
					d.afterItem()
				}
				return err
			}
			d.doc, d.data = nil, nil
		}
		if err := d.load(); err != nil {
			return err
		}
	}
}

// load indexes as many whole values as the buffer holds, reading more if it
// holds none.
//
// The extent has to be found before the index is built, because an index over a
// value cut in half is an error rather than a partial answer — the scan would
// report an unterminated string. Finding it is a separate pass, and a cheap one:
// it looks only at quotes, backslashes and brackets, and jumps between them.
func (d *Decoder) load() error {
	for {
		if _, err := d.peek(); err != nil {
			return err
		}
		// One pass, not two. The index is built over the whole buffer in
		// partial mode, which does not treat a value cut in half as an error:
		// it indexes what is there and says how far that goes. safeEnd is one
		// past the last top-level container that closed.
		//
		// What this replaces is a scalar scan over the same bytes to find that
		// point before the vector index was allowed to read them -- 17% of a
		// decode, spent deciding where the vector pass was permitted to start.
		if len(d.tstack) > 0 {
			return d.loadBatch()
		}
		if !d.single && d.off < len(d.buf) && canStartValue[d.buf[d.off]] {
			ix, err := buildIndexMode(d.buf[d.off:], d.ix, true, false, true)
			d.ix = ix
			if err == nil && ix.safeEnd > 0 {
				if ix.partErr != nil && ix.partErrAt < ix.safeEnd {
					return ix.partErr
				}
				if doc, err := scanRoot(d.buf[d.off:d.off+ix.safeEnd], ix); err == nil {
					doc.strictSkip = true
					doc.useNumber, doc.disallowUnknown = d.useNumber, d.disallowUnknown
					d.doc, d.data = doc, d.buf[d.off:d.off+ix.safeEnd]
					d.base, d.cur = d.off, 0
					return nil
				}
			}
		}

		limit, err := d.completeThrough()
		if err != nil {
			return err
		}
		if limit > d.off {
			// The framing scan finds where values end. It does not know
			// whether they are valid -- a string holding a control byte frames
			// perfectly and the index rejects it -- and a chunk that fails to
			// index takes every value in it down, including the good ones
			// before the bad one.
			//
			// So the first failure drops to one value per index for the rest of
			// this buffer. Each good value is then handed over on its own and
			// the error arrives at the value that actually causes it, which is
			// where encoding/json puts it too. It costs the amortisation, and
			// it costs it only on input that is about to stop the stream.
			if d.single {
				if e, ok := d.valueEnd(d.buf, d.off); ok && e < limit {
					limit = e
				}
			}
			d.ix, err = buildIndex(d.buf[d.off:limit], d.ix, true)
			if err != nil {
				if !d.single {
					d.single = true
					continue
				}
				return err
			}
			doc, err := scanRoot(d.buf[d.off:limit], d.ix)
			if err != nil {
				return err
			}
			doc.strictSkip = true
			doc.useNumber, doc.disallowUnknown = d.useNumber, d.disallowUnknown
			d.doc, d.data, d.base, d.cur = doc, d.buf[d.off:limit], d.off, 0
			return nil
		}
		// Nothing whole in the buffer. If the next byte cannot begin a value at
		// all, no amount of reading will help.
		if c, err := d.peek(); err == nil && !canStartValue[c] {
			return errAt("unexpected character", d.off)
		}
		// If the stream is already known to be over, that is all there will
		// ever be and what is left is a value cut short. Otherwise read more and ask again — including when that read
		// is the one that reports EOF, because a bare number at the very end of
		// a stream only becomes complete once there is known to be nothing
		// after it.
		if d.err != nil {
			if d.err == io.EOF {
				return io.ErrUnexpectedEOF
			}
			return d.err
		}
		if err := d.fill(); err != nil && err != io.EOF {
			return err
		}
	}
}

// loadBatch indexes as many whole elements of the open container as the buffer
// holds, rather than one per call.
//
// This is what C++ simdjson's parse_many does and for the same reason: stage
// one is a fixed cost per call and amortising it over a batch is most of what
// makes streaming fast. Indexing one element at a time ran the 10 GB array at
// 705 MB/s against the 956 the line-delimited path gets, on the same bytes —
// the difference was entirely the batching.
//
// The batch stops at the container's closing bracket, which the top-level
// framing has no reason to look for and would run straight past.
func (d *Decoder) loadBatch() error {
	for {
		if _, err := d.peek(); err != nil {
			return err
		}
		limit, n := d.elementsThrough()
		if n > 0 {
			ix, err := buildIndexMode(d.buf[d.off:limit], d.ix, true, false, true)
			d.ix = ix
			if err == nil && ix.safeEnd > 0 {
				if ix.partErr != nil && ix.partErrAt < ix.safeEnd {
					return ix.partErr
				}
				end := d.off + ix.safeEnd
				doc, err := scanRoot(d.buf[d.off:end], ix)
				if err == nil {
					doc.strictSkip = true
					doc.useNumber, doc.disallowUnknown = d.useNumber, d.disallowUnknown
					d.doc, d.data = doc, d.buf[d.off:end]
					d.base, d.cur = d.off, 0
					return nil
				}
			}
			// The batch would not index. One element on its own still might,
			// and if it does not the error belongs to that element.
			return d.loadOne()
		}
		if d.err != nil {
			return d.loadOne()
		}
		if err := d.fill(); err != nil && err != io.EOF {
			return err
		}
	}
}

// elementsThrough returns the end of the last whole element of the open
// container that the buffer holds, and how many there are. It stops at the
// closing bracket rather than running past it.
func (d *Decoder) elementsThrough() (int, int) {
	i, limit, n := d.off, d.off, 0
	for {
		for i < len(d.buf) && isJSONSpace[d.buf[i]] {
			i++
		}
		if i >= len(d.buf) || d.buf[i] == '}' || d.buf[i] == ']' {
			return limit, n
		}
		if n > 0 {
			if d.buf[i] != ',' {
				return limit, n
			}
			i++
			for i < len(d.buf) && isJSONSpace[d.buf[i]] {
				i++
			}
			if i >= len(d.buf) {
				return limit, n
			}
		}
		if !canStartValue[d.buf[i]] {
			return limit, n
		}
		end, ok := d.valueEnd(d.buf, i)
		if !ok {
			return limit, n
		}
		i, limit, n = end, end, n+1
		// Batched by bytes, not by count, which is what parse_many does. An
		// element of a megabyte fills a batch by itself and a hundred-byte
		// record shares one with six hundred others; batching by count instead
		// made the 10 GB array slower, because it meant indexing several
		// megabytes at a time to save a fixed cost measured in microseconds.
		if limit-d.off >= streamChunk {
			return limit, n
		}
	}
}

// loadOne indexes exactly one value, for a Decode reading an element the batch
// could not take: the last one before a refill, or one that will not index.
func (d *Decoder) loadOne() error {
	for {
		if _, err := d.peek(); err != nil {
			return err
		}
		end, ok := d.valueEnd(d.buf, d.off)
		if ok {
			ix, err := buildIndexMode(d.buf[d.off:end], d.ix, true, false, false)
			d.ix = ix
			if err != nil {
				return err
			}
			doc, err := scanRoot(d.buf[d.off:end], ix)
			if err != nil {
				return err
			}
			doc.strictSkip = true
			doc.useNumber, doc.disallowUnknown = d.useNumber, d.disallowUnknown
			d.doc, d.data = doc, d.buf[d.off:end]
			d.base, d.cur = d.off, 0
			return nil
		}
		if d.err != nil {
			if d.err == io.EOF && end > d.off && d.buf[d.off] != '"' &&
				d.buf[d.off] != '{' && d.buf[d.off] != '[' {
				continue
			}
			if d.err == io.EOF {
				return io.ErrUnexpectedEOF
			}
			return d.err
		}
		if err := d.fill(); err != nil && err != io.EOF {
			return err
		}
	}
}

// completeThrough returns the end of the last value in the buffer that is
// entirely present, and the index is built over exactly that much.
//
// Two things must stop it early, and both were found by fuzzing rather than by
// reasoning, because both only bite when a good value is followed by a bad one.
//
// A byte that cannot begin a value stops it. "00]" is two zeroes and then a
// syntax error; running the chunk to the end of the buffer meant indexing the
// bracket too, which failed as unbalanced, and the failure took the two values
// with it. encoding/json reports the same error but only after handing back
// both.
//
// A truncated object, array or string stops it as well. At end of stream a bare
// number that reaches the end of the buffer is complete — there is nothing left
// that could extend it — but an unclosed brace is not, and treating it the same
// way had the same effect: one malformed value at the end of a stream discarded
// every good one before it.
func (d *Decoder) completeThrough() (int, error) {
	i, limit := d.off, d.off
	for {
		for i < len(d.buf) && isJSONSpace[d.buf[i]] {
			i++
		}
		if i >= len(d.buf) || !canStartValue[d.buf[i]] {
			return limit, nil
		}
		end, ok := d.valueEnd(d.buf, i)
		if !ok {
			c := d.buf[i]
			if c == '{' || c == '[' || c == '"' {
				return limit, nil // cut short, however the stream ends
			}
			if d.err == io.EOF && end > i {
				return end, nil
			}
			if d.err != nil && d.err != io.EOF {
				return 0, d.err
			}
			return limit, nil
		}
		i, limit = end, end
	}
}

// canStartValue marks the bytes a JSON value may begin with.
var canStartValue = func() (t [256]bool) {
	for _, c := range []byte(`{["tfn-0123456789`) {
		t[c] = true
	}
	return
}()

// fill reads more input, sliding the unconsumed bytes to the front first so the
// buffer does not grow without bound on a long stream.
func (d *Decoder) fill() error {
	if d.err != nil {
		return d.err
	}
	if d.off > 0 && d.off == len(d.buf) {
		d.consumed += int64(d.off)
		d.buf, d.off = d.buf[:0], 0
	} else if d.off > 4096 && d.off > len(d.buf)/2 {
		d.consumed += int64(d.off)
		n := copy(d.buf, d.buf[d.off:])
		d.buf, d.off = d.buf[:n], 0
	}
	// One read of a whole chunk rather than a byte at a time, and the chunk is
	// the same size the windowed index uses — big enough that the syscall is
	// amortised, small enough to stay in cache while it is scanned.
	if cap(d.buf)-len(d.buf) < streamChunk {
		nb := make([]byte, len(d.buf), max(2*cap(d.buf), len(d.buf)+streamChunk))
		copy(nb, d.buf)
		d.buf = nb
	}
	n, err := d.r.Read(d.buf[len(d.buf):cap(d.buf)])
	d.buf = d.buf[:len(d.buf)+n]
	if n > 0 {
		d.single = false
	}
	if err != nil {
		d.err = err
		if n > 0 && err == io.EOF {
			return nil
		}
		return err
	}
	return nil
}

const streamChunk = 64 << 10

// valueEnd returns one past the end of the JSON value starting at i, and
// whether the buffer held all of it.
//
// It does not validate. Deciding where a value ends needs only the quotes, the
// backslashes and the brackets, and everything else can be jumped over — which
// is what makes this cheap enough to run before the real parse rather than
// instead of it. Unmarshal validates what this hands it.
//
// When the value is a number or a literal and the buffer ends before any
// delimiter does, it returns the end of the buffer and false: the caller has to
// decide, because whether that is a complete value depends on whether the
// stream is over.
func (d *Decoder) valueEnd(b []byte, i int) (int, bool) {
	if i >= len(b) {
		return i, false
	}
	switch b[i] {
	case '{', '[':
		st := d.vstack[:0]
		for i < len(b) {
			// The hop is written out rather than called, because it is short
			// and there are a dozen of them per record: a structured document
			// has a quote or a bracket every few bytes. Only when the scalar
			// run comes up empty -- a long run of numbers, which has nothing in
			// this set until the array closes -- is the call worth making.
			n := i + shortHop
			if n > len(b) {
				n = len(b)
			}
			for i < n && !frameSet[b[i]] {
				i++
			}
			if i == n && n < len(b) {
				k := simd.IndexAny(b[i:], `"{}[]`)
				if k < 0 {
					d.vstack = st
					return len(b), false
				}
				i += k
			} else if i >= len(b) {
				d.vstack = st
				return len(b), false
			}
			switch c := b[i]; c {
			case '"':
				e, ok := plainStringEnd(b, i)
				if !ok {
					d.vstack = st
					return len(b), false
				}
				i = e
			case '{', '[':
				st = append(st, c)
				i++
			default: // } or ]
				// The closer has to match. Counting depth alone called "{]"
				// complete, and the index then rejected it as mismatched --
				// which took the whole chunk down, including the values before
				// it. Reported as not-complete, it is left to be met on its own.
				want := byte('}')
				if c == ']' {
					want = '['
				} else {
					want = '{'
				}
				if len(st) == 0 || st[len(st)-1] != want {
					d.vstack = st
					return i, false
				}
				st = st[:len(st)-1]
				i++
				if len(st) == 0 {
					d.vstack = st
					return i, true
				}
			}
		}
		d.vstack = st
		return len(b), false
	case '"':
		return plainStringEnd(b, i)
	default:
		return scalarEnd(b, i)
	}
}

// scalarEnd returns one past the number or literal starting at i.
//
// It has to know the number grammar, not just where the next delimiter is.
// "0000" is four values, because a JSON number may not have a leading zero, so
// the first 0 is complete and the next one begins another value — and running
// to the next delimiter would have made it one value and rejected it. There is
// no delimiter between two values here, only the point where one stops being
// grammatical.
//
// Reaching the end of the buffer at any point means the answer is not known
// yet: "1" may be all of the number or the front of "1.5". The caller resolves
// that by whether the stream is over.
func scalarEnd(b []byte, i int) (int, bool) {
	switch b[i] {
	case 't':
		return literalEnd(b, i, 4)
	case 'f':
		return literalEnd(b, i, 5)
	case 'n':
		return literalEnd(b, i, 4)
	}
	j := i
	if b[j] == '-' {
		j++
	}
	if j >= len(b) {
		return len(b), false
	}
	switch {
	case b[j] == '0':
		j++ // and no more digits may follow, which is the whole point
	case b[j] >= '1' && b[j] <= '9':
		for j++; j < len(b) && isDigit(b[j]); j++ {
		}
	default:
		// Not a number after all. The value ends here rather than swallowing the
		// byte that spoiled it, because that byte may be the start of something
		// the index cannot survive: "0-\"" ended the chunk after the quote,
		// and indexing an unterminated string failed and took the 0 before it
		// down as well. Ending short leaves the bad byte to be met on its own,
		// which is where it produces the right error and nothing else.
		if j == i {
			j++ // never return an empty value; the caller would not advance
		}
		return j, true
	}
	if j >= len(b) {
		return len(b), false
	}
	if b[j] == '.' {
		for j++; j < len(b) && isDigit(b[j]); j++ {
		}
		if j >= len(b) {
			return len(b), false
		}
	}
	if b[j] == 'e' || b[j] == 'E' {
		j++
		if j < len(b) && (b[j] == '+' || b[j] == '-') {
			j++
		}
		for ; j < len(b) && isDigit(b[j]); j++ {
		}
		if j >= len(b) {
			return len(b), false
		}
	}
	return j, true
}

// literalEnd returns one past a true, false or null. The bytes are not checked
// against the word they should spell — Unmarshal does that, and does it with a
// better error than "not a value".
func literalEnd(b []byte, i, n int) (int, bool) {
	if i+n > len(b) {
		return len(b), false
	}
	return i + n, true
}

// plainStringEnd returns one past the closing quote of the string whose opening
// quote is at i, without an index to consult.
//
// A backslash escapes exactly one byte, whatever it is, so the end of a string
// can be found without knowing what the escapes mean — jump to the next quote
// or backslash, and on a backslash step over two bytes.
func plainStringEnd(b []byte, i int) (int, bool) {
	for i++; ; {
		j := indexAnyFrom(b, i, &stringSet, `"\`)
		if j < 0 {
			return len(b), false
		}
		if b[j] == '"' {
			return j + 1, true
		}
		i = j + 2
		if i > len(b) {
			return len(b), false
		}
	}
}

// indexAnyFrom finds the next byte of b at or after i that is in set.
//
// The hops here are short -- a record in a line-delimited stream is a hundred
// bytes and has a quote or a bracket every dozen -- and the kernel is a call
// whose cost does not shrink with the distance it covers. So a bounded scalar
// run goes first and the kernel picks up only when that run finds nothing,
// which is the case where the distance is long enough to pay for it: a long
// string, or a long run of numbers.
//
// The scan is over the rest of the buffer, not the rest of the value, so the
// kernel's own length guard cannot make this decision -- it sees 64 KiB left
// and takes the vector path to find something three bytes away.
func indexAnyFrom(b []byte, i int, set *[256]bool, chars string) int {
	n := i + shortHop
	if n > len(b) {
		n = len(b)
	}
	for ; i < n; i++ {
		if set[b[i]] {
			return i
		}
	}
	if i >= len(b) {
		return -1
	}
	j := simd.IndexAny(b[i:], chars)
	if j < 0 {
		return -1
	}
	return i + j
}

// shortHop is how far the scalar run goes before the kernel is worth a call.
//
// Measured both ways on both shapes of stream, since the answer differs by
// shape and picking the one that suits the corpus at hand is how a library ends
// up fast on a benchmark and slow in use. Records of a hundred bytes, where a
// long hop never happens, prefer the scalar run by 1.3%. Records built around
// one long string field prefer the kernel by a lot:
//
//	field bytes   scalar only   with kernel
//	         64      11.2 ms       10.8 ms
//	        512       5.43          3.75    1.44x
//	       4096       4.48          2.66    1.68x
//
// So both, in that order. One 64-byte block plus a little is where the vector
// path gets ahead counting the call.
const shortHop = 80

// frameSet and stringSet are the two alphabets the framing scan cares about:
// what changes the state outside a string, and what ends one.
var frameSet = func() (t [256]bool) {
	for _, c := range []byte(`"{}[]`) {
		t[c] = true
	}
	return
}()

var stringSet = func() (t [256]bool) {
	t['"'] = true
	t['\\'] = true
	return
}()

// An Encoder writes JSON values to a stream, one call to Encode per value, each
// followed by a newline.
type Encoder struct {
	w    io.Writer
	opts Options
	err  error

	prefix, indent string
	ibuf           bytes.Buffer

	// The Encoder keeps its own encoder rather than borrowing one per call.
	// An Encoder is a stateful object owned by one goroutine for its whole
	// life, which is exactly what a pool is a substitute for when you do not
	// have one; sync.Pool.Get and Put were 2.4% of a stream.
	es encodeState
}

// NewEncoder returns an Encoder writing to w, matching encoding/json's defaults:
// HTML characters escaped, strings checked for valid UTF-8.
func NewEncoder(w io.Writer) *Encoder {
	return &Encoder{w: w, opts: Std}
}

// SetEscapeHTML controls whether <, > and & are escaped. It is on by default.
func (e *Encoder) SetEscapeHTML(on bool) { e.opts.EscapeHTML = on }

// SetIndent makes Encode write each value the way [Indent] would. An empty
// indent turns it off.
func (e *Encoder) SetIndent(prefix, indent string) {
	e.prefix, e.indent = prefix, indent
}

// Options sets the whole option set at once, which is how the non-validating
// mode is reached from a stream. See [Options].
func (e *Encoder) Options(o Options) { e.opts = o }

// Encode writes the JSON encoding of v to the stream, followed by a newline.
func (e *Encoder) Encode(v any) error {
	if e.err != nil {
		return e.err
	}
	// Straight out of the encoder's own buffer rather than through MarshalTo,
	// which encodes into a pooled buffer and then copies the result into the
	// caller's. For a stream that copy is the whole record, once per record,
	// to no purpose: what the buffer is for is being written.
	es := &e.es
	es.buf, es.opts = es.buf[:0], e.opts
	if err := es.marshal(v); err != nil {
		return err
	}
	b := es.buf
	if e.indent != "" || e.prefix != "" {
		e.ibuf.Reset()
		if err := Indent(&e.ibuf, b, e.prefix, e.indent); err != nil {
			return err
		}
		e.ibuf.WriteByte('\n')
		b = e.ibuf.Bytes()
	} else {
		// The newline is part of the contract: a stream of values written by
		// an Encoder is newline-delimited JSON, and something is reading it
		// that way. Appending it here rather than in a second Write keeps the
		// record one call, and the grown buffer goes back to the pool.
		b = append(b, '\n')
		es.buf = b
	}
	_, err := e.w.Write(b)
	if err != nil {
		e.err = err
	}
	return err
}
