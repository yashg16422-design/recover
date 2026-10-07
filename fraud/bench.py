"""Measure the fraud service's latency honestly and record it in artifacts/results.json.

Starts the real app under uvicorn (port 8101), sends 1000 SEQUENTIAL requests over a
keep-alive connection to localhost, and reports p50/p95/p99. This is local-machine
latency including HTTP overhead; it is NOT AWS Lambda latency (cold/warm Lambda
numbers need a real deployment and are not measured here).

Run with the serving venv:  make fraud-bench
"""
import json
import platform
import random
import subprocess
import sys
import time
from pathlib import Path

import httpx
import numpy as np

HERE = Path(__file__).parent
SERVE = HERE / "serve"
RESULTS = HERE / "artifacts" / "results.json"
PORT = 8101
N = 1000
WARMUP = 50


def pct(xs, q):
    return float(np.percentile(xs, q))


def run_requests(client, ids, path_fn):
    for i in ids[:WARMUP]:
        client.get(path_fn(i)).raise_for_status()
    lat = []
    for i in ids[WARMUP:WARMUP + N]:
        t = time.perf_counter()
        r = client.get(path_fn(i))
        lat.append((time.perf_counter() - t) * 1000)
        r.raise_for_status()
    return {
        "requests": len(lat), "p50_ms": pct(lat, 50), "p95_ms": pct(lat, 95),
        "p99_ms": pct(lat, 99), "mean_ms": float(np.mean(lat)), "max_ms": float(np.max(lat)),
    }


def main():
    # artifact load time = the part of a "cold start" that is ours (fresh process, import app)
    t = subprocess.run(
        [sys.executable, "-c", "import time; t=time.perf_counter(); import app; print(time.perf_counter()-t)"],
        cwd=SERVE, capture_output=True, text=True, check=True)
    load_s = float(t.stdout.strip().splitlines()[-1])

    srv = subprocess.Popen(
        [sys.executable, "-m", "uvicorn", "app:app", "--port", str(PORT), "--log-level", "warning"], cwd=SERVE)
    base = f"http://127.0.0.1:{PORT}"
    try:
        with httpx.Client(base_url=base, timeout=5) as c:
            for _ in range(100):
                try:
                    n = c.get("/health").json()["nodes"]
                    break
                except Exception:
                    time.sleep(0.1)
            else:
                raise SystemExit("service did not start")
            rng = random.Random(0)
            ids = [rng.randrange(n) for _ in range(WARMUP + N)]
            out = {
                "score_1hop": run_requests(c, ids, lambda i: f"/score/{i}"),
                "score_2hop": run_requests(c, ids, lambda i: f"/score/{i}?hops=2"),
            }
    finally:
        srv.terminate()
        srv.wait(timeout=10)

    out["artifact_load_seconds_local"] = load_s
    out["setup"] = (f"uvicorn on localhost, 1 sequential client, keep-alive, {WARMUP} warmup + {N} timed requests, "
                    f"random node ids (seed 0); {platform.machine()} {platform.system()}; NOT AWS Lambda")
    res = json.loads(RESULTS.read_text())
    res["serving_latency"] = out
    RESULTS.write_text(json.dumps(res, indent=2))
    print(json.dumps(out, indent=2))


if __name__ == "__main__":
    main()
