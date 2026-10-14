// Package dictionary holds the zstd dictionary the artifact's documents are compressed against.
//
// It is fixed on purpose. A daily artifact is uploaded as a new version of one large file, and
// the storage only sends the parts of it that changed (content-defined chunks, see
// docs/opbygning.md). An unchanged document compresses to the same bytes every build only while
// the dictionary is the same, so retraining re-uploads the whole artifact once: it is a deliberate
// step with a new file name and ID, never a side effect of a build.
//
// retsin-1.zdict: 1 MiB, zstd 1.5.7 --train-fastcover over 9,278 documents published up to 2024
// (every 20th document of that period, in artifact order), from the artifact of 2026-10-06.
// Measured on the 6,035 documents published in 2025–2026, which it never saw, at the "better"
// level: 63.3 MB against 80.5 MB without a dictionary at the old default level (−21.4 %), and
// 1,254 MB against 1,642 MB over the whole corpus (−23.6 %). The trainers compared: zstd's
// fastcover and cover (equal, cover 6× slower to train), and the Go library's builder (−16.5 %
// at 112 KiB). Sizes compared: 32 KiB −16.1 %, 110 KiB −18.4 %, 256 KiB −19.7 %, 512 KiB −20.3 %,
// 1 MiB −21.4 %. How to retrain is in docs/opbygning.md.
package dictionary

import _ "embed"

// Current is the dictionary new artifacts are written with.
//
//go:embed retsin-1.zdict
var Current []byte
