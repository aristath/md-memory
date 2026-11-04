# MCP guide

[Getting started](../README.md) · [Engine reference](engine.md)

`songya-mcp` serves MCP over stdio. Initialization and `memory://guide` provide
compact agent instructions. Tool discovery describes each argument.

## Save a memory

Describe the memory, its time, and its participants directly:

```json
{
  "text": "Around June–July 2025 I had dinner with John Pavlou at Pizza AMI.",
  "user": "ari",
  "time": {
    "description": "Around June–July 2025",
    "earliest": "2025-06-01",
    "latest": "2025-07-31"
  },
  "entities": [
    {"name": "John Pavlou", "type": "person", "description": "An old friend.", "relation": "with"},
    {"name": "Pizza AMI", "type": "place", "relation": "at"}
  ],
  "topics": [{"name": "Old friendships"}]
}
```

Pass this object to `save_memory`. Songya creates the memory and its connections
in one durable write. IDs, creation/update times and request bookkeeping are
automatic. `title`, custom `metadata`, and existing-ID `relations` are optional.
`time` also accepts ordinary wording such as `"last summer"`. Known exact dates
use equal earliest/latest bounds.

Entities represent people, places, organizations or things. Topics represent
subjects. A unique exact name or alias reuses an existing record; matching is
case-insensitive. Supply `id` to select a particular existing entity/topic.
Ambiguous or unavailable identities remain in the saved memory's
`unresolved_connections`, with diagnostics. Healthy connections still save.
Capture adds supplied descriptive facts while preserving existing descriptions.
Simple lists such as `"entities":["John"]` and `"topics":["Gardening"]` also work.

Memories and stories require an owning `user`. Integrations can fill it from the
current user; Clara does this automatically. Entities and topics are shared.
Searches select a user's memories/stories and shared records; `user:"all"`
selects everyone. Selection is unrestricted.

## Dedicated writes

| Tool | Arguments |
| --- | --- |
| `save_memory` | `text`, optional `title`, `time`, `entities`, `topics`, `user`, `metadata`, `relations`; `id` updates an existing memory |
| `save_entity` | `name`, optional `type`, `aliases`, `description`, `metadata`, `relations`; `id` selects an existing entity |
| `save_topic` | `name`, optional `aliases`, `description`, `metadata`, `relations`; `id` selects an existing topic |
| `save_story` | `title`, optional `text`, ordered `members`, `user`, `metadata`, `relations`; `id` updates an existing story |

All four support `append` to add prose. `text` or `description` replaces supplied
prose; omission preserves it. Omitted metadata stays intact. Standalone entity
and topic saves reuse unique matching names, or create a new record.

```json
{"name":"save_entity","arguments":{"name":"John Pavlou","type":"person","description":"An old friend."}}
{"name":"save_topic","arguments":{"name":"Gardening","description":"Plants and gardens."}}
{"name":"save_memory","arguments":{"id":"MEMORY_ID","append":"We talked about roses."}}
{"name":"save_story","arguments":{"user":"ari","title":"Meetings with John","members":["FIRST_MEMORY_ID","SECOND_MEMORY_ID"]}}
```

Supplied story `members` replace the ordered membership. Omitting them preserves
existing membership, including when the narrative text is edited. Explicit `[]`
clears membership. Original narratives remain available in retained write data.

## Results and retrieval

Tool results are in `structuredContent.data`. A save returns `status`, the main
record's `id`, `operation_id`, saved prose/time/relations, connected entities and
topics, and diagnostics. `status:"committed"` confirms publication. Long returned
prose sets `text_truncated`; read the record for its full contents.

```json
{"name":"prepare_memory","arguments":{"text":"John Pavlou","user":"ari"}}
{"name":"search_memory","arguments":{"text":"John","kind":"entities"}}
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
