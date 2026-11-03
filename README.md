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

## First increment

This increment implements the document codec and candidate ID generation:

- `NewID("m_")` and `ValidID` generate and validate prefixed record IDs.
- `Parse` reads frontmatter and preserves the Markdown body.
- `Document.Metadata` reads the core fields; `Document.Set` edits a metadata field.
- `Document.Marshal` preserves additional YAML fields and the exact body, while
  allowing YAML formatting to change.

The codec does not write to disk. Store-wide uniqueness, filename matching,
filesystem publication, ID resolution, and index construction are not implemented
in this increment. A successful parse does not establish that relation targets
exist.

Run checks with:

```sh
go test -race ./...
go vet ./...
```
