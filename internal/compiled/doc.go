// Package compiled is where the mirror's compiled dataset lives: the artifacts the
// serving path reads instead of parsing the publisher's XML at query time.
//
// Nothing here is wired into serving yet. The first pass is measurement: what a
// compiled document costs in each candidate encoding, and whether the numbers justify
// replacing the parse-on-read path at all. See codec_bench_test.go and the reports it
// writes to testdata, which is where the decisions in this package should be argued
// from rather than asserted.
package compiled
