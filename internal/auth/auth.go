// Package auth handles the app's two roles. Administrators sign in with a
// single-use link printed by `retail admin login-link` (no passwords are
// stored), which starts a server-side session. Everyone else is an
// anonymous guest, identified only by a random contributor id in a cookie
// so their submissions can be grouped and capped.
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	AdminCookie       = "rp_admin"
	ContributorCookie = "rp_contrib"

	LoginTokenTTL     = 15 * time.Minute
	SessionTTL        = 30 * 24 * time.Hour
	contributorMaxAge = 365 * 24 * time.Hour
)

// ErrInvalidToken means a login link is unknown, expired or already used.
var ErrInvalidToken = errors.New("this sign-in link is invalid, expired or already used")

// Viewer is who is making the current request.
type Viewer struct {
	Admin         bool
	AdminName     string
	ContributorID string // empty until the guest first submits something
}

// DecidedBy is how this viewer is recorded in match_decisions.decided_by.
func (v Viewer) DecidedBy() string {
	if v.Admin {
		return "admin:" + v.AdminName
	}
	return ""
}

type ctxKey struct{}

func WithViewer(ctx context.Context, v Viewer) context.Context {
	return context.WithValue(ctx, ctxKey{}, v)
}

func ViewerFrom(ctx context.Context) Viewer {
	v, _ := ctx.Value(ctxKey{}).(Viewer)
	return v
}

// newToken returns a random URL-safe token and its SHA-256 hash.
func newToken(n int) (string, []byte, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", nil, err
	}
	raw := base64.RawURLEncoding.EncodeToString(b)
	return raw, hashToken(raw), nil
}

func hashToken(raw string) []byte {
	h := sha256.Sum256([]byte(raw))
	return h[:]
}

// CreateLoginToken stores a new single-use login token for adminName and
// returns the raw token to put in the link.
func CreateLoginToken(ctx context.Context, pool *pgxpool.Pool, adminName string, ttl time.Duration) (string, error) {
	raw, hash, err := newToken(32)
	if err != nil {
		return "", err
	}
	_, err = pool.Exec(ctx, `
		INSERT INTO admin_login_tokens (token_hash, admin_name, expires_at) VALUES ($1, $2, $3)`,
		hash, adminName, time.Now().Add(ttl))
	return raw, err
}

// PeekLoginToken reports who a still-valid token would sign in, without
// consuming it — link previewers and mail scanners issue GETs, so only the
// explicit POST may burn the token.
func PeekLoginToken(ctx context.Context, pool *pgxpool.Pool, raw string) (string, error) {
	var name string
	err := pool.QueryRow(ctx, `
		SELECT admin_name FROM admin_login_tokens
		WHERE token_hash = $1 AND used_at IS NULL AND expires_at > now()`, hashToken(raw)).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrInvalidToken
	}
	return name, err
}

// RedeemLoginToken consumes a login token and starts a session, returning
// the raw session token for the cookie. The token is marked used in the
// same statement that validates it, so it can only ever be redeemed once.
func RedeemLoginToken(ctx context.Context, pool *pgxpool.Pool, raw string) (adminName, session string, err error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return "", "", err
	}
	defer tx.Rollback(ctx)

	err = tx.QueryRow(ctx, `
		UPDATE admin_login_tokens SET used_at = now()
		WHERE token_hash = $1 AND used_at IS NULL AND expires_at > now()
		RETURNING admin_name`, hashToken(raw)).Scan(&adminName)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", ErrInvalidToken
	}
	if err != nil {
		return "", "", err
	}

	session, hash, err := newToken(32)
	if err != nil {
		return "", "", err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO admin_sessions (token_hash, admin_name, expires_at) VALUES ($1, $2, $3)`,
		hash, adminName, time.Now().Add(SessionTTL)); err != nil {
		return "", "", err
	}
	return adminName, session, tx.Commit(ctx)
}

// SessionAdmin returns the admin a session token belongs to, if it's valid.
func SessionAdmin(ctx context.Context, pool *pgxpool.Pool, raw string) (string, bool, error) {
	var name string
	err := pool.QueryRow(ctx, `
		UPDATE admin_sessions SET last_seen_at = now()
		WHERE token_hash = $1 AND expires_at > now()
		RETURNING admin_name`, hashToken(raw)).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	return name, err == nil, err
}

func DeleteSession(ctx context.Context, pool *pgxpool.Pool, raw string) error {
	_, err := pool.Exec(ctx, `DELETE FROM admin_sessions WHERE token_hash = $1`, hashToken(raw))
	return err
}

// RevokeAllSessions signs every administrator out everywhere.
func RevokeAllSessions(ctx context.Context, pool *pgxpool.Pool) (int64, error) {
	tag, err := pool.Exec(ctx, `DELETE FROM admin_sessions`)
	return tag.RowsAffected(), err
}

// LoadOrCreateSecret returns a random 32-byte server secret stored under
// name, generating it on first use so it stays stable across restarts.
func LoadOrCreateSecret(ctx context.Context, pool *pgxpool.Pool, name string) ([]byte, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	if _, err := pool.Exec(ctx, `INSERT INTO app_secrets (name, value) VALUES ($1, $2) ON CONFLICT (name) DO NOTHING`, name, b); err != nil {
		return nil, err
	}
	var v []byte
	err := pool.QueryRow(ctx, `SELECT value FROM app_secrets WHERE name = $1`, name).Scan(&v)
	return v, err
}

// Requests carries the per-deployment settings for reading requests.
type Requests struct {
	// TrustProxy makes the client IP and HTTPS detection come from
	// X-Forwarded-For / X-Forwarded-Proto. Only enable it behind a reverse
	// proxy that sets them; otherwise any client could spoof its IP.
	TrustProxy bool
	IPSecret   []byte
}

// ClientIP is the request's originating IP address.
func (q Requests) ClientIP(r *http.Request) string {
	if q.TrustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			// The proxy appends the address it saw, so the last entry is
			// the one we can trust.
			parts := strings.Split(xff, ",")
			if ip := strings.TrimSpace(parts[len(parts)-1]); ip != "" {
				return ip
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// IPHash is a keyed hash of the client IP: stable enough to rate-limit and
// spot abuse, without storing anyone's address.
func (q Requests) IPHash(r *http.Request) []byte {
	m := hmac.New(sha256.New, q.IPSecret)
	m.Write([]byte(q.ClientIP(r)))
	return m.Sum(nil)
}

func (q Requests) IsHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return q.TrustProxy && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// SetAdminCookie stores a session token in the admin cookie.
func (q Requests) SetAdminCookie(w http.ResponseWriter, r *http.Request, session string) {
	http.SetCookie(w, &http.Cookie{
		Name: AdminCookie, Value: session, Path: "/",
		MaxAge: int(SessionTTL.Seconds()), HttpOnly: true, Secure: q.IsHTTPS(r), SameSite: http.SameSiteLaxMode,
	})
}

func (q Requests) ClearAdminCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: AdminCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: q.IsHTTPS(r), SameSite: http.SameSiteLaxMode,
	})
}

// EnsureContributor returns the guest's contributor id, issuing one (and
// its cookie) on their first submission.
func (q Requests) EnsureContributor(w http.ResponseWriter, r *http.Request, v Viewer) (string, error) {
	if v.ContributorID != "" {
		return v.ContributorID, nil
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	id := hex.EncodeToString(b)
	http.SetCookie(w, &http.Cookie{
		Name: ContributorCookie, Value: id, Path: "/",
		MaxAge: int(contributorMaxAge.Seconds()), HttpOnly: true, Secure: q.IsHTTPS(r), SameSite: http.SameSiteLaxMode,
	})
	return id, nil
}

// validContributorID accepts only ids this package could have issued, so
// a hand-crafted cookie can't smuggle arbitrary text into the database.
func validContributorID(s string) bool {
	if len(s) != 32 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// SessionLookup resolves a raw session token to an admin name.
type SessionLookup func(ctx context.Context, raw string) (string, bool, error)

// Middleware resolves the Viewer once per request and stores it in the
// request context.
func Middleware(lookup SessionLookup, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var v Viewer
		if c, err := r.Cookie(AdminCookie); err == nil && c.Value != "" {
			name, ok, err := lookup(r.Context(), c.Value)
			if err != nil {
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			if ok {
				v.Admin, v.AdminName = true, name
			}
		}
		if c, err := r.Cookie(ContributorCookie); err == nil && validContributorID(c.Value) {
			v.ContributorID = c.Value
		}
		next.ServeHTTP(w, r.WithContext(WithViewer(r.Context(), v)))
	})
}

// RequireAdmin refuses guests with 403.
func RequireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !ViewerFrom(r.Context()).Admin {
			http.Error(w, "administrators only", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}
