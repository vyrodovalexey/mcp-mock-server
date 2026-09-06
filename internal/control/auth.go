package control

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// bearerPrefix is the RFC 6750 Authorization scheme the control API accepts.
const bearerPrefix = "Bearer "

// requireBearer wraps next so every request must carry a valid bearer token
// before it reaches the control handler (security.md §2.1, contract
// #/components/securitySchemes/bearerAuth). It is applied ONLY to a listener the
// contract says must be authenticated — a non-loopback TCP listener — because
// that is the reachable, adversarial surface (security.md §8: the hub is
// same-namespace and adversarial by assumption). The loopback and unix-socket
// listeners are not wrapped: the socket's 0600 permissions are its access
// control, and loopback is unreachable from another pod.
//
// The comparison is constant-time (crypto/subtle) so a caller cannot learn the
// token one byte at a time by timing (security.md §2.1 "Comparison"). A missing
// or malformed header, or any mismatch, is ErrUnauthorized → 401 through the
// same error encoder every other control error uses, so the wire error object
// stays uniform and leaks no token material.
//
// token is captured once at construction; the middleware holds no mutable state
// and is safe for unbounded concurrent requests (ADR-007).
func requireBearer(token string, next http.Handler) http.Handler {
	want := []byte(token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !bearerOK(want, r.Header.Get("Authorization")) {
			writeError(w, ErrUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// bearerOK reports whether authorization carries a bearer token that matches
// want in constant time. An empty want (no token configured) never matches, so
// a misconfiguration fails closed rather than accepting every request.
func bearerOK(want []byte, authorization string) bool {
	if len(want) == 0 {
		return false
	}
	tok, ok := strings.CutPrefix(authorization, bearerPrefix)
	if !ok {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(tok), want) == 1
}
