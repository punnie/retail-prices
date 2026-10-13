package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestLimiter_CapsAndRefillsPerWindow(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	l := NewLimiter(Limit{Max: 3, Window: time.Hour}, Limit{Max: 5, Window: 24 * time.Hour})
	l.now = func() time.Time { return now }

	for i := 0; i < 3; i++ {
		if !l.Allow("a") {
			t.Fatalf("event %d refused, want allowed", i+1)
		}
	}
	if l.Allow("a") {
		t.Fatal("4th event in the hour allowed, want refused")
	}
	if !l.Allow("b") {
		t.Fatal("another key was refused; limits must be per key")
	}

	now = now.Add(time.Hour)
	if !l.Allow("a") || !l.Allow("a") {
		t.Fatal("hourly window didn't reset")
	}
	// 5 events today: the daily limit now binds even though the hourly one has room.
	if l.Allow("a") {
		t.Fatal("6th event in the day allowed, want refused by the daily limit")
	}
}

func TestLimiter_RefusedEventsAreNotCounted(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	l := NewLimiter(Limit{Max: 1, Window: time.Hour}, Limit{Max: 2, Window: 24 * time.Hour})
	l.now = func() time.Time { return now }
	l.Allow("a")
	for i := 0; i < 10; i++ {
		l.Allow("a") // refused by the hourly limit
	}
	now = now.Add(time.Hour)
	if !l.Allow("a") {
		t.Fatal("refused attempts used up the daily allowance")
	}
}

func TestClientIP_IgnoresForwardedHeadersUnlessTrusted(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.0.0.1:5555"
	r.Header.Set("X-Forwarded-For", "6.6.6.6, 203.0.113.9")
	r.Header.Set("X-Forwarded-Proto", "https")

	direct := Requests{}
	if got := direct.ClientIP(r); got != "10.0.0.1" {
		t.Errorf("untrusted ClientIP = %q, want the socket address", got)
	}
	if direct.IsHTTPS(r) {
		t.Error("untrusted request treated as HTTPS from a spoofable header")
	}

	proxied := Requests{TrustProxy: true}
	if got := proxied.ClientIP(r); got != "203.0.113.9" {
		t.Errorf("trusted ClientIP = %q, want the address the proxy appended", got)
	}
	if !proxied.IsHTTPS(r) {
		t.Error("trusted X-Forwarded-Proto https not honoured")
	}
}

func TestIPHash_IsKeyedAndStable(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "198.51.100.7:1"
	a := Requests{IPSecret: []byte("one")}
	b := Requests{IPSecret: []byte("two")}
	if string(a.IPHash(r)) != string(a.IPHash(r)) {
		t.Error("IPHash not stable")
	}
	if string(a.IPHash(r)) == string(b.IPHash(r)) {
		t.Error("IPHash ignores the secret")
	}
}

func viewerAfterMiddleware(t *testing.T, r *http.Request, lookup SessionLookup) Viewer {
	t.Helper()
	var got Viewer
	h := Middleware(lookup, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = ViewerFrom(r.Context())
	}))
	h.ServeHTTP(httptest.NewRecorder(), r)
	return got
}

func TestMiddleware_ResolvesRoles(t *testing.T) {
	lookup := func(_ context.Context, raw string) (string, bool, error) {
		return "pedro", raw == "good-session", nil
	}

	r := httptest.NewRequest("GET", "/", nil)
	if v := viewerAfterMiddleware(t, r, lookup); v.Admin || v.ContributorID != "" {
		t.Errorf("no cookies: got %+v, want anonymous guest", v)
	}

	r = httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: AdminCookie, Value: "good-session"})
	if v := viewerAfterMiddleware(t, r, lookup); !v.Admin || v.AdminName != "pedro" || v.DecidedBy() != "admin:pedro" {
		t.Errorf("valid session: got %+v", v)
	}

	r = httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: AdminCookie, Value: "forged"})
	if v := viewerAfterMiddleware(t, r, lookup); v.Admin {
		t.Error("unknown session token granted admin")
	}

	r = httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: ContributorCookie, Value: "0123456789abcdef0123456789abcdef"})
	if v := viewerAfterMiddleware(t, r, lookup); v.ContributorID != "0123456789abcdef0123456789abcdef" {
		t.Errorf("contributor cookie not read: %+v", v)
	}

	r = httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: ContributorCookie, Value: "'; DROP TABLE contributions; --"})
	if v := viewerAfterMiddleware(t, r, lookup); v.ContributorID != "" {
		t.Errorf("malformed contributor cookie accepted: %q", v.ContributorID)
	}
}

func TestEnsureContributor_IssuesOnceAndValidId(t *testing.T) {
	q := Requests{}
	w := httptest.NewRecorder()
	id, err := q.EnsureContributor(w, httptest.NewRequest("POST", "/", nil), Viewer{})
	if err != nil {
		t.Fatal(err)
	}
	if !validContributorID(id) {
		t.Fatalf("issued id %q isn't one validContributorID accepts", id)
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != ContributorCookie || !cookies[0].HttpOnly {
		t.Fatalf("expected one HttpOnly contributor cookie, got %+v", cookies)
	}

	w = httptest.NewRecorder()
	again, _ := q.EnsureContributor(w, httptest.NewRequest("POST", "/", nil), Viewer{ContributorID: id})
	if again != id || len(w.Result().Cookies()) != 0 {
		t.Error("an existing contributor was issued a new id")
	}
}

func TestRequireAdmin(t *testing.T) {
	h := RequireAdmin(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) })

	w := httptest.NewRecorder()
	h(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != http.StatusForbidden {
		t.Errorf("guest got %d, want 403", w.Code)
	}

	w = httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/", nil)
	h(w, r.WithContext(WithViewer(r.Context(), Viewer{Admin: true, AdminName: "pedro"})))
	if w.Code != http.StatusTeapot {
		t.Errorf("admin got %d, want the wrapped handler", w.Code)
	}
}

func TestTokensAreRandomAndHashed(t *testing.T) {
	a, ha, _ := newToken(32)
	b, _, _ := newToken(32)
	if a == b {
		t.Fatal("two tokens are identical")
	}
	if string(hashToken(a)) != string(ha) || string(ha) == a {
		t.Fatal("stored hash doesn't match the token, or is the token itself")
	}
}
