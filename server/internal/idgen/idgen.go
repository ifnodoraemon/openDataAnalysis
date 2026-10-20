// Package idgen produces the opaque identifiers used for sessions, runs, files
// and other user-visible resources.
//
// Identifiers are random hex rather than a counter: they appear in URLs and in
// the event stream, so they must not be guessable or enumerable. The width is
// deliberately 16 hex characters (64 bits) — 8 characters (32 bits) is small
// enough that collisions become plausible on a busy deployment and that the
// identifier space can be walked.
package idgen

import (
	"crypto/rand"
	"encoding/hex"
)

// HexBytes is the number of random bytes behind a generated identifier.
const HexBytes = 8

// New returns a random 16-character hex identifier.
func New() string {
	var buf [HexBytes]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// crypto/rand.Read never fails on supported platforms; a failure means
		// the process cannot produce safe identifiers at all.
		panic("idgen: random source unavailable: " + err.Error())
	}
	return hex.EncodeToString(buf[:])
}

// Prefixed returns prefix + New(), for the resource families that namespace
// their identifiers.
func Prefixed(prefix string) string { return prefix + New() }
