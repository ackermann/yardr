"""Derive a yard conformance fixture from BDP's bd-backed fixture.

Only implementation-specific literals change: bd's link ids
(links/b64_<src>/b64_<tgt>/b64_<type>) become yard's link ids, bd's content
hashes become yard's revisions, and the demo-a property body / revision
provenance become yard's. Every membership, direction, readiness and ordering
expectation is bd's, re-sorted where ids changed. Capabilities yard does not
claim (external endpoints, content-derived revisions) are dropped. The type
inventory is bd's plus the Types yard always has for links to outside issues
(ISSUE_TYPES).

demo-a's properties and the revision provenance are pinned here, not read from the
server under test, so the suite catches a change in what yard serves. Only
values minted at run time are taken from the server, each after its shape is
checked: link ids and revisions (opaque, looked up by source, target and
type), demo-a's uuid and its timestamps.

usage: make-yard-fixture.py <bdpbd-fixture.json> <scope> <dial host:port> <out.json>
"""
import base64, copy, http.client, json, re, sys
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

UUID = re.compile(r"^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$")
INSTANT = re.compile(r"^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(\.\d+)?Z$")
REVISION = re.compile(r"^[0-9A-HJKMNP-TV-Z]{26}$")  # a ULID


def minted(value, shape, what):
    """A run-time value from the server, refused unless it has its shape."""
    if not (isinstance(value, str) and shape.match(value)):
        sys.exit(f"{what}: {value!r} is not the expected shape")
    return value


for t in o["collections"]["link-records"]:
    minted(t[2], REVISION, f"revision of {t[0]}")

# demo-a as yard serves it: bd's row without bd's owner (yard has none),
# plus yard's own fields.
a = get("beads/demo-a")
served = a["properties"]
want = {k: v for k, v in f["oracles"]["resources"]["bead.demo-a"]["properties"].items() if k != "owner"}
want.update({"uuid": minted(served.get("uuid"), UUID, "demo-a uuid"),
             "stage": "new", "labels": [], "hold": False,
             "created_at": minted(served.get("created_at"), INSTANT, "demo-a created_at"),
             "updated_at": minted(served.get("updated_at"), INSTANT, "demo-a updated_at")})
deps = served.get("dependencies") or []
if len(deps) != len(want["dependencies"]):
    sys.exit(f"demo-a dependencies: served {deps!r}, want {want['dependencies']!r}")
want["dependencies"] = [dict(d, created_at=minted(s.get("created_at"), INSTANT, "demo-a dependency created_at"))
                        for d, s in zip(want["dependencies"], deps)]
o["resources"]["bead.demo-a"]["properties"] = want
# yard's inventory always has the Types of links to outside issues, linked to
# or not (docs/bdp.md): discovery names the tracks-<source> Types in its
# maximums. Pinned here like demo-a's row, so a Type that appears or goes is
# caught. There is a tracks-<source> per source the yard can link to. A fresh
# yard has the built-in github source only; Linear is no longer built in, and
# run.sh does not declare a Linear source, so there is no tracks-linear. They
# sort after bd's types/related and before the reference Types.
ISSUE_TYPES = ["types/relates", "types/tracks", "types/tracks-github"]
o["collections"]["type-ids"] += ISSUE_TYPES
at = o["ordering"]["types"].index(["types/related"]) + 1
o["ordering"]["types"][at:at] = [[t] for t in ISSUE_TYPES]

# seed.py acts as bdp-conformance; yard carries the actor of the last change
# as the writer gave it.
actor = {"actor": {"basis": "writer-supplied", "id": "bdp-conformance"}}
o["revisionProvenance"] = {"bead": actor, "link": actor}

json.dump(y, open(out, "w"), indent=2, ensure_ascii=False)
print(f"wrote {out}: {len(idmap)} link ids mapped, {len(unmapped)} left (external endpoints)")
print("capabilities:", y["capabilities"])
