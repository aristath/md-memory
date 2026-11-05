# MCP guide

[Getting started](../README.md) · [Engine reference](engine.md)

`songya-mcp` serves MCP over stdio. Initialization and `memory://guide` provide compact agent instructions. Tool discovery describes the arguments.

## Records and capture

- **Memory:** an event, experience, fact or preference, understandable on its own.
- **Entity:** a particular person, place, organization, object or other thing, with a brief identifying description. Its experiences belong in linked memories.
- **Topic:** a subject connecting memories.
- **Story:** an ordered narrative composition of connected memories.

Before every save, call `prepare_memory` with the information to keep. Review its bounded shortlist, then save once, including any new connected entities/topics. Reuse existing records. Save snippets as information arrives; enrich an existing memory for additional information, correct its prose when facts change, and create connected memories for distinct events. Do not invent facts, identities, dates or causes.

```json
{"name":"prepare_memory","arguments":{"text":"Alex and Sam enjoy the soup at Cedar Café.","user":"ari"}}
{"name":"save_memory","arguments":{"text":"Alex and Sam enjoy the soup at Cedar Café.","user":"ari","relations":[{"type":"related_to","target":"Alex"},{"type":"related_to","target":"Sam"},{"type":"related_to","target":{"kind":"entities","name":"Cedar Café","type":"place"}}]}}
```

A target is an existing record ID, an entity name, or a named object with `kind` (`entities` or `topics`), `name`, and optional `type`, `description`, `aliases`. Unique exact names/aliases reuse existing identities; matching is case-insensitive. New IDs are assigned by the engine. Ambiguous or unusable connections remain in `unresolved_connections` with diagnostics; healthy connections and prose still save.

Memories and stories require an owning `user`; Clara supplies the current user automatically. Use `user:"clara"` for Clara's experiences. Ownership selects a collection; entity connections identify participants. Reflecting on another person's experience does not make the writer a participant. Entities and topics are shared. Selection is unrestricted; `user:"all"` selects everyone. Updating without a user preserves ownership.

## Writes and connections

| Tool | Main arguments |
| --- | --- |
| `save_memory` | `text`, optional `title`, `time`, `user`, `metadata`, `relations` |
| `save_entity` | `name`, optional `type`, `aliases`, `description`, `metadata`, `relations` |
| `save_topic` | `name`, optional `aliases`, `description`, `metadata`, `relations` |
| `save_story` | `title`, optional `text`, `user`, `metadata`, ordered `relations` |

All four accept an existing `id` to update, and `append` to enrich prose. `text` or `description` replaces supplied prose; omission preserves it. Omitted metadata and relations stay intact. Supplied relations replace the list; when enriching with `append`, they add connections. `[]` clears the list. Inline identity enrichment preserves existing connections.

Entity/topic `description` is their Markdown body, also used by search and the library table. Do not duplicate it in metadata.

All authoritative connections use `relations: [{type, target}]`. Store one connection and let Songya derive its reverse:

| Type | Meaning of A → B | Derived reverse |
| --- | --- | --- |
| `after` | A happened after B | `before` |
| `caused_by` | B caused A | `causes` |
| `part_of` | A is a component or sub-event of B | `contains` |
| `related_to` | General association, including entities and topics | `related_to` |
| `possibly_same_as` | Possible identity match, without merging | `possibly_same_as` |

A story's `related_to` links to memories/stories define reading order. Other links associate identities or subjects. Story prose is introductory text; narrative order does not assert chronology.

```json
{"name":"save_story","arguments":{"title":"Visits with Sam","user":"ari","relations":[{"type":"related_to","target":"FIRST_MEMORY_ID"},{"type":"related_to","target":"SECOND_MEMORY_ID"}]}}
{"name":"save_memory","arguments":{"id":"MEMORY_ID","append":"They also enjoy the salad."}}
```

## Event time

`time` has three nullable fields. Use an exact `date` or range bounds; leave unknown values null. IDs and creation/update timestamps are automatic.

```json
{"date":"2024-12-31","earliest":null,"latest":null}
{"date":null,"earliest":"2024-12-01","latest":"2024-12-31"}
{"date":null,"earliest":"2024-01-01","latest":null}
{"date":null,"earliest":null,"latest":"2024-12-31"}
```

The third example is the agreed “after 2024” convention. `after` orders events without dates; `caused_by` also orders cause before consequence. `part_of` groups events. Contradictions produce diagnostics while independent records remain usable.

## Retrieval

Tool results are in `structuredContent.data`. A save returns status, ID, operation ID, prose/time/relations and diagnostics. `status:"committed"` confirms publication. Previews are bounded; read the record for full content.

```json
{"name":"recall_memory","arguments":{"text":"Sam","user":"ari"}}
{"name":"search_memory","arguments":{"text":"Sam","kind":"entities"}}
{"name":"search_memory","arguments":{"kind":"topics"}}
{"name":"search_memory","arguments":{"kind":"memories","related_to":"ENTITY_ID"}}
{"name":"search_memory","arguments":{"mode":"semantic","text":"orchestral performer"}}
{"name":"read_memory","arguments":{"id":"RECORD_ID","view":"source"}}
{"name":"explore_memory","arguments":{"id":"RECORD_ID","direction":"both"}}
{"name":"memory_timeline","arguments":{"user":"ari"}}
{"name":"compose_story","arguments":{"id":"STORY_ID"}}
```

Recall returns connected accounts and story context. Semantic recall adds a bounded story introduction and neighboring events. Direct edits refresh automatically. Search without text lists records. Configure embeddings in the store's `config.json`; saving never waits for embeddings. `memory_maintenance(action="embed")` refreshes missing vectors.

Follow `next` as `offset` with the same query/user when `truncated` is true. Recall/text/source/story offsets count bytes; search/graph/timeline offsets count items. Numeric strings work. Recall caps at 16 KiB; MCP reads/composition default to 8 KiB and cap at 12 KiB. Missing data produces diagnostics while healthy work continues.

## Organization, imports and recovery

`organize_memory(action="merge", sources=[...])` combines reviewed records; `action="split"` takes a source and reviewed parts. Retired records remain readable and redirects preserve references. Splits inherit ownership; merges inherit a common owner, or require explicit `metadata.user` across owners.

Configure import roots with `--import-root notes=/absolute/path`. `import_memories(action="preview", root="notes")` lists sources; `action="apply"` takes a path and user. Optional reviewed parts use exact byte ranges (`start` inclusive, `end` exclusive), kind and metadata. Originals are retained with a checksum and coverage mapping.

Clients can attach a stable `songya/request-key` in MCP `_meta` to writes. Retry identical arguments with that key. Clara supplies its durable invocation ID. Without client metadata, Songya assigns an independent operation ID. Identical prose in separate operations can describe separate memories.

`memory_maintenance(action="receipt", key=...)` inspects an outcome; `recover` resumes pending work; `status` reports diagnostics; `rebuild` refreshes the disposable index. `abandon` withdraws uncommitted work while retaining proposed data. `read_memory` with `key`, `id`, and `view:"proposed"`, `"before"` or `"original_text"` reads retained work. Ordinary saves handle this bookkeeping automatically.
