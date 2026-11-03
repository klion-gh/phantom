"""How far apart does Phantom look from a browser, and on what?

Reads flowlab recordings (one class per mode: direct = a browser's own HTTPS
connections, phantom = the tunnel carrying the same browsing), builds the
features DPI reportedly looks at, and trains a gradient-boosting classifier
on each of three views:

  start   - the first 16 KB of a connection (sizes, directions, gaps):
            what TSPU inspects right after the handshake;
  windows - 5-second slices of connections that live longer than that:
            rhythm, burstiness, up/down ratio in steady state;
  conn    - one row per whole connection: lifetime, volume, ratio.

For each view it reports ROC AUC under cross-validation (0.5 = the
classifier can't tell them apart, 1.0 = it always can) and the features that
carry the decision. A view at 0.95+ is one a DPI box with any statistics at
all can use.

    python tools/flowlab/analyze.py                      # out/direct.jsonl vs out/phantom.jsonl
    python tools/flowlab/analyze.py --phantom out/phantom-h2.jsonl --tag h2
"""

import argparse
import collections
import json
import math
import pathlib
import statistics

import numpy as np
import pandas as pd
from sklearn.ensemble import HistGradientBoostingClassifier
from sklearn.inspection import permutation_importance
from sklearn.metrics import roc_auc_score
from sklearn.model_selection import StratifiedGroupKFold

OUT = pathlib.Path(__file__).resolve().parent / "out"

# Edge's own services: present in direct mode as separate connections but
# hidden inside the tunnel in phantom mode, so they would skew the browser
# class without telling us anything about Phantom.
NOISE = ("microsoft.com", "msedge.net", "skype.com", "azureedge.net", "bing.com", "msn.com",
         "live.com", "clients2.google", "googleusercontent.com", "gvt1.com", "windows.com")

START_BYTES = 16 * 1024
START_EVENTS = 20
WINDOW_S = 5.0


def load(path, cls):
    conns = {}
    for line in open(path, encoding="utf-8"):
        e = json.loads(line)
        if e["ev"] == "open":
            conns[e["conn"]] = {"cls": cls, "label": e["label"], "target": e["target"], "io": [], "rec": [], "closed": None}
        elif e["conn"] in conns:
            if e["ev"] == "io":
                conns[e["conn"]]["io"].append((e["t"] / 1e6, 1 if e["dir"] == "up" else -1, e["n"]))
            elif e["ev"] == "rec":
                conns[e["conn"]]["rec"].append((e["t"] / 1e6, 1 if e["dir"] == "up" else -1, e["n"], e["type"]))
            else:
                conns[e["conn"]]["closed"] = e["t"] / 1e6
    out = []
    for cid, c in conns.items():
        # TLS records when the recording has them: their lengths are what DPI
        # reads off the wire, whereas "io" chunks depend on each program's
        # socket reads. Older recordings only have io.
        if c["rec"]:
            c["types"] = [r[3] for r in c["rec"]]
            c["io"] = [r[:3] for r in c["rec"]]
        if not c["io"]:
            continue
        if cls == 0 and any(n in c["target"] for n in NOISE) and "microsoft" not in c["label"]:
            continue
        c["id"] = cid
        out.append(c)
    return out


def sstats(xs):
    if not xs:
        return 0.0, 0.0
    return float(statistics.fmean(xs)), float(statistics.pstdev(xs)) if len(xs) > 1 else 0.0


def start_features(c):
    io, total, ev = [], 0, c["io"]
    for t, d, n in ev:
        io.append((t, d, n))
        total += n
        if total >= START_BYTES or len(io) >= 40:
            break
    f = {}
    for k in range(START_EVENTS):
        if k < len(io):
            t, d, n = io[k]
            f[f"size{k:02d}"] = d * math.log1p(n)
            f[f"gap{k:02d}"] = math.log1p((t - io[k - 1][0]) * 1000) if k else math.log1p(t * 1000)
        else:
            f[f"size{k:02d}"] = 0.0
            f[f"gap{k:02d}"] = -1.0
    up = [n for _, d, n in io if d > 0]
    dn = [n for _, d, n in io if d < 0]
    f["events_to_16k"] = len(io)
    f["ms_to_16k"] = math.log1p(io[-1][0] * 1000)
    f["up_bytes"] = math.log1p(sum(up))
    f["down_bytes"] = math.log1p(sum(dn))
    f["up_mean"], f["up_std"] = sstats(up)
    f["down_mean"], f["down_std"] = sstats(dn)
    f["direction_changes"] = sum(1 for a, b in zip(io, io[1:]) if a[1] != b[1])
    return f


def app_start(c):
    """The connection from its first application data on: the TLS handshake
    stripped, so Firefox-vs-Chromium ClientHello and certificate sizes don't
    decide the comparison. In TLS 1.3 the client's Finished is its first
    type-23 record; what follows is application data."""
    types = c.get("types")
    if not types:
        return None
    for i, (rec, typ) in enumerate(zip(c["io"], types)):
        if rec[1] > 0 and typ == 23:
            rest = c["io"][i + 1:]
            return {**c, "io": rest} if rest else None
    return None


def window_features(c):
    ev = c["io"]
    end = ev[-1][0]
    rows = []
    w = 0
    while (w + 1) * WINDOW_S <= end:
        lo, hi = w * WINDOW_S, (w + 1) * WINDOW_S
        seg = [x for x in ev if lo <= x[0] < hi]
        w += 1
        if not seg:
            continue
        up = [n for _, d, n in seg if d > 0]
        dn = [n for _, d, n in seg if d < 0]
        times = [lo] + [t for t, _, _ in seg] + [hi]
        gaps = [b - a for a, b in zip(times, times[1:])]
        f = {
            "age_s": math.log1p(lo),
            "up_bytes": math.log1p(sum(up)), "down_bytes": math.log1p(sum(dn)),
            "up_events": len(up), "down_events": len(dn),
            "ratio": math.log((sum(dn) + 1) / (sum(up) + 1)),
            "max_idle_ms": math.log1p(max(gaps) * 1000),
            "bursts": sum(1 for g in gaps if g > 0.2),
            "active_frac": sum(g for g in gaps if g <= 0.05) / WINDOW_S,
        }
        f["up_mean"], f["up_std"] = sstats(up)
        f["down_mean"], f["down_std"] = sstats(dn)
        rows.append(f)
    return rows


def conn_features(c):
    ev = c["io"]
    up = sum(n for _, d, n in ev if d > 0)
    dn = sum(n for _, d, n in ev if d < 0)
    dur = (c["closed"] or ev[-1][0])
    return {
        "duration_s": math.log1p(dur), "up_bytes": math.log1p(up), "down_bytes": math.log1p(dn),
        "ratio": math.log((dn + 1) / (up + 1)), "events": math.log1p(len(ev)),
        "events_per_s": math.log1p(len(ev) / max(dur, 0.001)),
    }


def univariate(df, k=6):
    """Each feature's own AUC, folded so 0.5 = useless and 1.0 = alone it
    separates the classes - what gives Phantom away, even when the combined
    classifier is perfect and permutation importance can't single anything out."""
    y = df["y"].to_numpy()
    out = []
    for col in df.columns:
        if col in ("y", "group"):
            continue
        x = df[col].to_numpy()
        if np.all(x == x[0]):
            continue
        a = roc_auc_score(y, x)
        out.append((col, round(float(max(a, 1 - a)), 3)))
    return sorted(out, key=lambda t: -t[1])[:k]


def evaluate(df, name):
    X = df.drop(columns=["y", "group"])
    y = df["y"].to_numpy()
    groups = df["group"].to_numpy()
    n_pos, n_neg = int(y.sum()), int(len(y) - y.sum())
    if n_pos < 5 or n_neg < 5:
        return {"view": name, "n_phantom": n_pos, "n_browser": n_neg, "auc": None, "top": []}
    folds = min(5, n_pos, n_neg)
    cv = StratifiedGroupKFold(n_splits=folds, shuffle=True, random_state=1)
    scores = np.zeros(len(y))
    for tr, te in cv.split(X, y, groups):
        m = HistGradientBoostingClassifier(max_iter=200, learning_rate=0.1, class_weight="balanced", random_state=1)
        m.fit(X.iloc[tr], y[tr])
        scores[te] = m.predict_proba(X.iloc[te])[:, 1]
    auc = roc_auc_score(y, scores)
    m = HistGradientBoostingClassifier(max_iter=200, learning_rate=0.1, class_weight="balanced", random_state=1).fit(X, y)
    imp = permutation_importance(m, X, y, scoring="roc_auc", n_repeats=5, random_state=1)
    order = np.argsort(imp.importances_mean)[::-1][:6]
    top = [(X.columns[i], round(float(imp.importances_mean[i]), 3)) for i in order if imp.importances_mean[i] > 0.001]
    return {"view": name, "n_phantom": n_pos, "n_browser": n_neg, "auc": round(float(auc), 3), "top": top,
            "alone": univariate(df)}


def throughput(tag):
    path = OUT / "throughput.jsonl"
    if not path.exists():
        return []
    rows = [json.loads(l) for l in open(path)]
    agg = collections.defaultdict(list)
    for r in rows:
        if r["mbit"] > 0:
            agg[(r["mode"], r.get("tag", ""), r["dir"])].append(r["mbit"])
    return [(k, round(statistics.median(v), 1), len(v)) for k, v in sorted(agg.items())]


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--direct", default=str(OUT / "direct.jsonl"))
    ap.add_argument("--phantom", default=str(OUT / "phantom.jsonl"))
    ap.add_argument("--tag", default="baseline")
    args = ap.parse_args()

    browser = [c for c in load(args.direct, 0) if not c["label"].startswith("bulk:")]
    tunnel = [c for c in load(args.phantom, 1) if not c["label"].startswith("bulk:")]
    conns = browser + tunnel

    start = pd.DataFrame([{**start_features(c), "y": c["cls"], "group": c["id"]} for c in conns])
    app = [a for a in (app_start(c) for c in conns) if a]
    start_app = pd.DataFrame([{**start_features(c), "y": c["cls"], "group": c["id"]} for c in app]) if app else None
    win_rows = []
    for c in conns:
        for f in window_features(c):
            win_rows.append({**f, "y": c["cls"], "group": c["id"]})
    windows = pd.DataFrame(win_rows)
    whole = pd.DataFrame([{**conn_features(c), "y": c["cls"], "group": c["id"]} for c in conns])

    results = [evaluate(start, "start (первые 16 КБ, с рукопожатием TLS)")]
    if start_app is not None:
        results.append(evaluate(start_app, "start-app (первые 16 КБ после рукопожатия)"))
    results += [evaluate(windows, "windows (окна по 5 с)"), evaluate(whole, "conn (соединение целиком)")]

    lines = [f"# flowlab: {args.tag}", "",
             f"Соединений браузера: {len(browser)}, туннеля Phantom: {len(tunnel)}.", "",
             "| Срез | Phantom | Браузер | AUC | Важны в модели | Различают в одиночку (AUC) |", "|---|---|---|---|---|---|"]
    for r in results:
        top = ", ".join(f"{n} ({v})" for n, v in r["top"]) or "—"
        alone = ", ".join(f"{n} ({v})" for n, v in r.get("alone", [])) or "—"
        lines.append(f"| {r['view']} | {r['n_phantom']} | {r['n_browser']} | {r['auc'] if r['auc'] is not None else 'мало данных'} | {top} | {alone} |")
    tp = throughput(args.tag)
    if tp:
        lines += ["", "| Режим | Метка | Направление | Мбит/с (медиана) | Замеров |", "|---|---|---|---|---|"]
        for (mode, tag, d), med, n in tp:
            lines.append(f"| {mode} | {tag or '—'} | {d} | {med} | {n} |")
    report = "\n".join(lines) + "\n"
    OUT.mkdir(exist_ok=True)
    (OUT / f"report-{args.tag}.md").write_text(report, encoding="utf-8")
    (OUT / f"report-{args.tag}.json").write_text(json.dumps(results, ensure_ascii=False, indent=1), encoding="utf-8")
    print(report)


if __name__ == "__main__":
    main()
