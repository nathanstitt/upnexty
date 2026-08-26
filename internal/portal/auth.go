// Package portal serves the configuration UI, in AP mode and on the LAN.
package portal

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"strings"
)

// DefaultPassword derives the shipped admin password from the WiFi MAC: the
// last six hex digits, lowercase, no separators. It is per-device rather than a
// shared constant, and it is shown on the panel while the board is unconfigured
// so first-run needs no documentation. Returns "" when the MAC is unavailable
// (driver not loaded) or malformed, which never authenticates -- see CheckPassword.
func DefaultPassword(mac string) string {
	clean := strings.ToLower(strings.TrimSpace(mac))
	clean = strings.ReplaceAll(clean, ":", "")
	clean = strings.ReplaceAll(clean, "-", "")
	if len(clean) < 6 {
		return ""
	}
	suffix := clean[len(clean)-6:]
	// Validate rather than trust: this function mints the credential guarding
	// the portal, and a malformed MAC ("unknown", a truncated sysfs read) would
	// otherwise become a real, guessable password rather than the safe empty
	// case the caller expects.
	if _, err := hex.DecodeString(suffix); err != nil {
		return ""
	}
	return suffix
}

// HashPassword hashes an admin password for storage in config.json.
//
// This is a bare SHA-256, not bcrypt/scrypt/argon2: golang.org/x/crypto is not
// a dependency of this project and no new modules may be added. That is weaker
// against offline attack on a stolen config file, and acceptable here -- the
// threat this guards against is a houseguest on the LAN reconfiguring a wall
// display, not a credential-stuffing campaign.
func HashPassword(pw string) string {
	sum := sha256.Sum256([]byte(pw))
	return hex.EncodeToString(sum[:])
}

// CheckPassword reports whether pw is correct. With no hash configured it
// accepts the MAC-derived default; once a hash is set the default stops
// working. An empty password never authenticates, whatever the state.
func CheckPassword(pw, hash, mac string) bool {
	if pw == "" {
		return false
	}
	if hash == "" {
		def := DefaultPassword(mac)
		if def == "" {
			return false
		}
		// ConstantTimeCompare on variable-length strings leaks whether a guess
		// matches the 6-byte default length, but this is accepted under the
		// stated threat model (a houseguest on the LAN, not a timing attacker).
		return subtle.ConstantTimeCompare([]byte(pw), []byte(def)) == 1
	}
	return subtle.ConstantTimeCompare([]byte(HashPassword(pw)), []byte(hash)) == 1
}
