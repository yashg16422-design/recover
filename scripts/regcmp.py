"""Helpers for scripts/regression.sh. Usage: regcmp.py old_fields_same <main.json> <other.json> | offline_equal <main.json> <other.json>"""
import json, sys

def load(p): return json.load(open(p))

def old_fields_same(main, other):
    a, b = load(main), load(other)
    keep = set(a["rows"][0].keys()) - {"graph_node_id"}
    ra = {r["charge_id"]: {k: r[k] for k in keep} for r in a["rows"]}
    rb = {r["charge_id"]: {k: r[k] for k in keep} for r in b["rows"]}
    return ra == rb and all(a["summary"][k] == b["summary"][k] for k in ("count", "at_risk_amount", "by_bucket", "currency", "threat_count"))

def offline_equal(main, other):
    a, b = load(main), load(other)
    return (b["summary"]["recoverable_amount"] == a["summary"]["recoverable_amount"]
            and b["summary"]["quarantined_total"] == 0
            and all(r["graph_risk"] is None and not r["quarantined"] for r in b["rows"]))

print(globals()[sys.argv[1]](*sys.argv[2:]))
