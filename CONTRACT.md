# principal-store — API contract

`127.0.0.1:8314`. The registry of principals: the humans and groups a permission
can be granted to and a card can be assigned to. This store hands out the id
everything else joins on.

Module `github.com/kayushkin/principal-store`, root package `principalstore`,
binary `cmd/principal-store` installed at `~/bin/principal-store`. Env:
`PRINCIPAL_STORE_ADDR` (default `127.0.0.1:8314`), `PRINCIPAL_STORE_DATA_DIR`
(default `~/.config/principal-store`). SQLite at
`<data dir>/principal-store.db`, WAL, `?_foreign_keys=on`.

Routes are rooted at `/`, the same as prediction-store, quote-store and
job-store; dash adds the `/api/principals` prefix and the auth this service has
none of. All timestamps are epoch seconds (`INTEGER`, 0 = unset).

**The bind is `127.0.0.1`, deliberately, and it is part of this contract.** The
older store siblings bind `*`; this one must not, because it has no auth of its
own and the front door (dash) is the only door. The unit sets
`PRINCIPAL_STORE_ADDR=127.0.0.1:8314`, `main.go` defaults to the same, and
`deploy.sh` fails if the port is listening anywhere else. Nothing off-host may
reach this service directly.

Build with `-tags sqlite_fts5`. The schema creates an FTS5 virtual table, so a
binary built without the tag starts and dies at the first boot with `no such
module: fts5` — `Open()` says so and names the flag.

---

## The two tables

### `principals`

| Field | Meaning |
|---|---|
| `id` | `principal_000001`. What every other store joins on |
| `seq` | monotonic, assigned here; generates `id` and is the FTS rowid |
| `kind` | `human` \| `group`. Fixed at creation; enforced in Go against `kind.go` |
| `display_name` | required. Display only — never a join key, and not unique |
| `email` | humans, free text, trimmed. **Not unique and never a join key** — two rows may share one |
| `disabled_at` | 0 = active. The only removal; see below |
| `created_at` / `updated_at` | |
| **`groups`** | **computed on read** by `GET /principals/{id}` for a human: the groups it belongs to. Always present for a human, `[]` when none |
| **`members`** | **computed on read** by `GET /principals/{id}` for a group: the humans in it. Always present for a group, `[]` when none |

Exactly one of `groups` / `members` is present, so a reader can tell the kind
from the shape as well as from `kind`. Neither is expanded by `GET /principals`.

### `group_members`

| Field | Meaning |
|---|---|
| `group_id` | a `kind=group` principal. A human here is a 400 |
| `member_id` | a `kind=human` principal. A group here is a 400: **no nested groups in v1** |
| `created_at` | |

`(group_id, member_id)` is the primary key and the insert is `INSERT OR IGNORE`,
so adding the same member twice is idempotent.

---

## Invariants

**`kind` is `human` or `group`.** Anything else is a **400** naming the
vocabulary. Case is normalised, nothing else is. `GET /kinds` serves the list so
no caller hardcodes it.

**Memberships are one level deep.** `group_id` must be a group and `member_id`
must be a human, checked in Go on every write and read of a membership because a
`CHECK` cannot look across rows. Putting a group in the member slot is a **400**
that says nested groups are not supported in v1 and to add the group's humans
directly.

**`display_name` is required** on every write path. The name is the only thing
a card or a permission can render, and a row that renders as nothing is
indistinguishable from a broken join.

**There is no hard delete.** No route and no store method removes a row.
`disabled_at` is the only removal: a disabled principal still exists so every
reference from another store keeps resolving. Disabled principals are hidden
from `GET /principals`, `GET /principals/{id}/members`, `GET /principals/{id}/groups`
and the embedded `groups` / `members` unless `include_disabled=true`, and
`GET /principals/{id}` **always** returns them, with `disabled_at` set, because a
card assigned to them still needs to render. `disable` and `enable` are
idempotent: a second `disable` does not move the timestamp.

**`kind` and `disabled_at` do not move through `PATCH`.** `kind` is fixed at
creation (a group that became a human would strand its memberships — create a
new principal). `disabled_at` moves only through `POST /disable` and
`POST /enable`, so removal is always an explicit act. Either key in a `PATCH`
body is a **400** saying so.

**Ids are prefixed, never bare UUIDs.** dash's resolver probes every registry
row whose id pattern matches, and noteboard already claims the uuid shape, so a
uuid-shaped id here would make every uuid in every chat message probe this store
as well. The prefix also keeps `/` out of the ref, which kanban-store's reverse
lookup splits on.

---

## Routes

| Method | Path | Notes |
|---|---|---|
| GET | `/health` | `{"status":"ok","counts":{principals,humans,groups,disabled}}`. Every count is over all rows, disabled included; `principals = humans + groups` |
| GET | `/kinds` | `["human","group"]` |
| GET | `/principals` | `q` (search, below), `kind`, `include_disabled`, `limit`, `offset` → a **bare array** `[Principal]`, display-name order, memberships not expanded. An unknown `kind` is a **400** naming the vocabulary |
| POST | `/principals` | `{kind, display_name, email?}` → **201** with the row |
| GET | `/principals/{id}` | the row plus `groups` (human) or `members` (group). Answers for a disabled principal. `include_disabled` governs whether disabled rows appear in the embedded list |
| PATCH | `/principals/{id}` | `display_name`, `email` only. `kind` and `disabled_at` are each a **400** naming what does move them; any other key is a **400** naming the two accepted |
| POST | `/principals/{id}/disable` | **200** with the row. Idempotent |
| POST | `/principals/{id}/enable` | **200** with the row. Idempotent |
| GET | `/principals/{id}/members` | group → `[Principal]` of humans. **400** if the id is a human. `include_disabled` as above |
| PUT | `/principals/{group}/members/{member}` | no body. **201** `{"group_id","member_id","created":true}` the first time, **200** with `"created":false` after. **400** if `{group}` is not a group or `{member}` is not a human; **404** if either is missing |
| DELETE | `/principals/{group}/members/{member}` | **204**. **404** if not a member, or if either id is missing |
| GET | `/principals/{id}/groups` | human → `[Principal]` of groups. **400** if the id is a group (a group has no groups: no nesting). `include_disabled` as above |

Errors are `{"error":"…"}` and enumerate the valid values, so an agent reading a
400 can retry without guessing. **400** the caller described the record wrongly
(an unknown kind, a blank `display_name`, the wrong kind on either side of a
membership, a nested group, a forbidden or unknown `PATCH` key, a malformed
search query) / **404** no such principal, or no such membership on `DELETE` /
**500** otherwise.

Request bodies are decoded with unknown fields rejected, so a misspelled key is
a 400 rather than a write that silently drops it. `PATCH` reaches the same
answer by a different route: it reads the body as a map so it can name the
forbidden keys, which defeats `DisallowUnknownFields`, so it then checks the
remaining keys against its own allowlist by hand. A `PATCH` of
`{"display_nmae":"…"}` is a 400, not a 200 that changed nothing.

---

## Search

`q` is matched against an FTS5 index over `display_name` and `email`, kept true
by triggers on insert, update and delete, keyed on `seq`.

**Bare words are prefix terms.** `q=pri` finds Priya Raman and `q=priya northwind`
finds her by name and email domain together, because the main caller is an
assignee picker reading keystrokes and FTS5's default whole-token match would
find her only once the whole name was typed. Anything that already carries FTS5
syntax — a double quote, `*`, `:`, parentheses, or an uppercase `AND` / `OR` /
`NOT` / `NEAR` — is passed through exactly as written. So `q="Vlad Kayushkin"`
(quotes included, URL-encoded) is an exact phrase match, which is what
`scripts/seed-sample-principals.sh` relies on; note that a phrase can still
over-match (`"Data Team"` also finds `Data Team Leads`), so an exact-name lookup
narrows the result by `display_name` on the caller's side.

The expression is probed before the listing query runs, so a malformed one is a
**400** naming the query rather than a 500 from inside the handler.

---

## Seed data

`scripts/seed-sample-principals.sh`, installed by `deploy.sh` as
`~/bin/seed-sample-principals`, creates:

- humans: **Vlad Kayushkin** (`slava@kayushkin.com` — the operator; real, not
  a sample), **Priya Raman**, **Marcus Feld**, **Dinesh Okonkwo**, **Helena Vos**
  (`@northwind-eng.example` — the names already in kanban's `card_events.actor`
  from the Northwind demo)
- groups: **Data Team**, **Security**
- memberships: Priya, Dinesh → Data Team; Helena, Marcus → Security; Vlad → both

It is idempotent — each principal is looked up by exact `display_name` and
`kind` before it is created — and exits non-zero if two rows ever match one
lookup rather than seeding memberships onto the wrong row. It prints one line per
principal, `principal_000001  human  Vlad Kayushkin`.
