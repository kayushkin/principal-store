# principal-store

`127.0.0.1:8314`. The registry of *principals* — the humans and groups a
permission can be granted to and a card can be assigned to.

Part of the store family alongside prediction-store `:8313`, quote-store
`:8309` and job-store `:8311`, and the same shape as all three: one record type
per table, ids handed out here and joined on everywhere else, routes rooted at
`/`.

## Why it exists

Nothing on this box models a person. The operator appears as `slava`, `kanban`
and `vlad` in three different stores with nothing joining them, and kanban has
no assignee concept at all. This store hands out the id — `principal_000001` —
that every other store joins on. Display names and emails ride alongside for
display; neither is unique and neither is ever a join key.

## Run it

```bash
./deploy.sh          # test, build, install the unit and the seed script, start, smoke-check
make check           # fmt, vet, test, build
```

The `sqlite_fts5` build tag is mandatory: a binary built without it starts and
then dies applying the schema with `no such module: fts5`. `Makefile` and
`deploy.sh` both carry it, and `Open()` names it if a binary from elsewhere
shows up.

Env: `PRINCIPAL_STORE_ADDR` (default `127.0.0.1:8314`), `PRINCIPAL_STORE_DATA_DIR`
(default `~/.config/principal-store`), and the resource owners `LLM_BRIDGE_URL`
(`:8160`), `SKILL_STORE_URL` (`:8301`), `TOOL_STORE_URL` (`:8302`). SQLite at
`<data dir>/principal-store.db`, WAL, foreign keys on.

**The bind is loopback on purpose.** This service has no auth; dash is the front
door that adds it at `/api/principals`. Older siblings bind `*`, this one does
not, and `deploy.sh` fails if it finds the port listening anywhere but
`127.0.0.1`.

## Use it

```bash
curl -s -X POST http://127.0.0.1:8314/principals \
  -H 'Content-Type: application/json' \
  -d '{"kind":"human","display_name":"Priya Raman","email":"priya.raman@northwind-eng.example"}'

curl -s -X POST http://127.0.0.1:8314/principals \
  -H 'Content-Type: application/json' -d '{"kind":"group","display_name":"Data Team"}'

curl -s -X PUT http://127.0.0.1:8314/principals/principal_000002/members/principal_000001   # 201, then 200
curl -s "http://127.0.0.1:8314/principals?q=pri"                                             # prefix search
curl -s http://127.0.0.1:8314/principals/principal_000001                                    # with its groups
curl -s -X POST http://127.0.0.1:8314/principals/principal_000001/disable                    # the only removal
```

`scripts/seed-sample-principals.sh` (installed as `~/bin/seed-sample-principals`)
creates the operator, the four Northwind demo humans already named in kanban's
`card_events.actor`, and the `Data Team` and `Security` groups. It is idempotent
and refuses to guess if two rows ever share a name.

`CONTRACT.md` is the route table and the field-by-field reference.

## Design notes

**There is no hard delete.** A principal that vanished would leave dangling
assignee ids in kanban and dangling grantees in permission-store, so
`disabled_at` is the only removal: the row stays, drops out of default listings,
and `GET /principals/{id}` still answers so a card assigned to a departed human
still renders.

**Kind is fixed at creation and memberships are one level deep.** A group holds
humans and nothing else in v1 — no nested groups — because every consumer that
expands a group can then do it with one query and no cycle check. `PATCH` with
`kind` is a 400 saying so.

**Each principal carries a resource list, and it is a list, not a lock.**
`PUT /principals/{id}/resources/{type}/{id}` records an agent, harness instance,
machine, skill or tool a person or group works with, by the owner's id. The owner
is asked first, so the list never holds an id nobody hands out. A human inherits
the list of every group they belong to. Nothing enforces it: a card's dispatch
picker reads it to put an assignee's instances first. Permission grants stay in
permission-store.

**Search is prefix-by-default.** `?q=pri` finds Priya Raman, because the main
caller is an assignee picker reading keystrokes. Anything that already carries
FTS5 syntax — a quoted phrase, `*`, a column prefix — is passed through as
written, which is what the seed script's exact-name lookup relies on.

## License

MIT — see [LICENSE](LICENSE).
