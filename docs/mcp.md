# MCP guide

[Getting started](../README.md) · [Engine reference](engine.md)

`songya-mcp` serves MCP over stdio. Initialization and `memory://guide` provide
compact agent instructions. Tool discovery describes each argument.

## Save a memory

Memories hold experiences, facts, preferences and history. Each memory links to
all relevant entities, including the people whose experiences or preferences it
describes. A memory can grow over time and connect to several people and places.
Entity descriptions briefly identify who or what the entity is. Find its linked
memories with `search_memory({"kind":"memories","related_to":"ENTITY_ID"})`.

```json
{
  "text": "Alex Morgan and Sam Lee enjoy the lentil soup at Cedar Café.",
  "user": "ari",
  "entities": [
    { "name": "Alex Morgan", "type": "person", "relation": "about" },
    { "name": "Sam Lee", "type": "person", "relation": "about" },
    { "name": "Cedar Café", "type": "place", "relation": "at" }
  ]
}
```

Pass this object to `save_memory`. Songya creates the memory and its connections
in one durable write. A unique exact name or alias reuses an existing entity;
matching is case-insensitive. An `id` selects a specific existing entity. Simple
lists such as `"entities":["Sam Lee"]` also work. Include `relation` to preserve
the participant's role in the memory.

Save directly. Use `search_memory` or `prepare_memory` to find a record when
needed. To enrich an existing memory, supply its `id` and `append`. Omitted
entities and metadata stay intact. Long prose is preserved in full; returned
previews are bounded and full reads are paged. The memory's linked entities
remain brief identity records as its history grows.

`time` describes when something happened. It accepts words such as `"last summer"`
or an object with `description`, `earliest` and `latest`. Preserve approximate
wording; known exact dates use equal bounds. IDs and creation/update times are
automatic. `title`, custom `metadata` and `relations` are optional.

Ambiguous or unavailable capture participants remain in `unresolved_connections`
with their details. Unresolved relation targets remain in the relation list.
Diagnostics explain incomplete connections while healthy work still saves.

Memories and stories require an owning `user`. Integrations can fill it from the
current user; Clara does this automatically. Entities and topics are shared.
Searches select a user's memories/stories and shared records; `user:"all"`
selects everyone. Selection is unrestricted.

## Dedicated writes

| Tool          | Arguments                                                                                                                |
| ------------- | ------------------------------------------------------------------------------------------------------------------------ |
| `save_memory` | `text`, optional `title`, `time`, `entities`, `topics`, `user`, `metadata`, `relations`; `id` updates an existing memory |
| `save_entity` | `name`, optional `type`, `aliases`, `description`, `metadata`, `relations`; `id` selects an existing entity              |
| `save_topic`  | `name`, optional `aliases`, `description`, `metadata`, `relations`; `id` selects an existing topic                       |
| `save_story`  | `title`, optional `text`, ordered `members`, `user`, `metadata`, `relations`; `id` updates an existing story             |

All four support `append` to add prose. Entity prose is a brief identifying description. `text` or `description` replaces supplied
prose; omission preserves it. Omitted metadata stays intact. Standalone entity
and topic saves reuse unique matching names, or create a new record.

```json
{"name":"save_entity","arguments":{"name":"Sam Lee","type":"person","description":"A school friend."}}
{"name":"save_topic","arguments":{"name":"Gardening","description":"Plants and gardens."}}
{"name":"save_memory","arguments":{"id":"MEMORY_ID","append":"They also enjoy the café’s salad."}}
{"name":"save_story","arguments":{"user":"ari","title":"Visits with Sam","members":["FIRST_MEMORY_ID","SECOND_MEMORY_ID"]}}
```

Supplied story `members` replace the ordered membership. Omitting them preserves
existing membership, including when the narrative text is edited. Explicit `[]`
clears membership. Original narratives remain available in retained write data.

## Connections and corrections

`relations` is the explicit connection list on each save tool. Omission preserves
it; a supplied list replaces it; `[]` clears it. Targets accept existing record
IDs or ordinary entity names. Names are resolved or created in the same durable
write. Relationship labels describe the source record's connection to its target.
For example, a café can be `liked_by` a person; a visit memory can be `with` a
person and `at` a café.

```json
{"name":"save_entity","arguments":{"id":"CAFE_ID","relations":[{"type":"liked_by","target":"Sam Lee"}]}}
{"name":"save_entity","arguments":{"id":"CAFE_ID","relations":[]}}
```

On memory updates, supplied `entities` and `topics` add connections while
preserving existing links. A supplied `relations` list first replaces the
explicit list; any participants supplied in the same call are then connected.

## Results and retrieval

Tool results are in `structuredContent.data`. A save returns `status`, the main
record's `id`, `operation_id`, saved prose/time/relations, connected entities and
topics, and diagnostics. `status:"committed"` confirms publication. Long returned
prose sets `text_truncated`; read the record for its full contents.

```json
{"name":"prepare_memory","arguments":{"text":"Sam Lee","user":"ari"}}
{"name":"search_memory","arguments":{"text":"Sam","kind":"entities"}}
{"name":"search_memory","arguments":{"kind":"topics"}}
{"name":"read_memory","arguments":{"id":"MEMORY_ID"}}
{"name":"explore_memory","arguments":{"id":"MEMORY_ID","direction":"both"}}
{"name":"memory_timeline","arguments":{"user":"ari","text":"dinner"}}
{"name":"compose_story","arguments":{"id":"STORY_ID"}}
```

Preparation is optional: it finds candidates, ambiguities and possible duplicate
names. Its `query` identifies what was searched. Search without text lists records.
Ordinary reads return metadata and Markdown; `view:"body"` selects prose only.
Graph directions are `incoming`, `outgoing` or `both`. Timelines use stated event
bounds and `before`/`after` connections; uncertain order stays uncertain.

## Organization and imports

```json
{"name":"organize_memory","arguments":{"action":"merge","sources":["FIRST_ID","SECOND_ID"],"metadata":{"title":"Combined account"}}}
{"name":"organize_memory","arguments":{"action":"split","source":"SOURCE_ID","parts":[{"text":"First part"},{"text":"Second part"}]}}
```

Original records remain readable under retired IDs. Merge returns `id`; split
returns ordered replacement `ids`. Splits inherit their source user; merges
inherit a common user. Cross-user merges need an explicit `metadata.user`.

Configure optional import roots with `--import-root notes=/absolute/source/path`:

```json
{"name":"import_memories","arguments":{"action":"preview","root":"notes"}}
{"name":"read_memory","arguments":{"root":"notes","source":"old.md"}}
{"name":"import_memories","arguments":{"action":"apply","root":"notes","path":"old.md","user":"ari"}}
```

Omit `parts` to import the entire file. Reviewed parts use exact byte ranges,
`start` inclusive and `end` exclusive, with optional `kind` and `metadata`.
The original is preserved. Results include mapped IDs, coverage, and an archive
checksum, readable with `read_memory({"archive":"CHECKSUM"})`.

## Recovery and clients

Clients can attach a stable `songya/request-key` in MCP `_meta` to each write.
Reuse it with identical arguments when retrying the same operation. Clara forwards
its durable tool invocation ID automatically. Distinct operations receive distinct
keys, so identical words can describe separate memories. Without client metadata,
Songya creates an independent operation ID and returns it in the result.

Use `memory_maintenance` with `action:"receipt"` and `key:OPERATION_ID` to inspect
an outcome. `recover` resumes retained work; `status` reports diagnostics;
`rebuild` refreshes the disposable index. `abandon` deliberately withdraws an
uncommitted operation while retaining its proposed data. `read_memory` with `key`,
`id` and `view:"proposed"`, `"before"` or `"original_text"` inspects retained data.
These are recovery operations; ordinary saves handle their bookkeeping internally.

Follow `next` when `truncated` is true. Text/source/story offsets count bytes;
search, graph and timeline offsets count items. Numeric strings are accepted.
`memory://records/{id}/{offset}` provides source pages; binary pages use `base64`.
