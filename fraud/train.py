"""Offline training: a 2-layer GCN vs a non-graph baseline on the Elliptic Bitcoin graph.

Run once on a laptop (needs torch + torch_geometric, see requirements-train.txt).
It exports everything the serving app needs into fraud/artifacts/ so that serving
never imports torch:

    scores.npy       GCN risk P(illicit) for every node       float32 [N]
    scores_lr.npy    baseline (logistic regression) risk       float32 [N]
    labels.npy       -1 unknown, 0 licit, 1 illicit            int8    [N]
    split.npy        0 unlabeled, 1 train (t<=34), 2 test      int8    [N]
    neighbors.npz    undirected CSR neighbour list (indptr, indices)
    results.json     every measured number (the single source of truth)

Honest scope: Elliptic is a *Bitcoin* transaction graph, not card payments.
No number in this repo is typed by hand; they all come from results.json.
"""
import json
import platform
import sys
import time
from pathlib import Path

import numpy as np
import sklearn
import torch
import torch.nn.functional as F
import torch_geometric
import torch_geometric.transforms as T
from sklearn.linear_model import LogisticRegression
from sklearn.metrics import average_precision_score, f1_score, precision_score, recall_score, roc_auc_score
from sklearn.preprocessing import StandardScaler
from torch_geometric.datasets import EllipticBitcoinDataset
from torch_geometric.nn import GCNConv

HERE = Path(__file__).parent
ART = HERE / "artifacts"
MODEL_DIR = HERE / "model"

SEEDS = [42, 43, 44, 45, 46]  # SEEDS[0] is the "primary" run that gets exported
EPOCHS = 200
HIDDEN = 64
LR = 0.01
WEIGHT_DECAY = 5e-4
DROPOUT = 0.5
N_LOCAL = 93  # first 93 of the 165 features are per-transaction; the last 72 are neighbour aggregates
THRESHOLDS = [0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7, 0.8, 0.9]


class GCN(torch.nn.Module):
    """2 GCNConv layers = every node sees its 2-hop neighbourhood."""

    def __init__(self, n_in: int):
        super().__init__()
        self.conv1 = GCNConv(n_in, HIDDEN)
        self.conv2 = GCNConv(HIDDEN, 2)

    def forward(self, x, edge_index):
        x = F.dropout(x, DROPOUT, self.training)
        x = F.relu(self.conv1(x, edge_index))
        x = F.dropout(x, DROPOUT, self.training)
        return self.conv2(x, edge_index)


def metrics(y_true, p, thr=0.5):
    pred = (p >= thr).astype(int)
    return {
        "roc_auc": float(roc_auc_score(y_true, p)),
        "pr_auc": float(average_precision_score(y_true, p)),
        "f1_illicit": float(f1_score(y_true, pred, zero_division=0)),
        "precision_illicit": float(precision_score(y_true, pred, zero_division=0)),
        "recall_illicit": float(recall_score(y_true, pred, zero_division=0)),
        "f1_threshold": thr,
    }


def threshold_table(y_true, p):
    rows = []
    for t in THRESHOLDS:
        pred = p >= t
        rows.append({
            "threshold": t,
            "precision_illicit": float(precision_score(y_true, pred, zero_division=0)),
            "recall_illicit": float(recall_score(y_true, pred, zero_division=0)),
            "flagged": int(pred.sum()),
        })
    return rows


def train_gcn(data, cols, seed):
    torch.manual_seed(seed)
    np.random.seed(seed)
    x = data.x[:, cols]
    y = data.y
    # class weights from the TRAIN labels only (illicit is the rare class)
    counts = torch.bincount(y[data.train_mask], minlength=2).float()
    weights = counts.sum() / (2.0 * counts)
    model = GCN(x.shape[1])
    opt = torch.optim.Adam(model.parameters(), lr=LR, weight_decay=WEIGHT_DECAY)
    t0 = time.time()
    for _ in range(EPOCHS):
        model.train()
        opt.zero_grad()
        out = model(x, data.edge_index)
        loss = F.cross_entropy(out[data.train_mask], y[data.train_mask], weight=weights)
        loss.backward()
        opt.step()
    train_s = time.time() - t0
    model.eval()
    with torch.no_grad():
        prob = F.softmax(model(x, data.edge_index), dim=1)[:, 1].numpy()
    return model, prob, float(loss.item()), train_s, [float(w) for w in weights]


def train_lr(x_np, y_np, train_idx, seed):
    t0 = time.time()
    clf = LogisticRegression(class_weight="balanced", max_iter=2000, random_state=seed)
    clf.fit(x_np[train_idx], y_np[train_idx])
    return clf, clf.predict_proba(x_np)[:, 1], time.time() - t0


def mean_std(runs, key):
    v = np.array([r[key] for r in runs])
    return {"mean": float(v.mean()), "std": float(v.std())}


def main():
    ds =EllipticBitcoinDataset(root=str(HERE / "data" / "elliptic"))
    raw = ds[0]
    n = raw.num_nodes
    train_mask, test_mask = raw.train_mask, raw.test_mask  # dataset's own time split: steps 1-34 / 35-49
    y_all = raw.y.numpy()
    train_idx = np.where(train_mask.numpy())[0]
    test_idx = np.where(test_mask.numpy())[0]
    y_test = y_all[test_idx]

    # standardise with TRAIN statistics only, then make the graph undirected
    # (money-flow edges are directed; "neighbour" should mean either direction)
    scaler = StandardScaler().fit(raw.x[train_mask].numpy())
    x_scaled = torch.tensor(scaler.transform(raw.x.numpy()), dtype=torch.float32)
    data = raw.clone()
    data.x = x_scaled
    data = T.ToUndirected()(data)
    assert raw.x.shape[1] == 165, "expected 93 local + 72 aggregated features"

    feature_sets = {"all165": list(range(165)), "local93": list(range(N_LOCAL))}
    results = {"models": {}}
    export = {}

    for fs_name, cols in feature_sets.items():
        # --- baseline: logistic regression, no graph ---
        clf, p_lr, lr_s = train_lr(x_scaled.numpy()[:, cols], y_all, train_idx, SEEDS[0])
        m_lr = metrics(y_test, p_lr[test_idx])
        results["models"][f"logreg_{fs_name}"] = {
            "test": m_lr, "train_seconds": lr_s, "features": len(cols),
        }
        print(f"logreg_{fs_name}: {m_lr}")

        # --- GCN, several seeds ---
        runs = []
        for seed in SEEDS:
            model, p_gcn, final_loss, train_s, w = train_gcn(data, cols, seed)
            m = metrics(y_test, p_gcn[test_idx])
            m.update({"seed": seed, "train_seconds": train_s, "final_train_loss": final_loss})
            runs.append(m)
            print(f"gcn_{fs_name} seed={seed}: {m}")
            if seed == SEEDS[0]:
                primary = (model, p_gcn, w)
        results["models"][f"gcn_{fs_name}"] = {
            "runs": runs,
            "summary": {k: mean_std(runs, k) for k in ("roc_auc", "pr_auc", "f1_illicit", "precision_illicit", "recall_illicit")},
            "features": len(cols), "class_weights_licit_illicit": primary[2],
        }
        if fs_name == "all165":
            export = {"gcn": primary[1], "lr": p_lr, "model": primary[0]}
            results["threshold_table_test"] = {
                "gcn_all165_seed%d" % SEEDS[0]: threshold_table(y_test, primary[1][test_idx]),
                "logreg_all165": threshold_table(y_test, p_lr[test_idx]),
            }

    # ---------- export serving artifacts ----------
    ART.mkdir(exist_ok=True)
    MODEL_DIR.mkdir(exist_ok=True)
    torch.save(export["model"].state_dict(), MODEL_DIR / f"gcn_all165_seed{SEEDS[0]}.pt")

    labels = np.where(y_all == 2, -1, y_all).astype(np.int8)
    split = np.zeros(n, dtype=np.int8)
    split[train_idx] = 1
    split[test_idx] = 2
    np.save(ART / "scores.npy", export["gcn"].astype(np.float32))
    np.save(ART / "scores_lr.npy", export["lr"].astype(np.float32))
    np.save(ART / "labels.npy", labels)
    np.save(ART / "split.npy", split)

    ei = data.edge_index.numpy()  # undirected, coalesced => sorted by source
    order = np.argsort(ei[0], kind="stable")
    src, dst = ei[0][order], ei[1][order]
    indptr = np.zeros(n + 1, dtype=np.int64)
    np.cumsum(np.bincount(src, minlength=n), out=indptr[1:])
    np.savez_compressed(ART / "neighbors.npz", indptr=indptr.astype(np.int64), indices=dst.astype(np.int32))

    results["dataset"] = {
        "name": "EllipticBitcoinDataset (Bitcoin transactions, NOT card payments)",
        "nodes": int(n),
        "edges_directed_raw": int(raw.edge_index.shape[1]),
        "edges_undirected": int(ei.shape[1] // 2),
        "features": 165,
        "licit": int((y_all == 0).sum()),
        "illicit": int((y_all == 1).sum()),
        "unlabeled": int((y_all == 2).sum()),
        "train_labeled": int(len(train_idx)),
        "test_labeled": int(len(test_idx)),
        "train_illicit": int((y_all[train_idx] == 1).sum()),
        "test_illicit": int((y_all[test_idx] == 1).sum()),
        "split": "dataset's own time split: train = time steps 1-34, test = 35-49 (no shuffling)",
    }
    results["config"] = {
        "seeds": SEEDS, "primary_seed": SEEDS[0], "epochs": EPOCHS, "hidden": HIDDEN,
        "lr": LR, "weight_decay": WEIGHT_DECAY, "dropout": DROPOUT, "layers": 2,
        "device": "cpu", "f1_threshold": 0.5,
        "scaling": "StandardScaler fit on train nodes only",
        "class_imbalance": "class-weighted cross-entropy (GCN) / class_weight=balanced (logreg)",
    }
    results["env"] = {
        "python": platform.python_version(), "torch": torch.__version__,
        "torch_geometric": torch_geometric.__version__, "sklearn": sklearn.__version__,
        "numpy": np.__version__, "machine": platform.machine(),
    }
    results["notes"] = [
        "Of the 165 features, 93 are per-transaction and 72 are pre-aggregated neighbour statistics, "
        "so logreg_all165 already contains some graph information. logreg_local93 vs gcn_local93 is the cleaner graph-vs-no-graph comparison.",
        "Scores for train-split nodes are in-sample. Only test-split scores are out-of-sample.",
        "Threshold tables are computed on the test split, so any threshold chosen from them is optimistic.",
    ]
    (ART / "results.json").write_text(json.dumps(results, indent=2))
    print("wrote", ART)


if __name__ == "__main__":
    sys.exit(main())
