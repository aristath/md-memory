package memory

import (
	"crypto/rand"
	"math/big"
	"strings"
)

const idAlphabet = "0123456789abcdefghijklmnopqrstuvwxyz"

// IDLength is the number of characters in a record ID.
const IDLength = 12

// NewID generates a candidate ID. The store must check its uniqueness before use.
func NewID() (string, error) {
	var id [IDLength]byte
	for i := range id {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(idAlphabet))))
		if err != nil {
			return "", err
		}
		id[i] = idAlphabet[n.Int64()]
	}
	return string(id[:]), nil
}

// ValidID reports whether id uses the store's filename and reference format.
func ValidID(id string) bool {
	if len(id) != IDLength {
		return false
	}
	for i := range id {
		if !strings.ContainsRune(idAlphabet, rune(id[i])) {
			return false
		}
	}
	return true
}
