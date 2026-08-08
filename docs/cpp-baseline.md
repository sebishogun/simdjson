# The C++ simdjson baseline

What the original does on this machine, same corpus bytes, single thread,
minimum of eight. simdjson 4.6.4 amalgamation, clang -O3 -march=native;
`make bench-cpp` reproduces it (the amalgamation is fetched on demand, not
committed). Our numbers are the same session's `BenchmarkCorpus` minima.

| MB/s, single thread | C++ parse (validated tape) | ours `Parse` (validated) | ours `Scan` (index only) |
|---|---|---|---|
| twitter | 6,997 | 3,189 | 12,537 |
| citm | 6,856 | 3,150 | 8,902 |
| canada | 1,602 | **1,982** | 6,808 |

Reading it honestly:

- **C++ leads validated parsing 2.2× on the string-heavy corpora.** Its tape
  build fuses structure, UTF-8 and value validation into one branch-free
  pass; our `Parse` is the index plus a grammar descent. That is the
  architectural bill for a navigable `Doc` with encoding/json's exact error
  surface, in Go, with no cgo.
- **canada is ours, against the original.** 1,980 vs 1,602: the SWAR digit
  runs in the number validator outrun their float parse on the corpus that
  is nothing but floats.
- `Scan` is a different contract (no validation) and is not comparable to
  their parse; the row is there because its consumers exist.
- Their `ondemand` two-field read measures 14,048 MB/s on twitter — the
  never-materialize design at its best; our `GetPath` class serves the same
  use.
- **Past 8 MB the comparison inverts.** C++ simdjson does not parallelize a
  single document (one worker thread pipelines stage 1 of the *next* batch;
  ceiling 2× and only for many-document streams). This library shards one
  document: `Parse` 11.5 GB/s and `Scan` 31 GB/s at 64 MB on this machine,
  against their single-thread numbers above.

The gap statement for the README stays what it is: fastest **Go** JSON
library; the original C++ remains faster at single-threaded validated
parsing of string-heavy documents, and this table is the size of that gap.
