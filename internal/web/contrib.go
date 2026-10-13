package web

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"retail-prices/internal/auth"
)

// Contribution kinds, matching the CHECK on contributions.kind.
const (
	kindLink     = "link"
	kindNotMatch = "not_match"
	kindUnlink   = "unlink"
)

const (
	maxNoteLen          = 500
	maxNameLen          = 40
	maxPendingPerGuest  = 300
	honeypotField       = "website"
	contributionSubject = "contribution"
)

// orderPair puts two product ids in the left<right order shared by
// match_candidates, match_decisions and contributions.
func orderPair(a, b int64) (int64, int64) {
	if a > b {
		return b, a
	}
	return a, b
}

// sameGroup reports whether two products currently sit in the same group.
func sameGroup(ctx context.Context, q pgx.Tx, a, b int64) (bool, error) {
	var ok bool
	err := q.QueryRow(ctx, `
		SELECT EXISTS (
		    SELECT 1 FROM product_group_members x
		    JOIN product_group_members y ON y.product_group_id = x.product_group_id
		    WHERE x.retail_product_id = $1 AND y.retail_product_id = $2)`, a, b).Scan(&ok)
	return ok, err
}

// applyDecision is the one place a match decision takes effect, whether an
// administrator made it directly or accepted a guest's proposal: on a match
// it links both products into one group (see linkIntoGroup), and either way
// it records the decision so `retail match` never re-suggests the pair.
func applyDecision(ctx context.Context, tx pgx.Tx, a, b int64, matched bool, decidedBy string) error {
	if a == b {
		return linkError("a product can't be linked to itself")
	}
	l, r := orderPair(a, b)
	decision := "rejected"
	if matched {
		decision = "matched"
		if err := linkIntoGroup(ctx, tx, l, r); err != nil {
			return err
		}
	} else if linked, err := sameGroup(ctx, tx, l, r); err != nil {
		return err
	} else if linked {
		return linkError("these products are currently linked; unlink them instead")
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO match_decisions (left_product_id, right_product_id, decision, decided_by)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (left_product_id, right_product_id)
		DO UPDATE SET decision = EXCLUDED.decision, decided_by = EXCLUDED.decided_by, decided_at = now()`,
		l, r, decision, decidedBy)
	return err
}

// applyUnlink removes removeID from the group it shares with keepID,
// dissolving the group if fewer than two members remain, and records the
// pair as rejected so it is never suggested again.
func applyUnlink(ctx context.Context, tx pgx.Tx, keepID, removeID int64, decidedBy string) error {
	var groupID int64
	err := tx.QueryRow(ctx, `
		SELECT x.product_group_id FROM product_group_members x
		JOIN product_group_members y ON y.product_group_id = x.product_group_id
		WHERE x.retail_product_id = $1 AND y.retail_product_id = $2
		FOR UPDATE OF x`, keepID, removeID).Scan(&groupID)
	if errors.Is(err, pgx.ErrNoRows) {
		return linkError("these products are no longer linked")
	}
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM product_group_members WHERE retail_product_id = $1`, removeID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM product_groups g WHERE g.id = $1
		AND (SELECT count(*) FROM product_group_members m WHERE m.product_group_id = g.id) < 2`, groupID); err != nil {
		return err
	}
	l, r := orderPair(keepID, removeID)
	_, err = tx.Exec(ctx, `
		INSERT INTO match_decisions (left_product_id, right_product_id, decision, decided_by)
		VALUES ($1, $2, 'rejected', $3)
		ON CONFLICT (left_product_id, right_product_id)
		DO UPDATE SET decision = 'rejected', decided_by = EXCLUDED.decided_by, decided_at = now()`,
		l, r, decidedBy)
	return err
}

// inTx runs fn in a transaction, committing only if it succeeds.
func (s *server) inTx(ctx context.Context, fn func(pgx.Tx) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// decideMatch applies a decision immediately (administrators only).
func (s *server) decideMatch(ctx context.Context, a, b int64, matched bool, decidedBy string) error {
	return s.inTx(ctx, func(tx pgx.Tx) error { return applyDecision(ctx, tx, a, b, matched, decidedBy) })
}

func (s *server) unlink(ctx context.Context, keepID, removeID int64, decidedBy string) error {
	return s.inTx(ctx, func(tx pgx.Tx) error { return applyUnlink(ctx, tx, keepID, removeID, decidedBy) })
}

// errRateLimited means the guest has submitted too much, too quickly.
var errRateLimited = errors.New("rate limited")

// guestSubmission is what a guest form carries besides the product pair.
type guestSubmission struct {
	Note string
	Name string
	Spam bool // the honeypot field was filled in
}

func readGuestSubmission(r *http.Request) guestSubmission {
	clip := func(s string, n int) string {
		s = strings.TrimSpace(s)
		if len([]rune(s)) > n {
			s = string([]rune(s)[:n])
		}
		return s
	}
	return guestSubmission{
		Note: clip(r.FormValue("note"), maxNoteLen),
		Name: clip(r.FormValue("name"), maxNameLen),
		Spam: r.FormValue(honeypotField) != "",
	}
}

// submitContribution queues a guest proposal for moderation. It changes
// nothing in the catalogue. Proposals that are already true (a link between
// products that are already linked) or impossible are refused up front with
// a linkError so the guest gets an explanation.
func (s *server) submitContribution(w http.ResponseWriter, r *http.Request, kind string, a, b int64) error {
	sub := readGuestSubmission(r)
	if sub.Spam {
		return nil
	}
	ipHash := s.requests.IPHash(r)
	if !s.limiter.Allow(hex.EncodeToString(ipHash)) {
		return errRateLimited
	}
	if a == b {
		return linkError("pick two different products")
	}
	contributorID, err := s.requests.EnsureContributor(w, r, auth.ViewerFrom(r.Context()))
	if err != nil {
		return err
	}
	l, rt := orderPair(a, b)
	ctx := r.Context()

	return s.inTx(ctx, func(tx pgx.Tx) error {
		var real int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM retail_products WHERE id IN ($1, $2) AND NOT is_phantom`, l, rt).Scan(&real); err != nil {
			return err
		}
		if real != 2 {
			return linkError("one of these products doesn't exist")
		}
		linked, err := sameGroup(ctx, tx, l, rt)
		if err != nil {
			return err
		}
		switch {
		case kind == kindLink && linked:
			return linkError("these products are already linked")
		case kind == kindNotMatch && linked:
			return linkError("these products are linked; use \"this link is wrong\" on the product page instead")
		case kind == kindUnlink && !linked:
			return linkError("these products aren't linked")
		}
		var pending int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM contributions WHERE contributor_id = $1 AND status = 'pending'`, contributorID).Scan(&pending); err != nil {
			return err
		}
		if pending >= maxPendingPerGuest {
			return errRateLimited
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO contributions (kind, left_product_id, right_product_id, note, contributor_id, contributor_name, ip_hash)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (kind, left_product_id, right_product_id, contributor_id) WHERE status = 'pending'
			DO UPDATE SET note = EXCLUDED.note, contributor_name = EXCLUDED.contributor_name`,
			kind, l, rt, sub.Note, contributorID, sub.Name, ipHash)
		return err
	})
}

// Moderation review actions.
const (
	reviewAccept      = "accept"
	reviewRemoveLeft  = "remove_left"
	reviewRemoveRight = "remove_right"
	reviewReject      = "reject"
)

// reviewContribution resolves every pending proposal with the same
// (kind, pair) at once — several guests proposing the same thing are one
// decision. Accepting applies it exactly as an administrator's direct
// action would; if the catalogue changed since it was proposed and the
// action no longer makes sense, the linkError is returned and nothing is
// marked reviewed, so the administrator can still reject it.
func (s *server) reviewContribution(ctx context.Context, admin, kind string, l, r int64, action string) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id FROM contributions
			WHERE kind = $1 AND left_product_id = $2 AND right_product_id = $3 AND status = 'pending'
			ORDER BY id FOR UPDATE`, kind, l, r)
		if err != nil {
			return err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			return linkError("this proposal was already reviewed")
		}

		status := "accepted"
		decidedBy := fmt.Sprintf("admin:%s (%s:%d)", admin, contributionSubject, ids[0])
		switch {
		case action == reviewReject:
			status = "rejected"
		case kind == kindLink && action == reviewAccept:
			err = applyDecision(ctx, tx, l, r, true, decidedBy)
		case kind == kindNotMatch && action == reviewAccept:
			err = applyDecision(ctx, tx, l, r, false, decidedBy)
		case kind == kindUnlink && action == reviewRemoveLeft:
			err = applyUnlink(ctx, tx, r, l, decidedBy)
		case kind == kindUnlink && action == reviewRemoveRight:
			err = applyUnlink(ctx, tx, l, r, decidedBy)
		default:
			return fmt.Errorf("review action %q doesn't apply to a %q proposal", action, kind)
		}
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
			UPDATE contributions SET status = $1, reviewed_at = now(), reviewed_by = $2
			WHERE id = ANY($3)`, status, admin, ids)
		return err
	})
}

// pendingCount is the number of distinct pending proposals, for the nav.
func (s *server) pendingCount(ctx context.Context) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM (SELECT DISTINCT kind, left_product_id, right_product_id
		                      FROM contributions WHERE status = 'pending') x`).Scan(&n)
	return n, err
}

// moderationSide is one product on a moderation card.
type moderationSide struct {
	matchSide
	Pack    string
	GroupID *int64
}

type moderationItem struct {
	Kind        string
	Left, Right moderationSide
	Supporters  int
	Notes       []string
	Names       []string
	FirstAt     time.Time
	// PriorRejected is the most proposals any of this item's supporters has
	// had rejected before, a rough signal for spotting a bad-faith guest.
	PriorRejected int
	Error         string
}

func (m moderationItem) KindLabel() string {
	switch m.Kind {
	case kindLink:
		return "Link these"
	case kindNotMatch:
		return "Not the same product"
	default:
		return "Wrong link"
	}
}

func (s *server) loadModeration(ctx context.Context, kind string, limit int) ([]moderationItem, error) {
	rows, err := s.pool.Query(ctx, `
		WITH g AS (
		    SELECT kind, left_product_id l, right_product_id r, count(*) n, min(created_at) first_at,
		           array_remove(array_agg(NULLIF(note, '') ORDER BY created_at), NULL) notes,
		           array_remove(array_agg(DISTINCT NULLIF(contributor_name, '')), NULL) names,
		           array_agg(contributor_id) contributors
		    FROM contributions
		    WHERE status = 'pending' AND ($1 = '' OR kind = $1)
		    GROUP BY kind, left_product_id, right_product_id
		)
		SELECT g.kind, g.n, g.first_at, g.notes, g.names,
		       coalesce((SELECT max(c) FROM (
		           SELECT count(*) c FROM contributions x
		           WHERE x.contributor_id = ANY(g.contributors) AND x.status = 'rejected'
		           GROUP BY x.contributor_id) z), 0),
		       a.id, a.store, a.name, coalesce(a.brand,''), coalesce(a.image_url,''), a.url, coalesce(pa.current_price,0), pa.current_price IS NOT NULL,
		       coalesce(trim(to_char(a.pack_value, 'FM999999990.##')) || ' ' || a.pack_unit, ''), ma.product_group_id,
		       b.id, b.store, b.name, coalesce(b.brand,''), coalesce(b.image_url,''), b.url, coalesce(pb.current_price,0), pb.current_price IS NOT NULL,
		       coalesce(trim(to_char(b.pack_value, 'FM999999990.##')) || ' ' || b.pack_unit, ''), mb.product_group_id
		FROM g
		JOIN retail_products a ON a.id = g.l
		JOIN retail_products b ON b.id = g.r
		LEFT JOIN product_pricing_snapshot pa ON pa.retail_product_id = a.id
		LEFT JOIN product_pricing_snapshot pb ON pb.retail_product_id = b.id
		LEFT JOIN product_group_members ma ON ma.retail_product_id = a.id
		LEFT JOIN product_group_members mb ON mb.retail_product_id = b.id
		ORDER BY g.n DESC, g.first_at
		LIMIT $2`, kind, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []moderationItem
	for rows.Next() {
		var m moderationItem
		if err := rows.Scan(&m.Kind, &m.Supporters, &m.FirstAt, &m.Notes, &m.Names, &m.PriorRejected,
			&m.Left.ID, &m.Left.Store, &m.Left.Name, &m.Left.Brand, &m.Left.ImageURL, &m.Left.URL, &m.Left.Price, &m.Left.HasPrice, &m.Left.Pack, &m.Left.GroupID,
			&m.Right.ID, &m.Right.Store, &m.Right.Name, &m.Right.Brand, &m.Right.ImageURL, &m.Right.URL, &m.Right.Price, &m.Right.HasPrice, &m.Right.Pack, &m.Right.GroupID); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

const moderationLimit = 100

type moderationView struct {
	page
	Kind  string
	Items []moderationItem
}

func (s *server) handleModeration(w http.ResponseWriter, r *http.Request) {
	s.renderModeration(w, r, http.StatusOK, nil)
}

// renderModeration shows the queue; failed carries an inline error for the
// one card whose accept was refused.
func (s *server) renderModeration(w http.ResponseWriter, r *http.Request, status int, failed *moderationItem) {
	kind := r.FormValue("kind")
	if kind != kindLink && kind != kindNotMatch && kind != kindUnlink {
		kind = ""
	}
	items, err := s.loadModeration(r.Context(), kind, moderationLimit)
	if err != nil {
		httpError(w, err)
		return
	}
	if failed != nil {
		for i := range items {
			if items[i].Kind == failed.Kind && items[i].Left.ID == failed.Left.ID && items[i].Right.ID == failed.Right.ID {
				items[i].Error = failed.Error
			}
		}
	}
	p, err := s.newPage(r, "Moderation")
	if err != nil {
		httpError(w, err)
		return
	}
	s.render(w, status, moderationTmpl, moderationView{page: p, Kind: kind, Items: items})
}

func (s *server) handleModerationReview(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	l, err1 := strconv.ParseInt(r.FormValue("left"), 10, 64)
	rt, err2 := strconv.ParseInt(r.FormValue("right"), 10, 64)
	kind := r.FormValue("item_kind")
	if err1 != nil || err2 != nil {
		http.Error(w, "bad product ids", http.StatusBadRequest)
		return
	}
	v := auth.ViewerFrom(r.Context())
	err := s.reviewContribution(r.Context(), v.AdminName, kind, l, rt, r.FormValue("action"))
	var le linkError
	switch {
	case errors.As(err, &le):
		s.renderModeration(w, r, http.StatusConflict, &moderationItem{Kind: kind, Left: moderationSide{matchSide: matchSide{ID: l}}, Right: moderationSide{matchSide: matchSide{ID: rt}}, Error: string(le)})
		return
	case err != nil:
		httpError(w, fmt.Errorf("reviewing contribution: %w", err))
		return
	}
	target := "/admin/moderation"
	if k := r.FormValue("kind"); k != "" {
		target += "?kind=" + k
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// guestDone redirects a guest after a submission, or explains why it
// wasn't taken.
func (s *server) guestDone(w http.ResponseWriter, r *http.Request, err error, back string) {
	var le linkError
	switch {
	case err == nil:
		http.Redirect(w, r, withFlash(back, flashSent), http.StatusSeeOther)
	case errors.Is(err, errRateLimited):
		s.renderMessage(w, r, http.StatusTooManyRequests, "Slow down a little",
			"You've sent a lot of suggestions in a short time. Thank you! Please try again later.", back)
	case errors.As(err, &le):
		s.renderMessage(w, r, http.StatusConflict, "Couldn't send that", string(le), back)
	default:
		httpError(w, fmt.Errorf("recording contribution: %w", err))
	}
}
