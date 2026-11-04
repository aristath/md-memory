# MCP guide

[Getting started](../README.md) · [Engine reference](engine.md)

The `songya-mcp` executable uses the official Go MCP SDK and stdio transport.
Install it and configure your client using the [quickstart](../README.md#start-with-mcp).
Only protocol messages go to stdout; flags and errors go to stderr. The process
handles EOF, SIGINT, SIGTERM, and request cancellation.

- [Tools and resources](#tools-and-resources)
- [Users](#users)
- [Prepare and save](#prepare-and-save)
- [Search, read, and update](#search-read-and-update)
- [Relationships, timelines, and stories](#relationships-timelines-and-stories)
- [Merge and split](#merge-and-split)
- [Imports](#imports)
- [Retries and recovery](#retries-and-recovery)
- [Paging and limits](#paging-and-limits)

## Tools and resources

| Tool | Inputs and purpose |
| --- | --- |
| `prepare_memory` | `account`, optional `user`, `limit`: find candidates and ambiguities; return a fresh request key |
| `save_memory` | `key`, `changes`, optional `user`, `account`, `stories`: apply a complete related batch |
| `search_memory` | Optional `user`, `text`, `kind`, `related_to`, `relation_type`, `include_retired`, `offset`, `limit`: search or list |
| `read_memory` | `id`, or `source`, or configured `root` plus `source`, or `archive`, or `key` plus `view`; optional `offset`, `limit`: read byte pages |
| `explore_memory` | Optional `user`, `id`, `direction`, `type`, `depth`, `offset`, `limit`: outgoing links, backlinks, or traversal |
| `memory_timeline` | Same filters as search: partial chronology and constraint diagnostics |
| `compose_story` | `id`, optional `offset`, `limit`: expand canonical story references |
| `organize_memory` | `action`, `key`, and reviewed `sources` for merge or `source` and `parts` for split |
| `import_memories` | `action`, configured `root`; apply also takes `path`, `revision`, optional `user`, `parts`; `offset`, `limit` page results |
| `memory_maintenance` | `action`: `status`, `receipt`, `recover`, `abandon`, or `rebuild`; receipt/abandon use `key` |

Initialization supplies server instructions and capabilities. Clients discover
tool descriptions and input schemas through `tools/list`; they do not need an
extra application-specific schema tool before capturing memories.

`memory://guide` provides workflow instructions. The resource template
`memory://records/{id}/{offset}` returns a JSON source page containing raw canonical
record text (or base64), its revision, and continuation information. `read_memory`
also supplies parsed record diagnostics.

The JSON requests below are **`tools/call` parameters**, not complete JSON-RPC
messages to paste into the process's stdin. An MCP client wraps and sends them.
Replace uppercase placeholders with returned IDs, keys, and revisions. Response
examples show selected fields from `structuredContent`; other fields are omitted
for readability. Text content contains a short status summary.

## Users

Memories and stories require a `user`; entities and topics are shared. The
`save_memory` argument `user` supplies the default for new memories/stories, and
an individual change's `fields.user` can override it. Updating an existing record
keeps its user unless explicitly changed. A missing, empty, or `all` owner cannot
be saved.

`prepare_memory`, `search_memory`, `explore_memory`, and `memory_timeline` accept
`user` as a filter. A concrete user selects their memories/stories plus shared
entities/topics. `user:"all"`, or omitting the filter, selects everyone. Any caller
can choose any user or `all`; there is no access-control layer. Direct ID reads,
retained receipts, and explicit story compositions remain unrestricted.

Search/timeline summaries and canonical body/source pages identify the owning
user. A composed story's `user` identifies the story's owner; explicit references
may include other users' memories. Long display labels are abbreviated with
truncation flags; source pages retain the complete metadata.

Splits inherit the source user unless a part's fields override it. Merges inherit
it when all sources share a user; cross-user merges need an explicit `fields.user`.
Imports use top-level `user` as the default; part `fields.user` can override it.
No user is inferred from the text or assigned to existing files automatically.

## Prepare and save

Start by checking existing records:

```json
{"name":"prepare_memory","arguments":{"account":"I had coffee with John.","user":"ari"}}
```

Read `data.matches`, `data.ambiguities`, and `data.possible_duplicates`. They are
evidence to review, not automatic identity assignments. `data.key` identifies this
capture. If John is genuinely new, submit the related records together:

```json
{
  "name": "save_memory",
  "arguments": {
    "key": "CAPTURE_KEY",
    "user": "ari",
    "account": "I had coffee with John.",
    "changes": [
      {"ref":"john","kind":"entities","fields":{"name":"John"}},
      {
        "ref":"episode",
        "fields": {
          "title":"Coffee with John",
          "relations":[{"type":"with","target":"$john"}]
        },
        "body":"I had coffee with John."
      },
      {"ref":"story","kind":"stories","fields":{"title":"Coffee meetings"}}
    ],
    "stories":[{"story":"$story","members":["$episode"]}]
  }
}
```

`ref` labels are local to this request; `$john` becomes the generated entity ID.
Kinds are `memories` (the default), `entities`, `topics`, or `stories`. Stories and
classification are optional. An existing John should use his reviewed ID in the
relationship instead; several plausible Johns should remain unresolved.

A committed capture returns an envelope shaped like:

```json
{
  "status":"ok",
  "data": {
    "key":"CAPTURE_KEY",
    "status":"committed",
    "refs":{"john":"JOHN_ID","episode":"EPISODE_ID","story":"STORY_ID"},
    "records":[
      {"id":"JOHN_ID","revision":"JOHN_REVISION"},
      {"id":"EPISODE_ID","revision":"EPISODE_REVISION"},
      {"id":"STORY_ID","revision":"STORY_REVISION"}
    ]
  }
}
```

Keep the request key and exact arguments until the outcome is known. Accounts
remain in durable journals; affected memories also retain `original_accounts`.

## Search, read, and update

Search by words, or omit `text` to list records. `kind` uses directory names:

```json
{"name":"search_memory","arguments":{"user":"ari","text":"coffee","kind":"memories","limit":8}}
```

Results are in `data.matches`, with IDs, revisions, excerpts, and diagnostics.
Retired records are hidden unless `include_retired` is true. Read the body or
complete Markdown source before editing:

```json
{"name":"read_memory","arguments":{"id":"EPISODE_ID","view":"source","offset":0,"limit":4096}}
```

```json
{
  "status":"ok",
  "data": {
    "id":"EPISODE_ID",
    "revision":"CURRENT_REVISION",
    "text":"---\ntitle: Coffee with John\n...\n---\n\nI had coffee with John.",
    "offset":0,
    "truncated":false
  }
}
```

The source text above is abbreviated. Omitting `view` reads only the body, while
still returning the committed file revision. A raw orphan file without a valid
record ID can be inspected with `source`, for example `memories/notes.md`.

For a separate update, use a fresh operation key (one returned by a new
`prepare_memory` call works). Supply the reviewed revision:

```json
{
  "name":"save_memory",
  "arguments": {
    "key":"UPDATE_KEY",
    "account":"John brought his blue guitar.",
    "changes":[{
      "id":"EPISODE_ID",
      "revision":"CURRENT_REVISION",
      "append":"John brought his blue guitar."
    }]
  }
}
```

Omitting `body` preserves it; an explicit `body` replaces it. `fields` patches
metadata without removing omitted fields. `add_relations` appends relationships
without replacing existing ones. A successful update returns its new revision.

If another edit changed the source first, the outer status is `conflict` and
`isError` is true. Inspect the request receipt before doing anything else. If it
is `not_accepted`, read the current source, reconcile the changes, and submit a
new reviewed request under a fresh key. If it was accepted, follow
[recovery](#retries-and-recovery); changing arguments under an accepted key is blocked.

## Relationships, timelines, and stories

Find incoming and outgoing connections, including derived story membership:

```json
{"name":"explore_memory","arguments":{"user":"ari","id":"EPISODE_ID","direction":"both","depth":1}}
```

`data.edges` contains `source`, `type`, `target`, and `resolved`. Directions are
`incoming`, `outgoing`, or `both`; `type` filters the edge name. `resolved` means
the target is available, not that the relationship is true. Split redirects can
remain ambiguous; no inverse relationship or symmetry is invented.

```json
{"name":"memory_timeline","arguments":{"user":"ari","kind":"memories","text":"coffee"}}
```

The result has `data.records` and `data.constraints`. Only `before`/`after`
relationships and stated `time.earliest`/`time.latest` bounds establish chronology.
For example, metadata can include:

```yaml
time:
  description: the winter I moved away
  earliest: 2006-12-01
  latest: 2007-03-31
```

All fields are optional. Unknown order remains unknown; diagnostics explain
contradictions. Record creation/update times are never treated as episode dates.

Compose the story created during capture:

```json
{"name":"compose_story","arguments":{"id":"STORY_ID","offset":0,"limit":4096}}
```

Story bodies use exact `[[record_id]]` references; new batch bodies can use
`[[$episode]]`. Composition expands canonical bodies and nested stories without
storing narrative copies. Read `data.text` or `data.base64`, and follow `next` when
`truncated` is true. Missing, cyclic, and ambiguous references remain visible.

## Merge and split

These are explicit reviewed operations. Read every source first and use its
current revision. Choose a fresh key for each operation and keep it for retries.

```json
{
  "name":"organize_memory",
  "arguments": {
    "action":"merge",
    "key":"MERGE_KEY",
    "sources":[
      {"id":"FIRST_ID","revision":"FIRST_REVISION"},
      {"id":"SECOND_ID","revision":"SECOND_REVISION"}
    ],
    "fields":{"title":"Coffee meetings"}
  }
}
```

The receipt uses the same envelope as `save_memory`; `data.refs.merged` is the new
canonical ID. By default its body combines the originals and retains aliases and
relationships. An optional `body` supplies a reviewed replacement narrative.
Original sources remain readable under their retired IDs and redirect to the new
record. Refresh its revision before splitting:

```json
{
  "name":"organize_memory",
  "arguments": {
    "action":"split",
    "key":"SPLIT_KEY",
    "source":{"id":"MERGED_ID","revision":"MERGED_REVISION"},
    "parts":[
      {"fields":{"title":"Coffee with John"},"body":"I had coffee with John."},
      {"fields":{"title":"Another meeting"},"body":"The other reviewed account."}
    ]
  }
}
```

The receipt maps `part_1`, `part_2`, and so on to replacement IDs. The original
redirects to all parts. Existing backlinks and stories remain explicitly
ambiguous until reviewed; the engine does not pick a replacement for them.

## Imports

Add named source directories to the server's client configuration. Import roots
are optional; repeat the flag for additional roots:

```json
{
  "args":[
    "--store","/absolute/path/to/memory-store",
    "--import-root","notes=/absolute/path/to/reviewed-sources"
  ]
}
```

Clients select configured names, not arbitrary filesystem roots. Store access is
confined to its root; imports read the separately configured source root. Keep
import sources outside the active store. Source files are never rewritten.

Preview, then read the selected file using `path` and `revision` from `data.files`:

```json
{"name":"import_memories","arguments":{"action":"preview","root":"notes"}}
```

```json
{"name":"read_memory","arguments":{"root":"notes","source":"old.md","offset":0,"limit":4096}}
```

After reviewing all source pages, import the whole file verbatim:

```json
{"name":"import_memories","arguments":{"action":"apply","user":"ari","root":"notes","path":"old.md","revision":"IMPORT_REVISION"}}
```

Or, as an **alternative** to whole-file import, supply reviewed parts. For a file
whose exact bytes are `First.\nSecond.\n`, a complete partition is:

```json
{
  "name":"import_memories",
  "arguments": {
    "action":"apply","user":"ari","root":"notes","path":"old.md","revision":"IMPORT_REVISION",
    "parts":[
      {"start":0,"end":7,"fields":{"title":"First"}},
      {"start":7,"end":15,"fields":{"title":"Second"}}
    ]
  }
}
```

Offsets count bytes, with `start` inclusive and `end` exclusive. Every explicit
part needs both bounds. Review the actual source; never reuse illustrative
offsets for a different file. No automatic splitting, inferred dates, or identity
assignment occurs.

The result includes `status`, `batch_key`, `archive`, `parts` (IDs and ranges),
`coverage_complete`, and any `uncovered` substantive ranges. The original is
preserved even when coverage is incomplete. Inspect it with:

```json
{"name":"read_memory","arguments":{"archive":"ARCHIVE_CHECKSUM","offset":0,"limit":4096}}
```

One canonical source-root/path pair identifies one import operation. Retry the
same arguments to recover its mapping and IDs, including after source removal.
An accepted partition cannot be changed by importing again; use reviewed merge,
split, or updates. For a retained import, inspect its `batch_key` as a receipt.

## Retries and recovery

The outer `status` is `ok`, `blocked`, or `conflict`. A write receipt has its own
status: `not_accepted`, `pending`, `blocked`, `committed`, `abandoning`, or
`abandoned`. Read both. `isError` does not mean accepted work was lost.

After a lost response, inspect the original key:

```json
{"name":"memory_maintenance","arguments":{"action":"receipt","key":"CAPTURE_KEY"}}
```

If committed, keep its returned IDs; retrying the exact original request also
returns the original receipt without overwriting later edits. If pending, retry
the same request or trigger recovery:

```json
{"name":"memory_maintenance","arguments":{"action":"recover"}}
```

Recovery also runs when opening the store. Recheck the receipt afterward. If
blocked, inspect diagnostics, the current source, and retained bytes:

```json
{"name":"read_memory","arguments":{"key":"CAPTURE_KEY","id":"EPISODE_ID","view":"proposed"}}
```

Use `view:"before"` for the original record or `view:"account"` for the raw account
(no ID needed). Review before restoring a base and recovering. To deliberately
withdraw an uncommitted operation, use `memory_maintenance` with `action:"abandon"`
and its key. Withdrawal preserves proposals and later user edits; it can resume
from `abandoning`. A committed batch cannot be abandoned, and an abandoned key
never publishes again. Subsequent changes need a new reviewed request.

A `not_accepted` receipt is not proof that every preparation byte is gone. Retain
the key and exact arguments for an unchanged retry. Never switch keys merely
because a response was lost or publication was interrupted.

`memory_maintenance` with `action:"status"` reports store diagnostics and configured
import-root names. `action:"rebuild"` refreshes the disposable index; it does not
repair authoritative Markdown or transaction journals. Unconfirmed records stay
readable but carry no committed revision; inspect and repair their retained
coordination before updating them.

## Paging and limits

| Result | Default page | Maximum page |
| --- | --- | --- |
| Search, preparation, timeline, import preview | 8 items | 8 items |
| Graph | 10 edges | 20 edges |
| Receipt records, import mapping parts | 10 items | 20 items |
| Record/source/archive/proposal reads, raw receipt reads | 2 KiB | 4 KiB |
| Story composition | 2 KiB | 4 KiB |

Use the returned `next` as the following `offset` while `truncated` is true. Text
offsets count bytes; list offsets count items. UTF-8 pages retain whole characters;
invalid UTF-8 bytes use `base64`. Separate pages reflect a live view, not a fixed
snapshot. Preparation returns a fresh key each time, so keep the first key if
fetching more candidate pages through search.

Graph depth defaults to 1 and caps at 5. Stories retain markers beyond depth 32
and allow 100,000 reference visits per render. Extreme virtual story offsets can
report a range error; retrieve a nested story separately if necessary.

Summaries abbreviate descriptions and diagnostics. Record reads return at most
four diagnostics, each at most 1 KiB; they are not a complete diagnostic listing.
Other summaries expose truncation flags where available. Read source pages for
full metadata. The structured envelope has a 28 KiB budget; an oversized
response becomes an explicit compact summary without changing a write's durable
outcome. Receipt references are limited to records on the selected page. Large
keys/labels may be omitted with explicit flags. Page the full JSON receipt with:

```json
{"name":"memory_maintenance","arguments":{"action":"receipt","key":"CAPTURE_KEY","view":"source","offset":0,"limit":4096}}
```

Keep the entire store, including journals and pending directories, in
[backups](../README.md#files-and-backups). Only `.index/` is disposable. The engine
does not automatically commit or push user memories to Git.
