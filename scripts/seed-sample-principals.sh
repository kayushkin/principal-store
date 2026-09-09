#!/usr/bin/env bash
# seed-sample-principals: create the operator and the sample humans and groups
# the other demo data on this box already refers to, so principals line up with
# names that exist in kanban's card_events.actor.
#
# Idempotent. Each principal is looked up by an exact display_name and kind
# before it is created; a second run finds them all and creates nothing. If two
# rows ever match one lookup the script exits non-zero instead of guessing,
# because a guess here would seed memberships onto the wrong row.
#
# Prints one line per principal, created or found:
#   principal_000001  human  Vlad Kayushkin
set -euo pipefail

STORE="${PRINCIPAL_STORE_URL:-http://127.0.0.1:8314}"

if ! curl -sfS "$STORE/health" >/dev/null; then
  echo "seed-sample-principals: cannot reach principal-store at $STORE" >&2
  exit 1
fi

SEED_STORE="$STORE" python3 <<'PYTHON'
import json
import os
import sys
import urllib.error
import urllib.parse
import urllib.request

store = os.environ["SEED_STORE"]

# Vlad is the operator and is real. The other four are the Northwind demo team
# already present in kanban's card_events.actor, so a card seeded there can be
# assigned to a principal that exists.
humans = [
    ("Vlad Kayushkin", "slava@kayushkin.com"),
    ("Priya Raman", "priya.raman@northwind-eng.example"),
    ("Marcus Feld", "marcus.feld@northwind-eng.example"),
    ("Dinesh Okonkwo", "dinesh.okonkwo@northwind-eng.example"),
    ("Helena Vos", "helena.vos@northwind-eng.example"),
]
groups = ["Data Team", "Security"]
memberships = {
    "Data Team": ["Priya Raman", "Dinesh Okonkwo", "Vlad Kayushkin"],
    "Security": ["Helena Vos", "Marcus Feld", "Vlad Kayushkin"],
}


def request(method, path, body=None):
    data = None if body is None else json.dumps(body).encode()
    req = urllib.request.Request(store + path, data=data, method=method)
    if data is not None:
        req.add_header("Content-Type", "application/json")
    try:
        with urllib.request.urlopen(req) as resp:
            raw = resp.read()
            return resp.status, (json.loads(raw) if raw else None)
    except urllib.error.HTTPError as err:
        raw = err.read().decode(errors="replace")
        print("seed-sample-principals: %s %s -> %d %s" % (method, path, err.code, raw), file=sys.stderr)
        sys.exit(1)


def find_or_create(kind, display_name, email=""):
    # An exact phrase query, quoted so the store passes it through as FTS5
    # syntax rather than turning each word into a prefix term. A phrase can
    # still over-match ("Data Team" also finds "Data Team Leads"), so the
    # result is narrowed to the exact display_name here.
    query = urllib.parse.urlencode({
        "q": '"%s"' % display_name.replace('"', '""'),
        "kind": kind,
        "include_disabled": "true",
    })
    _, rows = request("GET", "/principals?" + query)
    exact = [r for r in rows if r["display_name"] == display_name]
    if len(exact) > 1:
        print(
            "seed-sample-principals: %d %s rows are named %r (%s) — refusing to guess"
            % (len(exact), kind, display_name, ", ".join(r["id"] for r in exact)),
            file=sys.stderr,
        )
        sys.exit(1)
    if exact:
        return exact[0]
    status, created = request("POST", "/principals", {
        "kind": kind, "display_name": display_name, "email": email,
    })
    if status != 201:
        print("seed-sample-principals: POST /principals answered %d" % status, file=sys.stderr)
        sys.exit(1)
    return created


by_name = {}
for name, email in humans:
    by_name[name] = find_or_create("human", name, email)
for name in groups:
    by_name[name] = find_or_create("group", name)

for group, members in memberships.items():
    for member in members:
        status, _ = request(
            "PUT", "/principals/%s/members/%s" % (by_name[group]["id"], by_name[member]["id"]))
        if status not in (200, 201):
            print("seed-sample-principals: PUT member answered %d" % status, file=sys.stderr)
            sys.exit(1)

for name, _ in humans:
    p = by_name[name]
    print("%s  %s  %s" % (p["id"], p["kind"], p["display_name"]))
for name in groups:
    p = by_name[name]
    print("%s  %s  %s" % (p["id"], p["kind"], p["display_name"]))
PYTHON
