package memory

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"strings"
)

const idAlphabet = "0123456789abcdefghijklmnopqrstuvwxyz"

// IDSuffixLength is the number of random characters after a record's prefix.
const IDSuffixLength = 12

// IDLength includes the two-character prefix and the random suffix.
const IDLength = 2 + IDSuffixLength

// NewID generates a candidate ID. The store must check its uniqueness before use.
func NewID(prefix string) (string, error) {
	if !validPrefix(prefix) {
		return "", fmt.Errorf("invalid ID prefix %q: use m_, e_, t_, or s_", prefix)
	}
	var id [IDSuffixLength]byte
	for i := range id {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(idAlphabet))))
		if err != nil {
			return "", err
		}
		id[i] = idAlphabet[n.Int64()]
	}
	return prefix + string(id[:]), nil
}

// ValidID reports whether id uses the store's filename and reference format.
func ValidID(id string) bool {
	if len(id) != IDLength || !validPrefix(id[:2]) {
		return false
	}
	for i := 2; i < len(id); i++ {
		if !strings.ContainsRune(idAlphabet, rune(id[i])) {
			return false
		}
	}
	return true
}

func validPrefix(prefix string) bool {
	switch prefix {
	case "m_", "e_", "t_", "s_":
		return true
	}
	return false
}
