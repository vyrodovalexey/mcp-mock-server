package control

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestRequireBearerUnit pins the constant-time bearer middleware's decisions
// directly: only an exact "Bearer <token>" match passes; everything else is a
// 401 unauthorized error object (security.md §2.1, REV-001). This is the guard
// the review found missing — a configured token that permits the off-loopback
// bind must actually be checked on every request.
func TestRequireBearerUnit(t *testing.T) {
	const token = "s3cr3t-token"
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := requireBearer(token, next)

	cases := []struct {
		name       string
		authHeader string
		set        bool
		wantStatus int
	}{
		{"no header", "", false, http.StatusUnauthorized},
		{"empty header", "", true, http.StatusUnauthorized},
		{"wrong scheme", "Basic " + token, true, http.StatusUnauthorized},
		{"bare token no scheme", token, true, http.StatusUnauthorized},
		{"wrong token", "Bearer nope", true, http.StatusUnauthorized},
		{"token prefix of value", "Bearer s3cr3t", true, http.StatusUnauthorized},
		{"correct token", "Bearer " + token, true, http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/v1/instances", http.NoBody)
			if tc.set {
				req.Header.Set("Authorization", tc.authHeader)
			}
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)
			if rr.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rr.Code, tc.wantStatus)
			}
			if tc.wantStatus == http.StatusUnauthorized {
				var eo errorObject
				if err := json.Unmarshal(rr.Body.Bytes(), &eo); err != nil {
					t.Fatalf("decode error body: %v", err)
				}
				if eo.Code != codeUnauthorized {
					t.Errorf("error code = %q, want %q", eo.Code, codeUnauthorized)
				}
			}
		})
	}
}

// TestRequireBearerEmptyTokenFailsClosed asserts that a middleware built with an
// empty token rejects every request, so a misconfiguration can never accept an
// arbitrary caller. This is the fail-closed property the review demands: the
// guard must not "look authenticated and not be".
func TestRequireBearerEmptyTokenFailsClosed(t *testing.T) {
	h := requireBearer("", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	for _, auth := range []string{"", "Bearer ", "Bearer anything"} {
		req := httptest.NewRequest(http.MethodGet, "/v1/instances", http.NoBody)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("empty-token middleware accepted %q (status %d)", auth, rr.Code)
		}
	}
}

// TestServerTCPRequiresAuth is the REV-001 integration property: when the facade
// marks the TCP listener as requiring auth (a non-loopback bind), the Server
// serves an auth-wrapped handler over TCP, so a same-namespace peer cannot read
// the journal or mutate the mock without the token — while the unix socket, whose
// 0600 perms are its access control, is served without per-request auth.
func TestServerTCPRequiresAuth(t *testing.T) {
	const token = "unit-token"
	h, _ := newTestHandler()
	tcpLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("tcp listen: %v", err)
	}
	s := New(Config{Handler: h, TCPListener: tcpLn, Token: token, TCPRequiresAuth: true})
	s.Serve()
	defer func() { _ = s.Shutdown(context.Background()) }()

	base := "http://" + tcpLn.Addr().String()
	client := &http.Client{Timeout: 5 * time.Second}

	// The journal-read surface is protected without a token.
	if status, _ := getVia(t, client, base+"/v1/instances/a/journal", ""); status != http.StatusUnauthorized {
		t.Errorf("journal read without token = %d, want 401", status)
	}

	// With the correct token the same request succeeds.
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, base+"/v1/instances/a/journal", http.NoBody)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("authenticated request: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("journal read with token = %d, want 200", resp.StatusCode)
	}
}

// TestServerLoopbackNoAuth asserts a loopback TCP listener (TCPRequiresAuth
// false) serves without per-request auth, matching the contract's bearerAuth
// condition ("Required whenever the listener is not loopback and not a unix
// socket"). This keeps the default 127.0.0.1 developer workflow tokenless.
func TestServerLoopbackNoAuth(t *testing.T) {
	h, _ := newTestHandler()
	tcpLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("tcp listen: %v", err)
	}
	s := New(Config{Handler: h, TCPListener: tcpLn})
	s.Serve()
	defer func() { _ = s.Shutdown(context.Background()) }()

	base := "http://" + tcpLn.Addr().String()
	if status, _ := getVia(t, &http.Client{Timeout: 5 * time.Second}, base+"/v1/instances", ""); status != http.StatusOK {
		t.Errorf("loopback read = %d, want 200", status)
	}
}

// TestRequiresAuth pins the loopback predicate the facade uses to decide whether
// to wrap the TCP handler, so the auth boundary and the CheckBind gate share one
// notion of "loopback".
func TestRequiresAuth(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:8080", false},
		{"localhost:8080", false},
		{"[::1]:8080", false},
		{"0.0.0.0:8080", true},
		{"192.168.1.10:8080", true},
		{":8080", true},
	}
	for _, tc := range cases {
		if got := RequiresAuth(tc.addr); got != tc.want {
			t.Errorf("RequiresAuth(%q) = %v, want %v", tc.addr, got, tc.want)
		}
	}
}
