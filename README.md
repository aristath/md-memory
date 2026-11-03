# sōngyā Memory Engine

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
  YAML merges supply inherited fields; explicit values take precedence. Reading
  the metadata view preserves the source tree, and edits add explicit overrides.
  Shared merge sources are searched once per field, including when a field is absent.
- `Document.Diagnostics()` reports unknown timestamps, unresolved relations,
  unparsed metadata, and other recoverable problems without failing the operation.
- `Document.Set(name, value)` edits one field while preserving other YAML fields,
  including references to nested anchors in the replaced value. Colliding anchor
  names are resolved across the whole document, so saving and rereading preserve
  the same values. Invalid anchor names are repaired and reported on the edited
  document while preserving their values and references. Alias keys use their
  resolved field names consistently for reads, edits, and duplicate diagnostics.
- `Document.Marshal()` serializes the document without rejecting damaged metadata.
  Editing a directly supplied non-mapping root retains that root in
  `unparsed_frontmatter` and makes the edited field available normally.

Unknown values remain in the YAML tree. Missing timestamps stay unknown rather
than being invented. Syntactically unreadable frontmatter or content following a
YAML document end marker is retained verbatim in `unparsed_frontmatter` when
serialized. Healthy fields in the first YAML document remain usable, and the
Markdown body stays unchanged. If the frontmatter boundaries cannot be determined,
the entire original is retained as
the body. Reads do not silently rewrite original files.

Standalone parsed documents have no ID; the store derives it from the filename.
Metadata inputs are data: scalars, maps, lists, and raw YAML nodes. Pointers to
these values, named scalar types, `time.Time`, `time.Duration`, `Relation`, and
`Metadata` are supported. Go struct tags and custom encoding callbacks belong
outside the engine; callers can convert such values to a YAML node first.

An unsupported value or cyclic Go reference becomes a `!md-memory/issue` scalar at
that location. Healthy siblings and the body remain usable. The issue is saved
with the record, so `Diagnostics()` still reports it, with its metadata path,
after reopening. Ordinary shared values and YAML aliases remain supported.
An incomplete raw YAML mapping retains its healthy entries and unfinished key;
only the missing value becomes an issue scalar. Other malformed node shapes
retain their readable values and children in a recovery mapping. Invalid UTF-8
scalar bytes are retained as YAML binary data; unsafe comments use an escaped
representation so they cannot break the header.
New Go maps use lexical key ordering. Parsed YAML retains its existing ordering.
YAML nodes inside a list or map share the field's anchor scope. Encoding copies
the supplied nodes, including aliases and comments, so later caller edits cannot
change the document. Supplied alias targets retain their identity even when map
ordering moves a reference before its definition. Shared Go maps and lists are
encoded once and referenced with aliases rather than expanded repeatedly.

## Store

- `Open(path)` initializes missing directories and recovers interrupted writes.
  An unavailable record directory or damaged cache does not disable healthy ones.
  If another writer holds the lock, recovery is deferred with a diagnostic;
  opening a reader does not wait for that writer.
- `Store.Create(destination, fields, body)` saves a new record. Choose `Memories`,
  `Entities`, `Topics`, or `Stories`; fields are YAML metadata as `map[string]any`.
  The engine generates the ID and both UTC timestamps. A supplied `id` is ignored.
  Metadata is encoded before acquiring the write lock. A failed save returns an
  ID only when this operation has reserved it; a collision never claims another
  write's ID.
- `Store.Read(id)` reads committed content first, or retries pending publication
  when the committed record is unavailable.
- `Store.Update(id, revision, fields, body)` patches metadata and replaces the
  body of an existing committed record. Pass `Revision` from a read or successful
  committed save, and pass the previous body when only metadata changes. Fields
  omitted from the patch remain intact. The engine ignores proposed `id`,
  `created_at`, and `updated_at` fields, preserves the existing creation value
  (including missing or damaged data), and assigns the current UTC update time.
- `Store.Recover()` retries outstanding work when the lock is available.
  Individual failures are isolated; successful recovery clears the deferred status.
  Saving an unrelated record does not clear deferred recovery.
- `Store.Diagnostics()` reports directory and recovery issues safely across goroutines.
  Successful directory scans clear obsolete file issues and report remaining
  damage again; failed scans retain earlier issues.

See the [executable example](example_test.go). `.lock` coordinates engine processes
and must not be removed while the store is in use. External editors do not acquire
this advisory lock. Filesystem access stays confined to the root.

Healthy committed records can be read without taking the write lock. If write
coordination is damaged, existing records remain readable while unsafe writes
are blocked and the coordination issue is reported through diagnostics. Pending
work never takes precedence over readable committed content. Readable pending
creations also remain available while another writer holds the lock; publication
is deferred and reported on the returned document. Reads recheck the committed
filename if the pending name disappears during publication.

During initialization, the engine syncs the store's ancestors before creating
`.lock`. If syncing fails, existing content stays readable and writes are blocked;
`Recover()` or `Create()` on the same handle retries initialization once the
blocker clears. Once initialized, a store does not require opening or syncing all
its ancestors on later calls. An initialized handle never replaces a missing
coordination lock.
The cache may be rebuilt; blocked record directories retain their contents, and
errors affect operations on that directory only.

## Updates

`Document.Revision` is the SHA-256 of the exact committed file bytes. It is never
stored in YAML. Pending creations have no committed revision. A successful update
returns the new revision; use it for the next update.

An update checks the base revision before staging and again immediately before
replacement, while engine writers coordinate through `.lock`. A stale revision
returns `ErrConflict`; use `errors.Is(err, memory.ErrConflict)` to identify it.
Read the current record and resolve the conflicting edits before submitting a new
update. Retrying an old revision cannot overwrite a newer committed version.

Only one pending change reserves a record at a time. Update first attempts to
recover existing staging for that record; unresolved work blocks another update
to the same record while other records remain usable. Missing committed records
are never recreated from update proposals. Reads continue returning the committed
version while an update is pending.

An update returns an error if it cannot confirm durable publication. A returned
document with `Pending=true` identifies this attempt's retained staging; the error
may indicate incomplete data or an unsynced write. `Recover()` or reopening the
store retries complete, verified work. The proposal is not a confirmed committed
version until publication succeeds. Re-read the record after recovery. Conflicting
or incomplete proposals remain on disk with diagnostics for deliberate resolution.

External editors do not acquire the advisory lock. Revision checks detect edits
visible before the last check, but checking bytes and renaming a file are not an
atomic compare-and-swap. An external edit between those steps can still be
overwritten. Concurrent writers need to use the engine's lock for that guarantee.
Byte revisions also cannot distinguish a file that changed and was restored to
exactly the same bytes.

## Durable writes and recovery

A creation exclusively creates a hidden `.ID.md.pending/` directory to reserve
its ID. This requires no scan of other records. Inside it, a file named
`<sha256>.tmp` holds the data and records its expected checksum. The file, staging
directory, and destination directory are synced before publication. Recovery
verifies the checksum, then hard-links the data to the final `<id>.md` name
without replacing existing files. New saves reuse their already-synced staging
data for this publication step. The destination directory is synced before
staging cleanup begins. If the final file already exists with identical bytes,
it is also synced before the redundant staged copy is removed, even when the
two paths refer to separate files.

Before durable publication, failures retain the staged data. Complete creations
recover automatically on open, read, or explicit recovery. Pending updates recover
on open or explicit recovery; committed reads keep returning the current version.
Empty reservations, ambiguous candidates, incomplete data, and conflicting data are retained and reported. Recovery never
promotes an unverified partial file or overwrites a conflicting record. Readable
partial staging data remains available with `Pending=true` and diagnostics.
An unfinished staging directory keeps its ID reserved for its original record.

`Document.Pending` means the record is retained in staging. A successful creation
with `Pending=true` has durably stored the complete record; it remains readable
while final publication is retried. If syncing cannot establish durability, the
operation returns an error and the retained document/ID where available. Use that
ID to inspect or resume the same write rather than creating another record.
Cleanup errors after durable publication are diagnostics, not failed saves.

Updates share the same reservation directory. Their payload is named
`<base-sha256>.<new-sha256>.tmp`. Recovery verifies the new checksum and requires
the committed bytes to match the base before replacing anything. If they already
match the new checksum, recovery confirms and syncs that result without replaying
the replacement. A second hard link named `publish` is atomically renamed over the
committed name; the synced payload remains until the destination directory is
synced. Unknown staging files are preserved.

The current store requires a local filesystem with hard links, advisory locks,
and directory syncing. Deletion, target-existence checks, and index
construction are outside the current increment.

```sh
go test -race ./...
go vet ./...
```
