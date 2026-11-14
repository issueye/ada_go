package runtime

import (
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"strconv"
	"strings"
	"time"
)

//go:embed runtime.js
var source string

// frameTokenPlaceholder is replaced by a per-process secret before injection.
// Keep it in sync with runtime.js.
const frameTokenPlaceholder = "__BU_FRAME_TOKEN__"

// Source returns the page runtime with a fixed development token. Production
// code must use SourceWithToken so every launch gets its own secret.
func Source() string { return SourceWithToken("dev-frame-token") }

// SourceWithToken returns the page runtime with the given per-process secret
// embedded in its closure. The token is never exposed on window: it only gates
// iframe relay commands and input-mask changes, which page scripts must not be
// able to forge or read.
func SourceWithToken(token string) string {
	if token == "" {
		token = "empty-frame-token"
	}
	return strings.Replace(source, frameTokenPlaceholder, token, 1)
}

// NewToken returns a random frame token. The fallback keeps a failed entropy
// read from collapsing every process onto the same guessable secret.
func NewToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "fallback-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(b)
}

// Version is the runtime's public version.
const Version = "0.3.0"
