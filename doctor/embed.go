// Package doctor carries waspdoctor, the doctor's loop in Python shared by DBee
// and the Hive, so a Go installer lays the very version its go.mod pins: one
// implementation, no copy staged beside it.
package doctor

import "embed"

// Python is the waspdoctor package: waspdoctor/*.py, rooted at waspdoctor/.
//
//go:embed waspdoctor/*.py
var Python embed.FS
