package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"retail-prices/internal/auth"
)

// These exercise the real middleware chain and router with no database:
// every request here must be refused before a handler touches the pool.
func testHandler() http.Handler {
	s := &server{limiter: auth.NewLimiter(guestLimits...)}
	return s.handler(http.NotFoundHandler())
}

func TestAdminOnlyRoutesRefuseGuests(t *testing.T) {
	h := testHandler()
	for _, tc := range []struct{ method, path string }{
		{"GET", "/match/bulk"},
		{"POST", "/match/bulk"},
		{"GET", "/admin/moderation"},
		{"POST", "/admin/moderation/review"},
	} {
		r := httptest.NewRequest(tc.method, tc.path, nil)
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Errorf("%s %s as guest: got %d, want 403", tc.method, tc.path, w.Code)
		}
	}
}

func TestCrossSitePostsAreRejected(t *testing.T) {
	h := testHandler()
	for _, path := range []string{"/match/decide", "/match/manual", "/products/1/report", "/admin/login", "/admin/logout"} {
		r := httptest.NewRequest("POST", path, strings.NewReader("left=1&right=2&decision=matched"))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Sec-Fetch-Site", "cross-site")
		r.Header.Set("Origin", "https://evil.example")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Errorf("cross-site POST %s: got %d, want 403", path, w.Code)
		}
	}
}

func TestSecurityHeaders(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/match/bulk", nil)
	testHandler().ServeHTTP(w, r)
	for k, want := range map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "same-origin",
	} {
		if got := w.Header().Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
}

func TestFlashOnlyRendersKnownCodes(t *testing.T) {
	if flashText(flashSent) == "" {
		t.Error("known flash code has no text")
	}
	if got := flashText("<script>alert(1)</script>"); got != "" {
		t.Errorf("unknown flash code rendered %q", got)
	}
	if got := withFlash("/match?x=1", flashSent); got != "/match?x=1&flash=sent" {
		t.Errorf("withFlash = %q", got)
	}
}
