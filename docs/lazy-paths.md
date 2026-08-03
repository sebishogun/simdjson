# Lazy path scanning: what it would take, and what it would be worth

A prototype question: can the structural index be built lazily, so that pulling
one field out of a large document costs only the part of the document before
that field? This is what [gjson](https://github.com/tidwall/gjson) does by a
different route — it scans bytes, keeps nothing, and stops when it finds the
field.

## Where the two stand now

twitter.json, 631 KB, one field, minimum of three:

| field position | gjson | this |
|---|---|---|
| `statuses.0.id` | **160 ns** | 58,345 ns |
| `statuses.50.id` | **57,977 ns** | 59,232 ns |
| `search_metadata.max_id` | 115,617 ns | **58,674 ns** |

This package is flat, because it indexes the whole document whatever is asked
of it. gjson is proportional to how far in the field is. They cross near the
middle: a field in the first half is gjson's, a field in the second half is
this one's.

That is the summary and it is what the README says. It is also not the
interesting number.

## The interesting number

Both are linear in the bytes they touch. The rates:

| | ns | bytes | ns/byte |
|---|---|---|---|
| gjson, full scan | 115,617 | 631,515 | 0.183 |
| this, index 4 KB | 865 | 4,096 | 0.211 |
| this, index 64 KB | 10,712 | 65,536 | 0.163 |
| this, index 631 KB | 59,196 | 631,515 | 0.094 |

**Building the index over a prefix is cheaper per byte than scanning that prefix
with a byte loop**, once the prefix is past about 8 KB — 0.094 against 0.183 at
full size, near enough to twice. gjson wins the first-field case by reading two
hundred bytes where this reads six hundred thousand, not by reading them faster.

So an index built only as far as the field would be ahead of gjson at every
position, not just past the midpoint. And unlike gjson's scan it would leave
something behind: the second query against the same document is free, which is
the whole argument for an index and is why the crossover in the README is stated
in queries rather than in bytes.

## What blocked it, and what still does

`buildIndex` could not produce an index for a prefix cut mid-value. That half is
done: **partial mode** now indexes what is there and reports `safeEnd`, one past
the last top-level container that closed, recording syntax errors with their
position and reporting them only if they fall before that point. It was built
for the streaming decoder, where it removed a scalar framing pass worth 17% of a
decode and made this the fastest of the four decoders measured.

It is not enough for lazy paths, and the reason is worth stating because it was
not obvious until partial mode existed.

`safeEnd` marks whole *top-level* values. A document that is one big object has
no top-level value that closes until the last byte, so `safeEnd` stays 0 for
every prefix and the mark says nothing useful. Streaming is the case where it
works, because a stream is a sequence of top-level values by definition.

What lazy paths need instead is to **navigate a partial index**: descend into a
container that has not closed yet, using the bracket positions that are known,
and distinguish "this field is not in the document" from "this field is not in
the part indexed so far". Concretely:

- `matchBracket` must answer "not yet" for an opener whose partner is past the
  indexed region, instead of treating it as an internal error;
- the descent must propagate that upward rather than returning absent;
- the caller extends by another window and resumes, rather than restarting.

The last of those is what makes it worth doing rather than merely possible: work
already done must not be repeated, or growing the prefix geometrically costs
twice what indexing it once would.

## What it would be worth

The rates above, unchanged: about twice gjson per byte. A field in the first
4 KB of a 631 KB document would cost roughly 865 ns against gjson's 160 ns for
the very first field and 58,000 ns for one in the middle — ahead everywhere but
the first few hundred bytes, and leaving an index behind that makes the next
query free.

## Since then: the first query stopped being the price

The table above was measured when the path API went through a validating
[Parse]. It does not any more — `GetPath` indexes with `Scan`, which is what
gjson.Get does too: neither validates the parts of the document it did not walk
through.

Indexing twitter.json without validating is 57 µs against 244 µs with, and that
is the whole of the difference. Two passes, minimum of many:

| query | gjson | this |
|---|---|---|
| one field near the front | 105 µs | **83 µs** |
| one field near the back | 105 µs | **85 µs** |
| ten fields, one document | 634 µs | **239 µs** |

So the crossover that this document was written to explain is gone. gjson is
proportional to how far into the document the field is and pays it again for
every query; this is proportional to the document once. There is no field
position and no query count at which gjson is now ahead.

What has not changed is the reason: gjson keeps nothing. That is still the right
design for one query against one document that will never be asked again, and it
is still why gjson needs no allocation to speak of. It is simply not faster.
