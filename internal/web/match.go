package web

import (
	"context"
	"fmt"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type matchSide struct {
	ID       int64
	Store    string
	Name     string
	Brand    string
	ImageURL string
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
		       a.id, a.store, a.name, coalesce(a.brand,''), coalesce(a.image_url,''), coalesce(pa.current_price,0), pa.current_price IS NOT NULL,
		       b.id, b.store, b.name, coalesce(b.brand,''), coalesce(b.image_url,''), coalesce(pb.current_price,0), pb.current_price IS NOT NULL
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
			&c.Left.ID, &c.Left.Store, &c.Left.Name, &c.Left.Brand, &c.Left.ImageURL, &c.Left.Price, &c.Left.HasPrice,
			&c.Right.ID, &c.Right.Store, &c.Right.Name, &c.Right.Brand, &c.Right.ImageURL, &c.Right.Price, &c.Right.HasPrice); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// decideMatch records a human decision and, on confirm, links the two
// products into one product_group — extending an existing group if either
// side already belongs to one (a group holds at most one member per store,
// enforced by the UNIQUE on product_group_members.retail_product_id, so a
// conflicting confirm fails loudly rather than silently merging retailers).
func decideMatch(ctx context.Context, pool *pgxpool.Pool, leftID, rightID int64, matched bool) error {
	decision := "rejected"
	if matched {
		decision = "matched"
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `
		INSERT INTO match_decisions (left_product_id, right_product_id, decision)
		VALUES ($1, $2, $3)
		ON CONFLICT (left_product_id, right_product_id) DO UPDATE SET decision = EXCLUDED.decision, decided_at = now()`,
		leftID, rightID, decision); err != nil {
		return err
	}

	if matched {
		if err := linkIntoGroup(ctx, tx, leftID, rightID); err != nil {
			return err
		}
	}

	return tx.Commit(ctx)
}

func linkIntoGroup(ctx context.Context, tx pgx.Tx, leftID, rightID int64) error {
	var groupID int64
	var found bool
	err := tx.QueryRow(ctx, `SELECT product_group_id FROM product_group_members WHERE retail_product_id = $1`, leftID).Scan(&groupID)
	if err == nil {
		found = true
	} else if err != pgx.ErrNoRows {
		return err
	}
	if !found {
		err := tx.QueryRow(ctx, `SELECT product_group_id FROM product_group_members WHERE retail_product_id = $1`, rightID).Scan(&groupID)
		if err == nil {
			found = true
		} else if err != pgx.ErrNoRows {
			return err
		}
	}
	if !found {
		if err := tx.QueryRow(ctx, `INSERT INTO product_groups DEFAULT VALUES RETURNING id`).Scan(&groupID); err != nil {
			return err
		}
	}

	for _, pid := range []int64{leftID, rightID} {
		if _, err := tx.Exec(ctx, `
			INSERT INTO product_group_members (product_group_id, retail_product_id)
			VALUES ($1, $2) ON CONFLICT (retail_product_id) DO NOTHING`, groupID, pid); err != nil {
			return err
		}
	}
	return nil
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
		httpError(w, fmt.Errorf("recording decision: %w", err))
		return
	}
	http.Redirect(w, r, "/match", http.StatusSeeOther)
}
