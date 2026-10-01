package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
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
// silently skipping) when the two already sit in different groups — merging
// groups is a separate, deliberate act. A group may hold several products
// from the same store: retailers sometimes relist the same item under a new
// SKU, often running both listings side by side for a while.
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
// or plain substring, so short/partial queries still hit), from every store
// — the same store included, for relisted SKUs.
func searchLinkOptions(ctx context.Context, pool *pgxpool.Pool, q string, limit int) ([]linkOption, error) {
	nq := normalize.NormalizeName(q)
	if nq == "" {
		return nil, nil
	}
	rows, err := pool.Query(ctx, linkOptionSelect+`
		WHERE NOT rp.is_phantom
		  AND (rp.name_norm % $1 OR rp.name_norm ILIKE '%' || $1 || '%')
		ORDER BY similarity(rp.name_norm, $1) DESC LIMIT $2`, nq, limit)
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

func (s *server) buildManualSide(ctx context.Context, field, q, selected string) (manualSide, error) {
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
		hits, err := searchLinkOptions(ctx, s.pool, q, manualResultLimit)
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
// selections (a, b) and searches (qa, qb).
func (s *server) buildManualView(ctx context.Context, vals map[string]string) (manualView, error) {
	v := manualView{Title: "Link products manually"}
	var err error
	if v.A, err = s.buildManualSide(ctx, "a", vals["qa"], vals["a"]); err != nil {
		return v, err
	}
	v.B, err = s.buildManualSide(ctx, "b", vals["qb"], vals["b"])
	return v, err
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

// bulkRow is one pair on the bulk review page.
type bulkRow struct {
	Score       float64
	Left, Right matchSide
	Pack        string
}

// bulkPriceTolerance is how far apart (as a ratio) the two sides' prices
// may be for a pair to be bulk-eligible. Regular prices are compared where
// known, so one retailer being mid-promo doesn't disqualify a pair.
const bulkPriceTolerance = 1.3

// loadBulkCandidates returns undecided candidates at or above minScore that
// are safe to confirm in bulk, i.e. every one of:
//   - pack size known on both sides and equal (names often omit the size, and
//     both early high-score rejections were same-name, different-size pairs);
//   - prices within bulkPriceTolerance of each other;
//   - neither product appears in another undecided pair at this score (those
//     need a real choice, so they stay in the one-at-a-time queue);
//   - neither product's existing group already holds a listing from the other
//     side's store.
func loadBulkCandidates(ctx context.Context, pool *pgxpool.Pool, minScore float64, limit int) ([]bulkRow, error) {
	rows, err := pool.Query(ctx, `
		WITH pending AS (
		    SELECT mc.left_product_id l, mc.right_product_id r, mc.score
		    FROM match_candidates mc
		    WHERE mc.score >= $1
		      AND NOT EXISTS (SELECT 1 FROM match_decisions d
		                      WHERE d.left_product_id = mc.left_product_id AND d.right_product_id = mc.right_product_id)
		),
		fanout AS (
		    SELECT pid, count(*) n FROM (SELECT l pid FROM pending UNION ALL SELECT r FROM pending) x GROUP BY pid
		)
		SELECT p.score,
		       a.id, a.store, a.name, coalesce(a.brand,''), coalesce(a.image_url,''), a.url, coalesce(pa.current_price,0), pa.current_price IS NOT NULL,
		       b.id, b.store, b.name, coalesce(b.brand,''), coalesce(b.image_url,''), b.url, coalesce(pb.current_price,0), pb.current_price IS NOT NULL,
		       trim(to_char(a.pack_value, 'FM999999990.##')) || ' ' || a.pack_unit
		FROM pending p
		JOIN retail_products a ON a.id = p.l
		JOIN retail_products b ON b.id = p.r
		JOIN fanout fa ON fa.pid = a.id
		JOIN fanout fb ON fb.pid = b.id
		JOIN product_pricing_snapshot pa ON pa.retail_product_id = a.id
		JOIN product_pricing_snapshot pb ON pb.retail_product_id = b.id
		WHERE fa.n = 1 AND fb.n = 1
		  AND a.pack_value IS NOT NULL AND b.pack_value IS NOT NULL AND a.pack_unit = b.pack_unit
		  AND abs(a.pack_value - b.pack_value) <= 0.005 * greatest(a.pack_value, b.pack_value)
		  AND coalesce(pa.regular_price, pa.current_price) > 0 AND coalesce(pb.regular_price, pb.current_price) > 0
		  AND greatest(coalesce(pa.regular_price, pa.current_price), coalesce(pb.regular_price, pb.current_price))
		      <= $2 * least(coalesce(pa.regular_price, pa.current_price), coalesce(pb.regular_price, pb.current_price))
		  AND NOT EXISTS (
		      SELECT 1 FROM product_group_members me
		      JOIN product_group_members o ON o.product_group_id = me.product_group_id AND o.retail_product_id <> me.retail_product_id
		      JOIN retail_products orp ON orp.id = o.retail_product_id
		      WHERE (me.retail_product_id = a.id AND orp.store = b.store)
		         OR (me.retail_product_id = b.id AND orp.store = a.store))
		ORDER BY p.score DESC, p.l, p.r
		LIMIT $3`, minScore, bulkPriceTolerance, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []bulkRow
	for rows.Next() {
		var c bulkRow
		if err := rows.Scan(&c.Score,
			&c.Left.ID, &c.Left.Store, &c.Left.Name, &c.Left.Brand, &c.Left.ImageURL, &c.Left.URL, &c.Left.Price, &c.Left.HasPrice,
			&c.Right.ID, &c.Right.Store, &c.Right.Name, &c.Right.Brand, &c.Right.ImageURL, &c.Right.URL, &c.Right.Price, &c.Right.HasPrice,
			&c.Pack); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

const (
	bulkDefaultMin  = 0.9
	bulkDefaultSize = 100
	bulkMaxSize     = 500
)

type bulkView struct {
	Title    string
	Min      float64
	Size     int
	Rows     []bulkRow
	Matched  int
	Rejected int
	Failed   int
	Posted   bool
}

func (s *server) handleBulkReview(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	v := bulkView{Title: "Bulk review", Min: bulkDefaultMin, Size: bulkDefaultSize}
	if f, err := strconv.ParseFloat(q.Get("min"), 64); err == nil && f > 0 && f <= 1 {
		v.Min = f
	}
	if n, err := strconv.Atoi(q.Get("size")); err == nil && n > 0 {
		v.Size = min(n, bulkMaxSize)
	}
	if q.Has("matched") {
		v.Posted = true
		v.Matched, _ = strconv.Atoi(q.Get("matched"))
		v.Rejected, _ = strconv.Atoi(q.Get("rejected"))
		v.Failed, _ = strconv.Atoi(q.Get("failed"))
	}
	rows, err := loadBulkCandidates(r.Context(), s.pool, v.Min, v.Size)
	if err != nil {
		httpError(w, err)
		return
	}
	v.Rows = rows
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := bulkTmpl.ExecuteTemplate(w, "layout", v); err != nil {
		httpError(w, err)
	}
}

// handleBulkDecide records one decision per pair shown on the page: ticked
// pairs ("ok") as matched, unticked ones as rejected. Each pair is its own
// transaction, so one refused link (e.g. the products ended up in different
// groups) doesn't undo the rest of the page; it is counted as failed and the
// pair stays undecided.
func (s *server) handleBulkDecide(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	ok := make(map[string]bool)
	for _, p := range r.Form["ok"] {
		ok[p] = true
	}
	var matched, rejected, failed int
	for _, p := range r.Form["pair"] {
		var l, rt int64
		if _, err := fmt.Sscanf(p, "%d-%d", &l, &rt); err != nil {
			http.Error(w, "bad pair "+p, http.StatusBadRequest)
			return
		}
		err := decideMatch(r.Context(), s.pool, l, rt, ok[p])
		var le linkError
		switch {
		case errors.As(err, &le):
			failed++
		case err != nil:
			httpError(w, fmt.Errorf("recording bulk decision %s: %w", p, err))
			return
		case ok[p]:
			matched++
		default:
			rejected++
		}
	}
	back := url.Values{
		"min": {r.FormValue("min")}, "size": {r.FormValue("size")},
		"matched": {strconv.Itoa(matched)}, "rejected": {strconv.Itoa(rejected)}, "failed": {strconv.Itoa(failed)},
	}
	http.Redirect(w, r, "/match/bulk?"+back.Encode(), http.StatusSeeOther)
}
