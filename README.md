# md-memory

A standalone Go package for a Markdown-backed memory engine.

## Record layout

```text
memory-store/
  memories/
    m_<12-character-suffix>.md
  entities/
    e_<12-character-suffix>.md
  topics/
    t_<12-character-suffix>.md
  stories/
    s_<12-character-suffix>.md
  .index/
  .lock
```

IDs consist of a type prefix (`m_`, `e_`, `t_`, or `s_`) followed by 12 random
lowercase letters or digits. The prefix is part of the ID, including in references.
The same suffix in different record types produces distinct IDs.
The filename is `<id>.md`. Each file has YAML frontmatter and a Markdown body:

```markdown
---
id: m_7k3m9x2q6v4c
title: Example
created_at: 2026-11-03T10:00:00Z
updated_at: 2026-11-03T10:00:00Z
relations:
  - type: motivated_by
    target: m_b8r5n2w9d4hx
---

Markdown body.
```

Additional metadata may contain arbitrary YAML values, including lists and nested
mappings. Relation types are open-ended. A relation is stored in its source file;
incoming connections are derived with the same type name.

The Markdown files are authoritative. `.index/` is reserved for generated data
that can be rebuilt from the files.

## Document format

The document codec and ID generation are available independently of storage:

- `NewID("m_")` and `ValidID` generate and validate prefixed record IDs.
- `Parse` reads frontmatter and preserves the Markdown body.
- `Document.Metadata` reads the core fields; `Document.Set` edits a metadata field.
- `Document.Marshal` preserves additional YAML fields and the exact body, while
  allowing YAML formatting to change.

## Filesystem storage

- `Open(path)` creates missing store directories and preserves existing files.
- `Store.Create(destination, fields, body)` creates a record in an explicit
  destination: `Memories`, `Entities`, `Topics`, or `Stories`. Fields are a
  `map[string]any` containing YAML metadata.
- `Store.Read(id)` resolves the prefixed ID directly to its file and checks that
  the metadata ID matches the filename.

Creation assigns the ID and both UTC timestamps, overriding any supplied values
for those fields. Additional metadata and the body are preserved. See the
[executable example](example_test.go) for the complete create/read sequence.

Engine processes coordinate through an OS-backed `.lock` file at the store root.
It remains separate from the disposable `.index/` directory and must stay in place
while the store is in use. Store operations also support concurrent goroutines.
These advisory locks coordinate engine access; external editors do not acquire them.

Creation writes and syncs a hidden staging file, publishes the complete record
using an atomic hard link that cannot replace an existing filename, then syncs
the directory. Collisions generate a fresh ID. A non-nil document returned with
an error identifies a record that was published but whose final sync or cleanup
failed; callers should use that ID to inspect the existing record before retrying.
The storage layer requires a local filesystem supporting hard links, advisory
locking, and directory syncing.

Malformed files and filename/metadata mismatches return errors identifying the
affected path and remain unchanged. Other records remain readable and new records
can still be created. Staging files left by process interruption remain hidden;
they are not returned as records or overwritten by new creations.

Updates, deletion, malformed-file recovery, target-existence checks, and index
construction are outside this increment. `.index/` is initialized but remains
empty; current storage operations do not depend on it.

Run checks with:

```sh
go test -race ./...
go vet ./...
```
