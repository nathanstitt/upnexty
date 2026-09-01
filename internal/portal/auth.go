// Package portal serves the configuration UI, in AP mode and on the LAN.
package portal

import (
	"crypto/hmac"
	"crypto/rand"
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

// SessionCookie is the name of the cookie holding a portal session.
const SessionCookie = "upnext_session"

// sessionSecret is minted once per process. Sessions therefore do not survive a
// restart, which is the right default for a wall display: the board reboots
// rarely, and a session that outlived a reboot would have to be persisted to
// NAND and invalidated on password change. Re-entering the password after a
// restart is a small cost for not storing credentials at rest.
//
// crypto/rand, not math/rand: this value is the whole strength of the session
// token. A failure to read the OS entropy source is fatal rather than silently
// falling back -- a predictable secret is worse than no portal.
var sessionSecret = mustRandom(32)

func mustRandom(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("portal: cannot read random bytes for session secret: " + err.Error())
	}
	return b
}

// NewSessionToken mints a session value bound to the current password hash.
//
// The token is HMAC(secret, hash) rather than an opaque random string kept in a
// server-side set, so there is no session table to grow or lock. Binding it to
// the password hash means changing the password invalidates every existing
// session for free: the recomputed HMAC no longer matches what old cookies
// carry. mac is folded in so a board that has not set a password yet (empty
// hash) still produces a per-device token rather than a constant.
func NewSessionToken(hash, mac string) string {
	m := hmac.New(sha256.New, sessionSecret)
	m.Write([]byte(hash))
	m.Write([]byte{0}) // domain separator: hash and mac cannot run together
	m.Write([]byte(mac))
	return hex.EncodeToString(m.Sum(nil))
}

// ValidSession reports whether a cookie value is a live session.
func ValidSession(token, hash, mac string) bool {
	if token == "" {
		return false
	}
	return hmac.Equal([]byte(token), []byte(NewSessionToken(hash, mac)))
}
