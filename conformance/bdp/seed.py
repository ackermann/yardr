"""Seed a yard with the graph BDP's bd conformance seed creates (seedBdWorkspace)."""
import json, os, subprocess, sys
fixture, yard = sys.argv[1], sys.argv[2]
f = json.load(open(fixture))
def y(*args):
    r = subprocess.run([yard, *args, "--json", "--actor", "bdp-conformance"], capture_output=True, text=True)
    if r.returncode: sys.exit(f"yard {' '.join(args)}: {r.stdout}{r.stderr}")
    return json.loads(r.stdout) if r.stdout.strip() else None
for b in f["beads"]:
    p = b["properties"]; bid = b["localId"].split("/", 1)[1]
    y("bead", "create", p["title"], "--id", bid, "--type", p.get("issue_type", "task"), "--priority", str(p.get("priority", 2)))
    if p["status"] == "closed": y("bead", "close", bid, "--reason", "fixture")
    elif p["status"] == "deferred": y("bead", "hold", bid)
skipped = []
for l in f["links"]:
    s, t = l["source"], l["target"]
    if not (isinstance(s, str) and isinstance(t, str) and s.startswith("beads/") and t.startswith("beads/")):
        skipped.append(l["localId"]); continue
    kind = l["type"].rsplit("/", 1)[1]
    # bd's conformance seed: reference "relates" is bd's "related"; no cites.
    if kind == "cites":
        skipped.append(l["localId"]); continue
    kind = {"relates": "related"}.get(kind, kind)
    # BDP: source depends on target. yard: dep add <blocker> <waiting>.
    y("dep", "add", t.split("/", 1)[1], s.split("/", 1)[1], "--kind", kind)
print("skipped links yard cannot express (external/pinned endpoints):", skipped)
