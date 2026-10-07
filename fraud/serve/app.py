"""Fraud score lookup service. FastAPI + Mangum + numpy only (no torch).

The GCN is trained offline (fraud/train.py). This service just looks up the
precomputed risk of a node and walks its neighbour list, so it is cheap enough
to run as an AWS Lambda behind API Gateway AND locally under uvicorn:

    uvicorn app:app --port 8100          # local
    handler = Mangum(app)                # AWS Lambda entry point

Artifacts are loaded ONCE per process/container (at import), from a local folder
by default or from S3 when ARTIFACT_BUCKET (+ optional ARTIFACT_PREFIX) is set.

Dataset: Elliptic Bitcoin transactions, not card payments. node_id is the row
index of the transaction in the PyG dataset (0..N-1).
"""
import json
import os
import random
from pathlib import Path
from typing import Literal

import numpy as np
from fastapi import Body, FastAPI, HTTPException, Query
from mangum import Mangum

ARTIFACT_FILES = ["scores.npy", "scores_lr.npy", "labels.npy", "split.npy", "neighbors.npz", "results.json"]
LABEL_NAMES = {-1: None, 0: "licit", 1: "illicit"}
SPLIT_NAMES = {0: "unlabeled", 1: "train", 2: "test"}


def _artifact_dir() -> tuple[Path, str]:
    bucket = os.environ.get("ARTIFACT_BUCKET")
    if not bucket:
        default = Path(__file__).resolve().parent.parent / "artifacts"
        return Path(os.environ.get("ARTIFACT_DIR", default)), "local"
    import boto3  # imported lazily: only needed on the S3 path (Lambda already ships boto3)

    prefix = os.environ.get("ARTIFACT_PREFIX", "").strip("/")
    dest = Path(os.environ.get("ARTIFACT_CACHE", "/tmp/artifacts"))
    dest.mkdir(parents=True, exist_ok=True)
    s3 = boto3.client("s3")
    for name in ARTIFACT_FILES:
        key = f"{prefix}/{name}" if prefix else name
        s3.download_file(bucket, key, str(dest / name))
    return dest, "s3"


class Store:
    """Everything the endpoints need, loaded once."""

    def __init__(self):
        d, self.source = _artifact_dir()
        self.scores = np.load(d / "scores.npy")
        self.scores_lr = np.load(d / "scores_lr.npy")
        self.labels = np.load(d / "labels.npy")
        self.split = np.load(d / "split.npy")
        nb = np.load(d / "neighbors.npz")
        self.indptr, self.indices = nb["indptr"], nb["indices"]
        self.results = json.loads((d / "results.json").read_text())
        self.n = len(self.scores)
        # demo samples come from the TEST split only, so the score shown is out-of-sample
        test = self.split == 2
        self.sample_ids = {
            "illicit": np.where(test & (self.labels == 1))[0],
            "licit": np.where(test & (self.labels == 0))[0],
        }
        # risk histogram on the TEST split (out-of-sample), 20 bins of width 0.05, by known label
        edges = np.linspace(0.0, 1.0, 21)
        self.distribution = {
            "bin_edges": [round(float(e), 2) for e in edges],
            "licit": np.histogram(self.scores[self.sample_ids["licit"]], bins=edges)[0].tolist(),
            "illicit": np.histogram(self.scores[self.sample_ids["illicit"]], bins=edges)[0].tolist(),
            "split": "test (time steps 35-49)",
        }

    def neighbors(self, i: int) -> np.ndarray:
        return self.indices[self.indptr[i]:self.indptr[i + 1]]

    def node(self, i: int, **extra) -> dict:
        return {
            "node_id": int(i),
            "risk": round(float(self.scores[i]), 6),
            "label": LABEL_NAMES[int(self.labels[i])],
            **extra,
        }


store = Store()  # cold start: runs once per container
app = FastAPI(title="Graph fraud score service", version="1.0")
_mangum = Mangum(app, lifespan="off")


def handler(event, context):
    """Lambda entry point. An EventBridge schedule (the keep-warm ping) is not an HTTP event, which Mangum
    rejects; answer it directly. Importing this module already loaded the artifacts, so the ping keeps a
    warm container with the data in memory."""
    if isinstance(event, dict) and event.get("source") == "aws.events":
        return {"warm": True, "nodes": store.n}
    return _mangum(event, context)


def _check(node_id: int) -> None:
    if not 0 <= node_id < store.n:
        raise HTTPException(status_code=404, detail=f"node_id must be in 0..{store.n - 1}")


@app.get("/health")
def health():
    return {"status": "ok", "nodes": store.n, "artifact_source": store.source}


@app.get("/score/{node_id}")
def score(node_id: int, hops: int = Query(1, ge=1, le=2)):
    _check(node_id)
    nbrs = np.unique(store.neighbors(node_id))
    nbrs = nbrs[nbrs != node_id]
    order = nbrs[np.argsort(-store.scores[nbrs])] if len(nbrs) else nbrs
    top = [store.node(j, hop=1, via=node_id) for j in order[:6]]
    if hops == 2:
        seen = {node_id, *map(int, order)}
        for j in order[:3]:  # expand only the 3 riskiest 1-hop neighbours
            n2 = np.unique(store.neighbors(int(j)))
            n2 = np.array([k for k in n2 if int(k) not in seen], dtype=np.int64)
            for k in (n2[np.argsort(-store.scores[n2])][:2] if len(n2) else []):
                seen.add(int(k))
                top.append(store.node(int(k), hop=2, via=int(j)))
    out = store.node(
        node_id,
        label_if_known=LABEL_NAMES[int(store.labels[node_id])],
        split=SPLIT_NAMES[int(store.split[node_id])],
        in_sample=bool(store.split[node_id] == 1),
        baseline_risk=round(float(store.scores_lr[node_id]), 6),
        degree=int(len(nbrs)),
        top_neighbors=top,
    )
    out.pop("label")
    return out


@app.get("/sample")
def sample(kind: Literal["illicit", "licit"]):
    ids = store.sample_ids[kind]
    return {"node_id": int(random.choice(ids)), "kind": kind, "split": "test"}


@app.get("/metrics")
def metrics():
    return store.results


@app.post("/score_batch")
def score_batch(ids: list[int] = Body(..., embed=True, max_length=5000)):
    """Risk for many nodes in one call: {"ids":[1,2,3]} -> {"scores":{"1":0.01,...}}; unknown ids map to null."""
    return {"scores": {str(i): (round(float(store.scores[i]), 6) if 0 <= i < store.n else None) for i in ids}}


@app.get("/distribution")
def distribution():
    """Histogram of GCN risk on the test split, split by known label (for the dashboard chart)."""
    return store.distribution
