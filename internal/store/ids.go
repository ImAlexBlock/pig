package store

import (
	"crypto/rand"
	"encoding/base32"
)

var idEnc = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

func newID(prefix string) string {
	var b [10]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("store: random: " + err.Error())
	}
	return prefix + idEnc.EncodeToString(b[:])
}

// NewSessionID returns a fresh ses_ identifier.
func NewSessionID() string { return newID("ses_") }

// NewEntryID returns a fresh ent_ identifier.
func NewEntryID() string { return newID("ent_") }

// NewArtifactID returns a fresh art_ identifier.
func NewArtifactID() string { return newID("art_") }

// NewCompactionID returns a fresh cmp_ identifier.
func NewCompactionID() string { return newID("cmp_") }

// NewJobID returns a fresh job_ identifier.
func NewJobID() string { return newID("job_") }

// NewRunID returns a fresh run_ identifier.
func NewRunID() string { return newID("run_") }
