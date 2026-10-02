"""Derive a yard conformance fixture from BDP's bd-backed fixture.

Only implementation-specific literals change: bd's link ids
(links/b64_<src>/b64_<tgt>/b64_<type>) become yard's link ids, bd's content
hashes become yard's revisions, and the demo-a property body / attribution
become yard's. Every membership, direction, readiness and ordering
expectation is bd's, re-sorted where ids changed. Capabilities yard does not
claim (external endpoints, content-derived revisions) are dropped.

usage: make-yard-fixture.py <bdpbd-fixture.json> <scope> <dial host:port> <out.json>
"""
import base64, copy, http.client, json, sys
from urllib.parse import urlsplit

src, scope, dial, out = sys.argv[1:5]
f = json.load(open(src))
host, port = dial.split(":")
parts = urlsplit(scope)


def get(path):
    c = http.client.HTTPConnection(host, int(port))
    c.request("GET", parts.path + path, headers={"Host": parts.netloc, "Accept": "application/json"})
    r = c.getresponse()
    body = r.read()
    if r.status != 200:
        sys.exit(f"GET {path}: {r.status} {body!r}")
    return json.loads(body)


def collect(path):
    items, url = [], path
    while url:
        page = get(url)
        items += page["items"]
        nxt = page["next"]
        url = nxt[len(scope):] if nxt else None
    return items


local = lambda absolute: absolute[len(scope):] if absolute.startswith(scope) else absolute
links = collect("links/?limit=200")
by_triple = {(local(l["source"]), local(l["target"]), l["type"].rsplit("/", 1)[1]): l for l in links}


def b64(seg):
    return base64.urlsafe_b64decode(seg[4:] + "=" * (-len(seg[4:]) % 4)).decode()


idmap, revmap, unmapped = {}, {}, set()
def map_bd_link(lid):
    if lid in idmap:
        return idmap[lid]
    segs = lid.split("/")
    if len(segs) != 4 or not all(s.startswith("b64_") for s in segs[1:]):
        return None
    triple = ("beads/" + b64(segs[1]), "beads/" + b64(segs[2]), b64(segs[3]))
    rec = by_triple.get(triple)
    if rec is None:
        # Only the external-endpoint oracle names such links, and yard does
        # not claim that capability.
        unmapped.add(lid)
        return None
    idmap[lid] = local(rec["id"])
    revmap[lid] = rec["revision"]
    return idmap[lid]


def canon(s):  # canonical-uri order is UTF-16 code unit order
    return s.encode("utf-16-be")


def rewrite(v):
    if isinstance(v, str):
        if v.startswith("links/b64_"):
            return map_bd_link(v) or v
        if v.startswith(scope + "links/b64_"):
            m = map_bd_link(v[len(scope):])
            return scope + m if m else v
        return v
    if isinstance(v, list):
        out = [rewrite(x) for x in v]
        # Lists of link ids (or [id] tuples) are in canonical order; re-sort
        # after the ids changed.
        key = None
        if out and all(isinstance(x, str) and x.startswith("links/") for x in out):
            key = canon
        elif out and all(isinstance(x, list) and x and isinstance(x[0], str) and x[0].startswith("links/") for x in out):
            key = lambda t: canon(t[0])
        return sorted(out, key=key) if key else out
    if isinstance(v, dict):
        return {k: rewrite(x) for k, x in v.items()}
    return v


y = copy.deepcopy(f)
y["realization"] = "yard"
y["capabilities"] = [c for c in f["capabilities"]
                     if c not in ("content-derived-revisions-v1", "controlled-read-external-endpoint-v1")]
y["bindings"] = rewrite(f["bindings"])
o = y["oracles"] = rewrite(f["oracles"])

# link-records tuples: [id, type, revision, source, sourcePin, target, targetPin, properties]
inv = {v: k for k, v in idmap.items()}
for t in o["collections"]["link-records"]:
    t[2] = revmap[inv[t[0]]]

a = get("beads/demo-a")
o["resources"]["bead.demo-a"]["properties"] = a["properties"]
link = get(o["resources"]["link.demo-b-a"]["id"])
o["attribution"] = {"bead": a["attribution"], "link": link["attribution"]}

json.dump(y, open(out, "w"), indent=2, ensure_ascii=False)
print(f"wrote {out}: {len(idmap)} link ids mapped, {len(unmapped)} left (external endpoints)")
print("capabilities:", y["capabilities"])
