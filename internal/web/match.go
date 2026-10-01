package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"retail-prices/internal/normalize"
)

type matchSide struct {
	ID       int64
	Store    string
	Name     string
	Brand    string
	ImageURL string
	URL      string
	Price    float64
	HasPrice bool
}

type candidateRow struct {
	CandidateID int64
	Score       float64
	Left, Right matchSide
}

// loadCandidates returns undecided match_candidates ordered by score —
// human-confirmed decisions (match_decisions) are the only thing that ever
// resolves one; nothing here matches automatically.
func loadCandidates(ctx context.Context, pool *pgxpool.Pool, limit int) ([]candidateRow, error) {
	rows, err := pool.Query(ctx, `
		SELECT mc.id, mc.score,
		       a.id, a.store, a.name, coalesce(a.brand,''), coalesce(a.image_url,''), a.url, coalesce(pa.current_price,0), pa.current_price IS NOT NULL,
		       b.id, b.store, b.name, coalesce(b.brand,''), coalesce(b.image_url,''), b.url, coalesce(pb.current_price,0), pb.current_price IS NOT NULL
		FROM match_candidates mc
		JOIN retail_products a ON a.id = mc.left_product_id
		JOIN retail_products b ON b.id = mc.right_product_id
		LEFT JOIN product_pricing_snapshot pa ON pa.retail_product_id = a.id
		LEFT JOIN product_pricing_snapshot pb ON pb.retail_product_id = b.id
		WHERE NOT EXISTS (
			SELECT 1 FROM match_decisions d
			WHERE d.left_product_id = mc.left_product_id AND d.right_product_id = mc.right_product_id
		)
		ORDER BY mc.score DESC
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []candidateRow
	for rows.Next() {
		var c candidateRow
		if err := rows.Scan(&c.CandidateID, &c.Score,
			&c.Left.ID, &c.Left.Store, &c.Left.Name, &c.Left.Brand, &c.Left.ImageURL, &c.Left.URL, &c.Left.Price, &c.Left.HasPrice,
			&c.Right.ID, &c.Right.Store, &c.Right.Name, &c.Right.Brand, &c.Right.ImageURL, &c.Right.URL, &c.Right.Price, &c.Right.HasPrice); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// linkError is a user-correctable reason a link was refused (as opposed to a
// database failure), so handlers can show it instead of a bare 500.
type linkError string

func (e linkError) Error() string { return string(e) }

// decideMatch records a human decision and, on confirm, links the two
// products into one product_group — extending an existing group if either
// side already belongs to one. Ids are put in left<right order first, the
// convention match_decisions and match_candidates share, so a pair decided
// from either side (or manually) resolves the same queue row.
func decideMatch(ctx context.Context, pool *pgxpool.Pool, leftID, rightID int64, matched bool) error {
	if leftID == rightID {
		return linkError("a product can't be linked to itself")
	}
	if leftID > rightID {
		leftID, rightID = rightID, leftID
	}
	decision := "rejected"
	if matched {
		decision = "matched"
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if matched {
		if err := linkIntoGroup(ctx, tx, leftID, rightID); err != nil {
			return err
		}
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO match_decisions (left_product_id, right_product_id, decision)
		VALUES ($1, $2, $3)
		ON CONFLICT (left_product_id, right_product_id) DO UPDATE SET decision = EXCLUDED.decision, decided_at = now()`,
		leftID, rightID, decision); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// linkIntoGroup puts both products in one group, creating it or extending
// whichever group either already belongs to. It refuses (rather than
// silently skipping) the cases that would corrupt identity: the two already
// sit in different groups (merging groups is a separate, deliberate act),
// or the target group already has a different product from the same store.
func linkIntoGroup(ctx context.Context, tx pgx.Tx, leftID, rightID int64) error {
	groupOf := func(pid int64) (int64, bool, error) {
		var g int64
		err := tx.QueryRow(ctx, `SELECT product_group_id FROM product_group_members WHERE retail_product_id = $1`, pid).Scan(&g)
		if err == pgx.ErrNoRows {
			return 0, false, nil
		}
		return g, err == nil, err
	}
	leftGroup, leftIn, err := groupOf(leftID)
	if err != nil {
		return err
	}
	rightGroup, rightIn, err := groupOf(rightID)
	if err != nil {
		return err
	}

	var groupID int64
	switch {
	case leftIn && rightIn && leftGroup != rightGroup:
		return linkError("these products already belong to two different groups; unlink one first")
	case leftIn && rightIn:
		return nil
	case leftIn:
		groupID = leftGroup
	case rightIn:
		groupID = rightGroup
	default:
		if err := tx.QueryRow(ctx, `INSERT INTO product_groups DEFAULT VALUES RETURNING id`).Scan(&groupID); err != nil {
			return err
		}
	}

	for _, pid := range []int64{leftID, rightID} {
		if pid == leftID && leftIn || pid == rightID && rightIn {
			continue
		}
		var clash string
		err := tx.QueryRow(ctx, `
			SELECT other.name FROM product_group_members m
			JOIN retail_products other ON other.id = m.retail_product_id
			JOIN retail_products me ON me.id = $2
			WHERE m.product_group_id = $1 AND other.store = me.store`, groupID, pid).Scan(&clash)
		if err == nil {
			return linkError(fmt.Sprintf("the group already has a product from the same store (%s)", clash))
		} else if err != pgx.ErrNoRows {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO product_group_members (product_group_id, retail_product_id) VALUES ($1, $2)`, groupID, pid); err != nil {
			return err
		}
	}
	return nil
}

// linkOption is one search hit on the manual-link page.
type linkOption struct {
	ID       int64
	Store    string
	Name     string
	Brand    string
	URL      string
	ImageURL string
	Pack     string
	Price    float64
	HasPrice bool
	Grouped  bool
}

const linkOptionSelect = `
	SELECT rp.id, rp.store, rp.name, coalesce(rp.brand,''), rp.url, coalesce(rp.image_url,''),
	       coalesce(rp.pack_value::text || ' ' || coalesce(rp.pack_unit,''), ''),
	       coalesce(pps.current_price,0), pps.current_price IS NOT NULL,
	       EXISTS (SELECT 1 FROM product_group_members m WHERE m.retail_product_id = rp.id)
	FROM retail_products rp
	LEFT JOIN product_pricing_snapshot pps ON pps.retail_product_id = rp.id`

func scanLinkOptions(rows pgx.Rows) ([]linkOption, error) {
	defer rows.Close()
	var out []linkOption
	for rows.Next() {
		var o linkOption
		if err := rows.Scan(&o.ID, &o.Store, &o.Name, &o.Brand, &o.URL, &o.ImageURL, &o.Pack, &o.Price, &o.HasPrice, &o.Grouped); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func loadLinkOption(ctx context.Context, pool *pgxpool.Pool, id int64) (*linkOption, error) {
	rows, err := pool.Query(ctx, linkOptionSelect+` WHERE rp.id = $1`, id)
	if err != nil {
		return nil, err
	}
	opts, err := scanLinkOptions(rows)
	if err != nil || len(opts) == 0 {
		return nil, err
	}
	return &opts[0], nil
}

// searchLinkOptions finds non-phantom products by name (trigram similarity
// or plain substring, so short/partial queries still hit), optionally
// excluding one store — the other side of a link is always a different
// retailer.
func searchLinkOptions(ctx context.Context, pool *pgxpool.Pool, q, excludeStore string, limit int) ([]linkOption, error) {
	nq := normalize.NormalizeName(q)
	if nq == "" {
		return nil, nil
	}
	rows, err := pool.Query(ctx, linkOptionSelect+`
		WHERE NOT rp.is_phantom AND rp.store <> $2
		  AND (rp.name_norm % $1 OR rp.name_norm ILIKE '%' || $1 || '%')
		ORDER BY similarity(rp.name_norm, $1) DESC LIMIT $3`, nq, excludeStore, limit)
	if err != nil {
		return nil, err
	}
	return scanLinkOptions(rows)
}

type matchView struct {
	Title      string
	Candidates []candidateRow
}

func (s *server) handleMatchQueue(w http.ResponseWriter, r *http.Request) {
	candidates, err := loadCandidates(r.Context(), s.pool, 50)
	if err != nil {
		httpError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := matchTmpl.ExecuteTemplate(w, "layout", matchView{Title: "Linking queue", Candidates: candidates}); err != nil {
		httpError(w, err)
	}
}

func (s *server) handleMatchDecide(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	leftID, err1 := strconv.ParseInt(r.FormValue("left"), 10, 64)
	rightID, err2 := strconv.ParseInt(r.FormValue("right"), 10, 64)
	if err1 != nil || err2 != nil {
		http.Error(w, "bad product ids", http.StatusBadRequest)
		return
	}
	matched := r.FormValue("decision") == "matched"

	if err := decideMatch(r.Context(), s.pool, leftID, rightID, matched); err != nil {
		var le linkError
		if errors.As(err, &le) {
			http.Error(w, string(le), http.StatusConflict)
			return
		}
		httpError(w, fmt.Errorf("recording decision: %w", err))
		return
	}
	http.Redirect(w, r, "/match", http.StatusSeeOther)
}

// manualSide is one column of the manual-link page: the search box, the
// currently selected product (always listed first, so it survives
// re-searching), and the hits.
type manualSide struct {
	Field    string // form field name for the radio group: "a" or "b"
	Query    string
	Selected *linkOption
	List     []linkOption
	Searched bool
}

type manualView struct {
	Title string
	A, B  manualSide
	Error string
}

const manualResultLimit = 12

func (s *server) buildManualSide(ctx context.Context, field, q, selected, excludeStore string) (manualSide, error) {
	side := manualSide{Field: field, Query: q}
	if id, err := strconv.ParseInt(selected, 10, 64); err == nil {
		sel, err := loadLinkOption(ctx, s.pool, id)
		if err != nil {
			return side, err
		}
		side.Selected = sel
	}
	if q != "" {
		side.Searched = true
		hits, err := searchLinkOptions(ctx, s.pool, q, excludeStore, manualResultLimit)
		if err != nil {
			return side, err
		}
		for _, h := range hits {
			if side.Selected == nil || h.ID != side.Selected.ID {
				side.List = append(side.List, h)
			}
		}
	}
	if side.Selected != nil {
		side.List = append([]linkOption{*side.Selected}, side.List...)
	}
	return side, nil
}

// buildManualView assembles the manual-link page from the request's
// selections (a, b) and searches (qa, qb). Each side searches only other
// retailers than the opposite side's selection.
func (s *server) buildManualView(ctx context.Context, vals map[string]string) (manualView, error) {
	v := manualView{Title: "Link products manually"}
	var err error
	if v.A, err = s.buildManualSide(ctx, "a", vals["qa"], vals["a"], ""); err != nil {
		return v, err
	}
	if v.B, err = s.buildManualSide(ctx, "b", vals["qb"], vals["b"], ""); err != nil {
		return v, err
	}
	// Re-run each search excluding the other side's store now that both
	// selections are known.
	if v.B.Selected != nil && v.A.Searched {
		if v.A, err = s.buildManualSide(ctx, "a", vals["qa"], vals["a"], v.B.Selected.Store); err != nil {
			return v, err
		}
	}
	if v.A.Selected != nil && v.B.Searched {
		if v.B, err = s.buildManualSide(ctx, "b", vals["qb"], vals["b"], v.A.Selected.Store); err != nil {
			return v, err
		}
	}
	return v, nil
}

func (s *server) handleManualLink(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	vals := map[string]string{"a": q.Get("a"), "b": q.Get("b"), "qa": q.Get("qa"), "qb": q.Get("qb")}
	// Arriving from a product page (?a=ID): pre-search the other side by
	// that product's own name, the most likely thing to type anyway.
	if id, err := strconv.ParseInt(vals["a"], 10, 64); err == nil && vals["qb"] == "" && vals["b"] == "" {
		if p, perr := loadLinkOption(r.Context(), s.pool, id); perr == nil && p != nil {
			vals["qb"] = p.Name
		}
	}
	v, err := s.buildManualView(r.Context(), vals)
	if err != nil {
		httpError(w, err)
		return
	}
	s.renderManual(w, v, http.StatusOK)
}

func (s *server) handleManualLinkPost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	a, err1 := strconv.ParseInt(r.FormValue("a"), 10, 64)
	b, err2 := strconv.ParseInt(r.FormValue("b"), 10, 64)
	var err error
	if err1 != nil || err2 != nil {
		err = linkError("pick one product on each side first")
	} else if err = decideMatch(r.Context(), s.pool, a, b, true); err == nil {
		http.Redirect(w, r, "/products/"+strconv.FormatInt(a, 10), http.StatusSeeOther)
		return
	}
	var le linkError
	if !errors.As(err, &le) {
		httpError(w, fmt.Errorf("recording manual link: %w", err))
		return
	}
	v, verr := s.buildManualView(r.Context(), map[string]string{
		"a": r.FormValue("a"), "b": r.FormValue("b"), "qa": r.FormValue("qa"), "qb": r.FormValue("qb")})
	if verr != nil {
		httpError(w, verr)
		return
	}
	v.Error = string(le)
	s.renderManual(w, v, http.StatusConflict)
}

func (s *server) renderManual(w http.ResponseWriter, v manualView, status int) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := manualTmpl.ExecuteTemplate(w, "layout", v); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
	}
}
