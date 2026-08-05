// The C++ simdjson baseline over the same corpora the Go rows use.
//
// Three measurements per corpus, minimum of N runs each, matching the Go
// side's estimator:
//   parse    - dom::parser::parse, the validated tape (compare: our Parse)
//   walk     - recursive visit of every element on the tape
//              (compare: Parse + a full navigation)
//   ondemand - ondemand::parser reading two fields out of twitter
//              (compare: GetPath-style access)
// Corpora arrive DECOMPRESSED on argv; run via `make bench-cpp`.
#include "simdjson.h"
#include <chrono>
#include <cstdio>
#include <string>
#include <vector>
#include <fstream>
#include <sstream>

using namespace simdjson;
using clk = std::chrono::steady_clock;

static double mbps(size_t bytes, double secs) {
  return (double)bytes / 1e6 / secs;
}

static size_t walk(dom::element e) {
  size_t n = 1;
  switch (e.type()) {
  case dom::element_type::ARRAY:
    for (dom::element c : dom::array(e)) n += walk(c);
    break;
  case dom::element_type::OBJECT:
    for (dom::key_value_pair kv : dom::object(e)) n += walk(kv.value);
    break;
  default: break;
  }
  return n;
}

int main(int argc, char **argv) {
  const int runs = 8;
  for (int a = 1; a < argc; a++) {
    std::ifstream f(argv[a], std::ios::binary);
    std::stringstream ss; ss << f.rdbuf();
    std::string data = ss.str();
    padded_string ps(data);

    dom::parser parser;
    double best_parse = 1e99, best_walk = 1e99;
    size_t elems = 0;
    for (int r = 0; r < runs; r++) {
      auto t0 = clk::now();
      dom::element doc = parser.parse(ps);
      auto t1 = clk::now();
      elems = walk(doc);
      auto t2 = clk::now();
      double dp = std::chrono::duration<double>(t1 - t0).count();
      double dw = std::chrono::duration<double>(t2 - t1).count();
      if (dp < best_parse) best_parse = dp;
      if (dw < best_walk) best_walk = dw;
    }
    printf("%s  parse %7.0f MB/s   walk %7.0f MB/s (%zu elems)\n", argv[a],
           mbps(ps.size(), best_parse), mbps(ps.size(), best_walk), elems);

    if (std::string(argv[a]).find("twitter") != std::string::npos) {
      ondemand::parser op;
      double best = 1e99;
      int64_t sink = 0;
      for (int r = 0; r < runs * 4; r++) {
        auto t0 = clk::now();
        ondemand::document d = op.iterate(ps);
        sink += int64_t(d["search_metadata"]["count"].get_int64());
        auto t1 = clk::now();
        double dt = std::chrono::duration<double>(t1 - t0).count();
        if (dt < best) best = dt;
      }
      printf("%s  ondemand two-field read %7.0f MB/s (sink %ld)\n", argv[a],
             mbps(ps.size(), best), (long)sink);
    }
  }
  return 0;
}
