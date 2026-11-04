# MCP guide

[Getting started](../README.md) · [Engine reference](engine.md)

`songya-mcp` serves MCP over stdio. Configure it using the
[quickstart](../README.md#start-with-mcp). Initialization supplies the agent guide;
`tools/list` supplies tool and parameter descriptions. The same guide is readable
at `memory://guide`.

## Records

| Kind       | Purpose                                  | Metadata conventions                         |
| ---------- | ---------------------------------------- | -------------------------------------------- |
| `memories` | Remembered events and facts              | `title`, `user`, optional `time`             |
| `entities` | People, places, organizations and things | `name`, optional `aliases`, free-form `type` |
| `topics`   | Subjects used to group records           | `name`, optional `aliases`                   |
| `stories`  | Memories arranged into a narrative       | `title`, `user`                              |

Put narrative and descriptive knowledge in `body`, metadata in `fields`.
Metadata and relationship names are open-ended. Songya generates IDs and record
creation/update timestamps automatically.

Memories and stories require an owner. Top-level `user` supplies the default for
new records; `fields.user` can override it. Entities and topics are shared.
Searches with a specific user include that user's memories/stories and shared
records. `user:"all"` or an omitted filter selects everyone. Selection is unrestricted.

## Prepare and save

The examples below are tool names and arguments passed through an MCP client.
Results are in `structuredContent.data`; text content is a short status summary.

Prepare the user's original words:

```json
{
  "name": "prepare_memory",
  "arguments": {
    "original_text": "Around June–July 2025 I had dinner with John at Pizza AMI.",
    "user": "ari"
  }
}
```

The response includes `key`, the searched `query`, `user`, `matches`,
`ambiguities`, and `possible_duplicates`. Read relevant candidates and reuse
existing entity/topic IDs. An empty result describes the supplied search.

Use the returned key to save the related records together:

```json
{
  "name": "save_memory",
  "arguments": {
    "key": "RETURNED_KEY",
    "user": "ari",
    "original_text": "Around June–July 2025 I had dinner with John at Pizza AMI.",
    "changes": [
      {
        "ref": "john",
        "kind": "entities",
        "fields": { "name": "John", "type": "person" },
        "body": "My friend John."
      },
      {
        "ref": "pizza",
        "kind": "entities",
        "fields": { "name": "Pizza AMI", "type": "place" },
        "body": "A restaurant."
      },
      {
        "ref": "dinner",
        "kind": "memories",
        "fields": {
          "title": "Dinner with John",
          "time": {
            "description": "Around June–July 2025",
            "earliest": "2025-06-01",
            "latest": "2025-07-31"
          }
        },
        "body": "I had dinner with John at Pizza AMI.",
        "add_relations": [
          { "type": "with", "target": "$john" },
          { "type": "at", "target": "$pizza" }
        ]
      }
    ]
  }
}
```

`ref` declares a label for a new record. `$john` uses that label within this save.
A relation to an existing record uses its ID directly. `data.refs` maps labels to
generated IDs; `data.records` lists saved IDs. `data.status:"committed"` confirms
saving. Songya retains `original_text` verbatim in the journal and affected memories.

Time describes when the event happened. `description` preserves ordinary wording;
`earliest` and `latest` give known bounds. For an exact day, use equal bounds.
Include only known information and preserve uncertainty.

## Search, read, and update

Search a name, alias, or prose; omit `text` to list records of that kind:

```json
{"name":"search_memory","arguments":{"kind":"entities","text":"John"}}
{"name":"search_memory","arguments":{"kind":"topics"}}
{"name":"read_memory","arguments":{"id":"RECORD_ID"}}
```

An ordinary read returns metadata and Markdown together. `view:"body"` returns
prose only; `view:"source"` explicitly selects the complete Markdown source.
`memory://records/{id}/{offset}` also provides source pages. Binary pages use
`base64` instead of `text`.

Update by ID, using a fresh key from preparation for a new operation:

```json
{
  "name": "save_memory",
  "arguments": {
    "key": "UPDATE_KEY",
    "original_text": "John brought his blue guitar.",
    "changes": [
      { "id": "RECORD_ID", "append": "John brought his blue guitar." }
    ]
  }
}
```

`append` adds prose; `body` replaces it. Omitted fields and body remain intact.
`fields` patches metadata, and `add_relations` adds links. Locking and checks for
changes during publication are handled by the engine.

## Connections, timelines, and stories

```json
{"name":"explore_memory","arguments":{"id":"MEMORY_ID","direction":"both"}}
{"name":"memory_timeline","arguments":{"user":"ari","text":"dinner"}}
```

Graph edges contain `source`, `type`, `target`, and `resolved`. Directions are
`incoming`, `outgoing`, or `both`. Timeline order comes from event-time bounds
and `before`/`after` links. Uncertain order remains uncertain.

Create stories alongside memories, then add ordered members:

```json
{"name":"save_memory","arguments":{"key":"STORY_KEY","user":"ari","changes":[{"ref":"story","kind":"stories","fields":{"title":"Meetings with John"}}],"stories":[{"story":"$story","members":["MEMORY_ID"]}]}}
{"name":"compose_story","arguments":{"id":"STORY_ID"}}
```

An existing story uses its ID in `story`. Composition expands `[[record_id]]`
references in order. New story bodies may refer to new records as `[[$label]]`.
Missing or ambiguous references remain visible.

## Merge, split, and import

Read the sources, then use a fresh key for each operation:

```json
{"name":"organize_memory","arguments":{"action":"merge","key":"MERGE_KEY","sources":[{"id":"FIRST_ID"},{"id":"SECOND_ID"}]}}
{"name":"organize_memory","arguments":{"action":"split","key":"SPLIT_KEY","source":{"id":"SOURCE_ID"},"parts":[{"body":"First part"},{"body":"Second part"}]}}
```

Original bodies remain readable under retired IDs. Results identify replacements
as `refs.merged` or `refs.part_1`, `refs.part_2`, etc. Splits inherit their source
user; merges inherit a common source user. Cross-user merges need `fields.user`.

Imports use roots configured at startup with
`--import-root notes=/absolute/path/to/sources`:

```json
{"name":"import_memories","arguments":{"action":"preview","root":"notes"}}
{"name":"read_memory","arguments":{"root":"notes","source":"old.md"}}
{"name":"import_memories","arguments":{"action":"apply","root":"notes","path":"old.md","user":"ari"}}
```

Omitting `parts` imports the whole file. Explicit parts use reviewed byte ranges
with `start` inclusive and `end` exclusive. Songya preserves the original and
returns saved IDs, coverage, and an `archive` checksum. Archives remain readable
through `read_memory({"archive":"CHECKSUM"})`. Retrying a source reuses its
preserved original and saved IDs.

## Retries and recovery

Retry a save with the **same key and arguments**. Preparation produces an
independent key; it does not invalidate previous preparations. A rejected request
with `status:"not_accepted"` can be corrected under the same key. An accepted
request keeps its original arguments and unfinished work for recovery.

```json
{"name":"memory_maintenance","arguments":{"action":"receipt","key":"SAVE_KEY"}}
{"name":"memory_maintenance","arguments":{"action":"recover"}}
{"name":"memory_maintenance","arguments":{"action":"status"}}
```

For inspection, `read_memory` accepts a saved `key`, `id`, and `view` of
`proposed`, `before`, or `original_text`. Maintenance `abandon` deliberately
withdraws an uncommitted request while retaining its proposals. `rebuild`
recreates the disposable index.

## Paging and limits

Follow `next` when `truncated` is true. Search, graph, timeline and receipt pages
use item offsets; text/source/story pages use byte offsets. Numeric strings are
accepted. Pending or unconfirmed records remain readable with diagnostics.
