package redact

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
)

// LabelPlaceholder replaces a match with [REDACTED:rule-id].
func LabelPlaceholder(ruleID, _ string) string {
	return "[REDACTED:" + ruleID + "]"
}

// StablePlaceholder returns a Placeholder that writes [REDACTED:rule-id:abcd],
// where abcd is the first two bytes of HMAC-SHA256(salt, rule-id NUL match).
// The same secret always maps to the same token under one salt, and the token
// cannot be reversed or correlated across installs without the salt.
func StablePlaceholder(salt []byte) Placeholder {
	key := append([]byte(nil), salt...)
	return func(ruleID, matched string) string {
		m := hmac.New(sha256.New, key)
		m.Write([]byte(ruleID))
		m.Write([]byte{0})
		m.Write([]byte(matched))
		return "[REDACTED:" + ruleID + ":" + hex.EncodeToString(m.Sum(nil)[:2]) + "]"
	}
}
