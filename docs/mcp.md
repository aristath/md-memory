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

All four accept an existing `id` to update, and `append` to enrich prose. For `save_memory`:

- **Create:** omit `id`, supply standalone prose in `text` and an owner (or let the integration supply it). Empty content cannot create a memory. Equivalent content supplied through `append` is retained rather than rejected.
- **Append:** supply `id` and `append`. This literally adds a blank line and the new prose, preserving existing content.
- **Update:** supply `id` and the fields to change. `text` replaces the full prose, including an empty string to clear it. When both `text` and `append` are supplied, replacement happens first, then append. Date-only, connection-only, title-only, ownership-only and metadata-only updates are supported.

Preserve the account's meaning, attribution and meaningful quotations; prose must stand on its own without inventing facts. Omitted fields preserve existing information. `title:""` clears the memory title. Custom metadata patches supplied top-level keys, including explicit null values; omitted keys and `metadata:{}` preserve existing values. Supplied relations replace the list; with nonempty `append`, they add connections. `[]` always clears the list. For a connection-only addition, read the old connections and supply every connection to keep plus the new one. Inline identity enrichment preserves existing connections.

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

`time` describes when the event happened, separately from automatic creation/update timestamps. Record supported dates in these structured fields as well as prose. Use an exact `date` or inclusive range bounds. Combine all known timing, including related events and relative expressions, rather than defaulting to the beginning of a known year. Preserve the original temporal wording and explain the basis of inferred bounds in prose; never invent an exact date.

For memory updates, omitted `time` preserves it. Supplied components patch only those components; component `null` explicitly clears it. `time:null` clears all three. When changing an exact date into a range, explicitly supply `date:null`. Switching to an exact date should clear any obsolete bounds. Unreadable values remain available with diagnostics and in retained write snapshots.

Keep separately dated events in separate, connected memories. For example:

```json
{"name":"save_memory","arguments":{"text":"Ari was born in Thessaloniki on 6 March 1981.","user":"ari","time":{"date":"1981-03-06"}}}
{"name":"save_memory","arguments":{"text":"Ari's family moved to Mytilene a few months after his birth, still in 1981. May is the earliest plausible month for this account; the exact date is unknown.","user":"ari","time":{"date":null,"earliest":"1981-05-01","latest":"1981-12-31"},"relations":[{"type":"after","target":"BIRTH_MEMORY_ID"}]}}
{"name":"save_memory","arguments":{"id":"MEMORY_ID","time":{"latest":"1981-11-30"}}}
{"name":"save_memory","arguments":{"id":"MEMORY_ID","text":"The complete corrected account, including details to keep."}}
{"name":"save_memory","arguments":{"id":"MEMORY_ID","relations":[{"type":"related_to","target":"EXISTING_ENTITY_ID"},{"type":"related_to","target":"NEW_ENTITY_ID"}]}}
```

Replace example IDs with IDs returned by the store. The May bound follows the stated interpretation of this account, not a universal definition of “a few months.” The lower bound cannot precede the known birth. The date-only update preserves the earliest bound, prose and connections.

```json
{"date":"2024-12-31","earliest":null,"latest":null}
{"date":null,"earliest":"2024-12-01","latest":"2024-12-31"}
{"date":null,"earliest":"2024-01-01","latest":null}
{"date":null,"earliest":null,"latest":"2024-12-31"}
```

The third example is the agreed “after 2024” convention. `after` orders events without dates; `caused_by` also orders cause before consequence. `part_of` groups events. Contradictions produce diagnostics while independent records remain usable.

## Retrieval

Tool results are in `structuredContent.data`. A save returns publication `status`, ID, operation ID, `outcome`, `changed_fields`, resulting prose/time/relations and diagnostics. `status:"committed"` confirms durability, not that new facts were added. For a committed operation, `outcome` is `created`, `updated`, or `unchanged`; `changed_fields` lists changed data fields, excluding automatic timestamps and source bookkeeping. Unfinished writes retain their pending/blocked outcome and recovery receipt; they are never described as completed updates.

An ID-only call or an update supplying identical values returns `unchanged`, an empty `changed_fields`, and actionable guidance; it does not rewrite the memory or advance its timestamp. Compare the returned content and time with the facts intended for saving. Do not claim new facts were saved or repeat the same unchanged call. Previews are bounded; read the record for full content. Retrying an accepted request key returns the original saved snapshot and outcome, even after later edits, without replaying the write.

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
