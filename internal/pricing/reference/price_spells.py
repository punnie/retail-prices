"""
Spell-based regular price / promo classifier for high-low retail pricing.

Input : DataFrame with columns product_id, date, price (one row per product-day, gaps allowed)
Output: spells  - one row per constant-price run, labelled regular / promo / pending / short_high / unknown
        changes - one row per regular price change (date, old, new, pct)
        daily   - daily series with regular_price, promo flag, depth, rolling trend metrics
"""
import numpy as np
import pandas as pd

MIN_REG_DAYS = 3     # a high must last this long to count as a regular price (kills glitches)
MAX_PROMO_DAYS = 56  # a low lasting longer than this is a permanent change, not a promo
TREND_WINDOW = 84    # rolling window in days; use a multiple of the promo cycle


def fill_daily(df):
    """One row per product-day; missing days carry the last seen price."""
    df = df.assign(date=pd.to_datetime(df["date"])).sort_values(["product_id", "date"])
    out = []
    for pid, g in df.groupby("product_id"):
        g = g.drop_duplicates("date", keep="last").set_index("date")
        g = g.reindex(pd.date_range(g.index.min(), g.index.max(), name="date"))
        out.append(g.assign(product_id=pid, price=g["price"].ffill()).reset_index())
    return pd.concat(out, ignore_index=True)[["product_id", "date", "price"]]


def build_spells(daily):
    """Run-length encode each product's series into constant-price spells."""
    d = daily.copy()
    changed = d["price"].ne(d.groupby("product_id")["price"].shift())
    d["spell_id"] = changed.groupby(d["product_id"]).cumsum()
    spells = (d.groupby(["product_id", "spell_id"])
                .agg(price=("price", "first"), start=("date", "min"),
                     end=("date", "max"), days=("date", "size"))
                .reset_index())
    return d, spells


def drop_glitches(daily):
    """Blank out short spikes (a 1-2 day high above both neighbours) so they don't
    split a regular spell in two. Short lows are left alone: they may be real promos."""
    d, sp = build_spells(daily)
    g = sp.groupby("product_id")["price"]
    glitch = (sp["days"] < MIN_REG_DAYS) & (sp["price"] > g.shift()) & (sp["price"] > g.shift(-1))
    d = d.merge(sp.loc[glitch, ["product_id", "spell_id"]].assign(glitch=True),
                on=["product_id", "spell_id"], how="left")
    d["price"] = d["price"].mask(d["glitch"].notna()).groupby(d["product_id"]).ffill()
    return d[["product_id", "date", "price"]]


def _carry(spells):
    """Regular price carried forward from the last regular spell."""
    return (spells["price"].where(spells["is_reg"])
            .groupby(spells["product_id"]).ffill())


def classify(spells):
    s = spells.copy()
    g = s.groupby("product_id")
    prev_p, next_p = g["price"].shift(), g["price"].shift(-1)

    # Pass 1: a regular spell is a local high that persists, or any spell too long to be a promo
    local_high = (prev_p.isna() | (s["price"] > prev_p)) & (next_p.isna() | (s["price"] > next_p))
    s["is_reg"] = (local_high & (s["days"] >= MIN_REG_DAYS)) | (s["days"] > MAX_PROMO_DAYS)

    # Pass 2: V-filter on the regular candidates. A lower high sandwiched between two
    # equal-or-higher highs within MAX_PROMO_DAYS is a shallow promo (3.00 > 2.00 > 2.50 > 2.00 > 3.00)
    c = s[s["is_reg"]]
    cg = c.groupby("product_id")
    gap = (cg["start"].shift(-1) - cg["end"].shift()).dt.days - 1
    dip = ((c["price"] < cg["price"].shift()) & (cg["price"].shift(-1) >= cg["price"].shift())
           & (gap <= MAX_PROMO_DAYS) & (c["days"] <= MAX_PROMO_DAYS))
    s.loc[dip[dip].index, "is_reg"] = False

    # Pass 3: staircase increases (2.00 > 2.20 > 2.50 with no promo between).
    # A persistent spell at or above the carried regular price can't be a promo.
    s["is_reg"] |= (~s["is_reg"]) & (s["price"] >= _carry(s)) & (s["days"] >= MIN_REG_DAYS)

    # Spells before the first regular spell stay NaN / "unknown": a series that opens
    # low could be mid-promo or mid-staircase, and the data can't tell you which.
    s["regular_price"] = _carry(s)

    is_last = g["spell_id"].transform("max").eq(s["spell_id"])
    s["label"] = np.select(
        [s["is_reg"],
         s["regular_price"].isna(),
         is_last & (s["price"] < s["regular_price"]),   # too recent to call
         s["price"] < s["regular_price"]],
        ["regular", "unknown", "pending", "promo"], default="short_high")
    return s


def regular_changes(spells):
    r = spells[spells["is_reg"]].copy()
    r["old_price"] = r.groupby("product_id")["price"].shift()
    r = r[r["old_price"].notna() & r["price"].ne(r["old_price"])]
    r["pct_change"] = r["price"] / r["old_price"] - 1
    r["direction"] = np.where(r["pct_change"] > 0, "increase", "permanent_decrease")
    return r[["product_id", "start", "old_price", "price", "pct_change", "direction"]] \
        .rename(columns={"start": "change_date", "price": "new_price"})


def daily_metrics(daily_with_ids, spells):
    d = daily_with_ids.merge(spells[["product_id", "spell_id", "regular_price", "label"]],
                             on=["product_id", "spell_id"])
    d["on_promo"] = d["label"].isin(["promo", "pending"]).astype(float)
    d["depth"] = (1 - d["price"] / d["regular_price"]).where(d["on_promo"] == 1)
    roll = lambda col, fn: (d.groupby("product_id")[col]
                             .transform(lambda x: getattr(x.rolling(TREND_WINDOW, min_periods=28), fn)()))
    d["avg_paid_price"] = roll("price", "mean")     # the cycle-averaged trend line
    d["promo_share"] = roll("on_promo", "mean")     # share of days on promo
    d["avg_promo_depth"] = roll("depth", "mean")    # mean discount when on promo
    return d


def run(df):
    daily, spells = build_spells(drop_glitches(fill_daily(df)))
    spells = classify(spells)
    return spells, regular_changes(spells), daily_metrics(daily, spells)


def price_vs(daily, since):
    """Has the price moved since a date? Compares regular and average paid price."""
    since = pd.Timestamp(since)
    then = daily[daily["date"] <= since].groupby("product_id").last()
    now = daily.groupby("product_id").last()
    out = pd.DataFrame({
        "regular_then": then["regular_price"], "regular_now": now["regular_price"],
        "avg_paid_then": then["avg_paid_price"], "avg_paid_now": now["avg_paid_price"],
        "current_status": now["label"]})
    out["regular_pct"] = out["regular_now"] / out["regular_then"] - 1
    out["avg_paid_pct"] = out["avg_paid_now"] / out["avg_paid_then"] - 1
    return out


if __name__ == "__main__":
    # Synthetic demo: A = high-low with a hidden increase and a shallow-promo dip,
    # B = permanent cut, C = staircase increase ending on a live promo, D = opens mid-promo, has a 1-day glitch
    def series(pid, runs, start="2026-01-01"):
        prices = [p for p, n in runs for _ in range(n)]
        return pd.DataFrame({"product_id": pid, "price": prices,
                             "date": pd.date_range(start, periods=len(prices))})
    demo = pd.concat([
        series("A", [(3.0, 7), (2.0, 14), (3.0, 5), (2.0, 14), (2.5, 5), (2.0, 10),
                     (3.0, 6), (2.0, 14), (3.3, 7), (2.2, 14), (3.3, 5)]),
        series("B", [(4.0, 10), (3.0, 14), (4.0, 7), (3.5, 70)]),
        series("C", [(2.0, 10), (1.5, 7), (2.0, 5), (2.2, 10), (2.5, 10), (1.8, 6)]),
        series("D", [(1.5, 6), (2.0, 4), (2.6, 1), (2.0, 9), (1.5, 20)]),
    ]).drop(index=[3])  # knock out some days to test gaps
    spells, changes, daily = run(demo)
    print(spells[["product_id", "price", "start", "days", "label", "regular_price"]].to_string(index=False))
    print(); print(changes.to_string(index=False))
    print(); print(price_vs(daily, "2026-02-01").round(3).to_string())
