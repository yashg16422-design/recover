"""Adds a `graph_node_id` column to the sample CSVs (idempotent; re-run any time).

Honest labeling: a card payment has no node in the Elliptic BITCOIN graph, so these links are a
DEMO. The draw is seeded (SEED below) and STRATIFIED so the graph signal is visible in the demo:
  sample_failed_payments.csv : 3 illicit + 9 licit test-period nodes
  sample_with_attack.csv     : card-testing burst rows -> licit nodes; the 5 other rows -> 1 illicit + 4 licit
Real illicit prevalence in the test period is far lower (see fraud/artifacts/results.json). Whatever the
graph model scores these nodes is reported as-is; nothing is chosen by score.

Run:  fraud/.venv-serve/bin/python scripts/make_graph_samples.py
"""
import csv, json, random
from pathlib import Path
import numpy as np

SEED = 7
ROOT = Path(__file__).resolve().parent.parent
art = ROOT / "fraud" / "artifacts"
labels, split = np.load(art / "labels.npy"), np.load(art / "split.npy")
illicit = np.where((split == 2) & (labels == 1))[0].tolist()
licit = np.where((split == 2) & (labels == 0))[0].tolist()
rng = random.Random(SEED)

def draw(kinds):
    out = []
    for k in kinds:
        out.append(rng.choice(illicit if k == "illicit" else licit))
    return out

def rewrite(path, kinds_for):
    rows = list(csv.reader(open(path, newline="")))
    head, body = rows[0], rows[1:]
    if "graph_node_id" in head:
        i = head.index("graph_node_id"); head.pop(i); [r.pop(i) for r in body]
    kinds = kinds_for(head, body)
    ids = draw(kinds)
    head.append("graph_node_id")
    for r, n in zip(body, ids):
        r.append(str(n))
    with open(path, "w", newline="") as f:
        csv.writer(f, lineterminator="\n").writerows([head] + body)
    return list(zip([r[0] for r in body], kinds, ids))

def plain(head, body):  # 3 illicit + 9 licit, shuffled
    k = ["illicit"] * 3 + ["licit"] * (len(body) - 3)
    rng.shuffle(k)
    return k

def with_attack(head, body):
    # the burst is detected by Recover's own detector; mirror its signature here (10 tiny rows in one window)
    ts = [r[head.index("created_at")] for r in body]
    amt = [float(r[head.index("amount")]) for r in body]
    burst = [a <= 20000 for a in amt]  # tiny-value rows are the burst
    normal_idx = [i for i, b in enumerate(burst) if not b]
    k = ["licit"] * len(body)
    k[rng.choice(normal_idx)] = "illicit"
    return k

for rel, fn in (("web/sample_failed_payments.csv", plain), ("web/sample_with_attack.csv", with_attack)):
    res = rewrite(ROOT / rel, fn)
    print(rel, res)
# keep the root copy identical to the served copy
(ROOT / "sample_failed_payments.csv").write_text((ROOT / "web/sample_failed_payments.csv").read_text())
