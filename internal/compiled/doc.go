// Package compiled is where the mirror's compiled dataset lives: the artifacts the
// serving path reads instead of parsing the publisher's XML at query time.
//
// The writer and reader of the artifact exist, and `retsin artifact build` makes the
// file. What is not here yet is serving: lookup and snippets still parse XML, and they
// will not be pointed at the artifact until the parity gate over all seven media says
// the artifact answers exactly what the parse answers, and a latency measurement says
// what the change is worth.
//
// Decisions in this package are argued from the reports in testdata — codec-bench for
// the encoding and container, artifact-<date> for the measured cost of the container
// that was chosen — not from preference.
package compiled
