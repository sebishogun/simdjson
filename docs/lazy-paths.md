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

That is the honest summary and it is what the README says. It is also not the
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

## What blocks it

`buildIndex` already works a window at a time on documents past 4 MB. Stopping
early needs one thing it cannot currently do: **produce an index for a prefix
whose tail is cut mid-value.** A prefix of a document is not a document. The
scan reaches the end inside a string, or with brackets still open, and reports a
syntax error rather than a partial answer.

What it would have to return instead:

- the masks, which are correct up to the last complete string;
- the bracket positions and their pairs, up to the last one that closed;
- the offset past which nothing is known.

and the path descent would have to distinguish *absent* from *not yet indexed*,
extending by another window in the second case.

## The same blocker, twice

This is the same missing capability as the one holding back the streaming
decoder. `Decoder.load` has to find where the last whole value ends before it
can build an index, because an index over half a value is an error — so it runs
a scalar framing pass over bytes the vector index is about to read again. That
pass is 17% of a stream decode, and it is the difference between this and goccy
there.

One change closes both: an index build that tolerates a truncated tail and
reports how far it got. The streaming decoder stops needing the framing pass at
all, and lazy paths become possible.

That is the next thing to build here, and it is worth roughly 17% of streaming
plus the whole first-field case against gjson.
