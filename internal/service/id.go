package service

import (
	"crypto/rand"
	"encoding/hex"
)

// newLocalID returns a 32-character hex identifier generated locally with
// crypto/rand. It is intentionally dependency-free for V1; swapping to a UUID
// library later only requires replacing this function.
func newLocalID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand.Read does not fail on supported platforms.
		panic(err)
	}
	return hex.EncodeToString(b[:])
}