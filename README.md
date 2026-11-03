# md-memory

A standalone Go package for connected Markdown records. Durability, continued
operation, and simple code are the project's priorities; see [AGENTS.md](AGENTS.md).

## Files

```text
memory-store/
  memories/m_<12-character-suffix>.md
  entities/e_<12-character-suffix>.md
  topics/t_<12-character-suffix>.md
  stories/s_<12-character-suffix>.md
  .index/
  .lock
```

The filename is the record's identity. IDs are a type prefix followed by 12 random
lowercase letters or digits, and are returned as `Document.ID`, never written as
an `id` metadata field. A record contains YAML frontmatter and a Markdown body:

```markdown
---
title: Example
created_at: 2026-11-03T10:00:00Z
updated_at: 2026-11-03T10:00:00Z
relations:
  - type: motivated_by
    target: m_b8r5n2w9d4hx
---

Markdown body.
```

Metadata is open-ended, including lists and nested mappings. Relation types are
open-ended too. Relations live in their source file; reverse connections are
derived using the same type. Markdown files are authoritative; `.index/` is disposable.

## Documents

- `Parse(data)` always returns the readable content, including damaged records.
- `Document.Metadata()` reads fields independently. For example, numeric `3`
  works as the title `"3"`, and one invalid field does not hide other fields.
- `Document.Diagnostics()` reports unknown timestamps, unresolved relations,
  unparsed metadata, and other recoverable problems without failing the operation.
- `Document.Set(name, value)` edits one field while preserving other YAML fields,
  including references to nested anchors in the replaced value. Colliding anchor
  names are renamed when needed to preserve their original values.
- `Document.Marshal()` serializes the document without rejecting damaged metadata.

Unknown values remain in the YAML tree. Missing timestamps stay unknown rather
than being invented. Syntactically unreadable frontmatter or content following a
YAML document end marker is retained verbatim in `unparsed_frontmatter` when
serialized. Healthy fields in the first YAML document remain usable, and the
Markdown body stays unchanged. If the frontmatter boundaries cannot be determined,
the entire original is retained as
the body. Reads do not silently rewrite original files.

Standalone parsed documents have no ID; the store derives it from the filename.

## Store

- `Open(path)` initializes missing directories and recovers interrupted writes.
  An unavailable record directory or damaged cache does not disable healthy ones.
  If another writer holds the lock, recovery is deferred with a diagnostic;
  opening a reader does not wait for that writer.
- `Store.Create(destination, fields, body)` saves a new record. Choose `Memories`,
  `Entities`, `Topics`, or `Stories`; fields are YAML metadata as `map[string]any`.
  The engine generates the ID and both UTC timestamps. A supplied `id` is ignored.
- `Store.Read(id)` reads committed content first, or retries pending publication
  when the committed record is unavailable.
- `Store.Recover()` retries outstanding work when the lock is available.
  Individual failures are isolated; successful recovery clears the deferred status.
- `Store.Diagnostics()` reports directory and recovery issues safely across goroutines.

See the [executable example](example_test.go). `.lock` coordinates engine processes
and must not be removed while the store is in use. External editors do not acquire
this advisory lock. Filesystem access stays confined to the root.

Healthy committed records can be read without taking the write lock. If write
coordination is damaged, existing records remain readable while unsafe writes
are blocked and the coordination issue is reported through diagnostics. Pending
work never takes precedence over readable committed content. Readable pending
records also remain available while another writer holds the lock; publication
is deferred and reported on the returned document.

During initialization, the engine syncs the store's ancestors before creating
`.lock`. If syncing fails, existing content stays readable and writes are blocked;
reopening retries initialization. Once initialized, a store does not require
opening or syncing all its ancestors on later calls.
The cache may be rebuilt; blocked record directories retain their contents, and
errors affect operations on that directory only.

## Durable writes and recovery

A new write first goes to a hidden staging file whose name includes the expected
SHA-256 checksum. Recovery verifies completeness before publishing anything. A
complete write is synced, linked to a `.pending` name, and the directory is synced.
The final `<id>.md` name is then published without replacing existing files and synced.
Publication checks the pending bytes against the expected checksum recorded in
the staging filename. This evidence is retained until removal of the pending
marker is durable; a damaged pending copy remains readable with diagnostics.

A failure never deletes the staged data. Complete writes recover automatically on
open, read, or explicit recovery, including interruption before the `.pending`
marker. Incomplete or conflicting data is retained and reported. Recovery never
promotes an unverified partial file or overwrites a conflicting record.

`Document.Pending` means the record is retained in staging. A successful creation
with `Pending=true` has durably stored the complete record; it remains readable
while final publication is retried. If syncing cannot establish durability, the
operation returns an error and the retained document/ID where available. Use that
ID to inspect or resume the same write rather than creating another record.
Cleanup errors after durable publication are diagnostics, not failed saves.

The current store requires a local filesystem with hard links, advisory locks,
and directory syncing. Updates, deletion, target-existence checks, and index
construction are outside the current increment.

```sh
go test -race ./...
go vet ./...
```
