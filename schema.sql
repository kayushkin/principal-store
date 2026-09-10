-- principal-store schema. Create-only and idempotent: Open() executes this
-- whole file on every boot. New columns go in the ensureColumns loop in
-- store.go, never in an edit to a CREATE TABLE below — an edited CREATE TABLE
-- is a no-op against a database that already exists.

PRAGMA foreign_keys = ON;

-- A principal is something a permission can be granted to and a card can be
-- assigned to: a human or a group. This is the row other stores join on, so
-- there is NO hard delete anywhere in this file or in the Go code — a row that
-- vanished would leave dangling assignee ids in kanban and dangling grantees in
-- permission-store. `disabled_at` is the only removal.
CREATE TABLE IF NOT EXISTS principals (
    id            TEXT PRIMARY KEY,            -- principal_000001; see formatID
    seq           INTEGER NOT NULL UNIQUE,     -- monotonic; generates id, doubles as the FTS rowid
    kind          TEXT NOT NULL,               -- 'human' | 'group'; enforced in Go against kind.go
    display_name  TEXT NOT NULL,
    email         TEXT NOT NULL DEFAULT '',    -- humans; NOT unique, never a join key
    disabled_at   INTEGER NOT NULL DEFAULT 0,  -- 0 = active
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_principals_kind     ON principals(kind, disabled_at);
CREATE INDEX IF NOT EXISTS idx_principals_disabled ON principals(disabled_at);

-- Who is in which group. Both sides are principal ids; the Go layer enforces
-- that group_id is a 'group' row and member_id a 'human' row (no nested groups
-- in v1), because a CHECK cannot look across rows.
CREATE TABLE IF NOT EXISTS group_members (
    group_id   TEXT NOT NULL REFERENCES principals(id),
    member_id  TEXT NOT NULL REFERENCES principals(id),
    created_at INTEGER NOT NULL,
    PRIMARY KEY (group_id, member_id)
);
CREATE INDEX IF NOT EXISTS idx_group_members_member ON group_members(member_id);

-- What a principal works with: the agents, harness instances, machines, skills
-- and tools a person or group is associated with. It is a list, not a lock —
-- nothing reads it to refuse anything; a card's dispatch picker reads it to put
-- an assignee's instances first. Permission grants stay in permission-store.
--
-- resource_id is the owning store's id as text, and deliberately not a foreign
-- key: the owner is another database. The Go layer checks resource_type against
-- resource_type.go and asks the owner whether the id exists on every write.
-- There is no display-name column: the owner renames things, so the UI resolves
-- names live by id instead of reading back a copy that has gone stale.
CREATE TABLE IF NOT EXISTS principal_resources (
    principal_id  TEXT NOT NULL REFERENCES principals(id),
    resource_type TEXT NOT NULL,     -- 'agent' | 'instance' | 'machine' | 'skill' | 'tool'; enforced in Go
    resource_id   TEXT NOT NULL,     -- agents.id, skills.id, tools.id, or harness-store's instance / machine id
    created_at    INTEGER NOT NULL,
    PRIMARY KEY (principal_id, resource_type, resource_id)
);
-- The reverse lookup: who works with this instance.
CREATE INDEX IF NOT EXISTS idx_principal_resources_resource ON principal_resources(resource_type, resource_id);

-- Content-carrying (not external-content) so the triggers below can stay simple
-- deletes and inserts rather than fts5 'delete' commands. rowid is `seq`.
CREATE VIRTUAL TABLE IF NOT EXISTS principals_fts USING fts5(
    display_name, email
);

CREATE TRIGGER IF NOT EXISTS principals_fts_after_insert AFTER INSERT ON principals BEGIN
    INSERT INTO principals_fts(rowid, display_name, email)
    VALUES (new.seq, new.display_name, new.email);
END;

CREATE TRIGGER IF NOT EXISTS principals_fts_after_delete AFTER DELETE ON principals BEGIN
    DELETE FROM principals_fts WHERE rowid = old.seq;
END;

CREATE TRIGGER IF NOT EXISTS principals_fts_after_update AFTER UPDATE ON principals BEGIN
    DELETE FROM principals_fts WHERE rowid = old.seq;
    INSERT INTO principals_fts(rowid, display_name, email)
    VALUES (new.seq, new.display_name, new.email);
END;
