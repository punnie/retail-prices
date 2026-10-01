"""
Generates random high-low price series, runs them through the reference
price_spells.py classifier, and dumps both the raw (product_id, date, price)
rows and the resulting spells as JSON fixtures for internal/pricing's Go
parity test (see PRICING_CONCEPTS.md §5: "Keep a parity test between the
reference SQL and Python classifiers ... whenever parameters or logic
change"). Re-run this and re-check in the JSON whenever price_spells.py or
the Go port in internal/pricing changes.

Usage: python3 gen_parity.py > ../testdata/parity_fixtures.json
"""
import json
import random
import sys

import pandas as pd

sys.path.insert(0, ".")
import price_spells as ps


def gen_series(rng, pid, n_days=500):
    """A random walk through regular/promo/staircase phases, with occasional
    one-day glitches, loosely modelling a real high-low retailer series."""
    rows = []
    date0 = pd.Timestamp("2025-01-01")
    day = 0
    regular = round(rng.uniform(1.0, 20.0), 2)
    while day < n_days:
        phase = rng.choices(["regular", "promo", "staircase", "glitch"], weights=[5, 4, 1, 1])[0]
        if phase == "regular":
            dur = rng.randint(5, 45)
            price = regular
        elif phase == "promo":
            dur = rng.randint(2, 25)
            depth = rng.uniform(0.1, 0.45)
            price = round(regular * (1 - depth), 2)
        elif phase == "staircase":
            dur = rng.randint(5, 45)
            regular = round(regular * rng.uniform(0.85, 1.25), 2)
            price = regular
        else:  # glitch: a 1-day spike
            dur = 1
            price = round(regular * rng.uniform(1.2, 2.0), 2)
        for d in range(dur):
            if day >= n_days:
                break
            # occasional missing day, to exercise gap-filling
            if rng.random() > 0.05:
                rows.append({"product_id": pid, "date": (date0 + pd.Timedelta(days=day)).isoformat(), "price": price})
            day += 1
    return rows


def main():
    rng = random.Random(20260301)
    all_rows = []
    for i in range(15):
        all_rows.extend(gen_series(rng, f"P{i:02d}", n_days=300))

    df = pd.DataFrame(all_rows)
    spells, changes, _daily = ps.run(df)

    out_spells = []
    for _, r in spells.iterrows():
        out_spells.append({
            "product_id": r["product_id"],
            "price": float(r["price"]),
            "start": r["start"].date().isoformat(),
            "days": int(r["days"]),
            "label": r["label"],
            "regular_price": None if pd.isna(r["regular_price"]) else float(r["regular_price"]),
        })

    out_changes = []
    for _, r in changes.iterrows():
        out_changes.append({
            "product_id": r["product_id"],
            "change_date": r["change_date"].date().isoformat(),
            "old_price": float(r["old_price"]),
            "new_price": float(r["new_price"]),
            "direction": r["direction"],
        })

    print(json.dumps({"rows": all_rows, "spells": out_spells, "changes": out_changes}, indent=1))


if __name__ == "__main__":
    main()
