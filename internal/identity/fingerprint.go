// Package identity creates deterministic hashes for domain configuration.
package identity

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// Fingerprint returns a SHA-256 hash of JSON encoding for a struct made only
// from deterministic scalar fields. Callers should use dedicated input structs
// rather than maps, whose construction can conceal accidental schema changes.
func Fingerprint(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}

	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}
