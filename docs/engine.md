# Engine reference

[Getting started](../README.md) · [MCP guide](mcp.md)

The Go package is `github.com/aristath/songya`, imported as `songya`.
This reference describes the stored format, public operations, and durability
limits. For executable Go examples, see [example_test.go](../example_test.go).

- [Files](#files)
- [Users](#users)
- [Documents](#documents)
- [Store](#store)
- [Updates](#updates)
- [Durable writes and recovery](#durable-writes-and-recovery)
- [Discovery and search](#discovery-and-search)
- [Graph, episode time, and stories](#graph-episode-time-and-stories)
- [Related changes and retry safety](#related-changes-and-retry-safety)
- [Organization and import](#organization-and-import)

## Files

```text
memory-store/
  memories/m_<12-character-suffix>.md
  entities/e_<12-character-suffix>.md
  topics/t_<12-character-suffix>.md
  stories/s_<12-character-suffix>.md
  .index/
  .batches/
  .imports/
  .lock
```

The filename is the record's identity. IDs are a type prefix followed by 12 random
lowercase letters or digits, and are returned as `Document.ID`, never written as
an `id` metadata field. A record contains YAML frontmatter and a Markdown body:

```markdown
---
title: Example
user: ari
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

## Users

One store contains records for every user. Memories and stories **must** have a
nonempty `user` field identifying one user; `all` is reserved for queries. Numeric
scalar values such as `3` normalize to the string `"3"`. Entities and topics are
shared and do not receive an owner automatically.

`Query.User` filters search and timelines. `Prepare(ctx, account, user, limit)`
uses the same scope for candidates, duplicate suggestions, and ambiguities.
`GraphQuery.User` selects edge sources for traversal and backlinks; shared source
records remain available, and references still report their actual target
availability. A specific user sees their memories/stories and shared entities and
topics. `"all"`, or an omitted filter, selects everyone. Pagination follows this
filtered view.

This is selection, not authorization. Anyone can select another user or `all`,
read an exact record ID, compose an explicitly selected story, or change a record's
user. There are no administrator checks or ownership permissions.

`Create` requires `fields["user"]` for memories/stories. Updates preserve the
current user when omitted, and cannot clear it. `BatchRequest.User` supplies a
default for newly created memories/stories; each change's explicit `fields.user`
takes precedence. User IDs are literal values, not `$ref` record references.
Shared entity/topic changes are unaffected. Ownership is part
of the accepted batch intent, so changing it under an accepted key is blocked.

Splits inherit the source user unless a part explicitly supplies another one.
Merges inherit the user when every source has that user; a cross-user merge needs
an explicit `fields.user`. `ImportRequest.User` provides the default for imported
memories/stories; reviewed part fields can override it. Imported originals are
preserved verbatim.

Required ownership is checked before new writes are accepted. If an external edit
or damaged file removes the field, the original remains readable with diagnostics;
reads never invent an owner or rewrite the source. Updating it needs an explicit
valid user. No existing records are migrated or assigned automatically.

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
the entire original is retained as the body. Reads do not silently rewrite original files.

Standalone parsed documents have no ID; the store derives it from the filename.
Metadata inputs are data: scalars, maps, lists, and raw YAML nodes. Pointers to
these values, named scalar types, `time.Time`, `time.Duration`, `Relation`,
`Metadata`, and exact `json.Number` values are supported. Go struct tags and
custom encoding callbacks belong outside the engine; callers can convert such
values to a YAML node first.

An unsupported value or cyclic Go reference becomes a `!songya/issue` scalar at
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
  The engine generates the ID and both UTC timestamps. Memories/stories require
  `fields.user`; entities/topics are shared. A supplied `id` is ignored.
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

See the [executable examples](../example_test.go). `.lock` coordinates engine processes
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
stored in YAML. Pending creations have no committed revision. If a damaged batch
journal prevents verification, readable canonical bytes remain available with
`Unconfirmed=true`, diagnostics, and no committed revision. Healthy records remain
usable. Unconfirmed redirects and dates do not establish canonical identities or
chronology; graph/story results retain that qualification. A successful update
returns the new revision; use it for the next update.

An update checks the base revision before staging and again immediately before
replacement, while engine writers coordinate through `.lock`. A stale revision
returns `ErrConflict`; use `errors.Is(err, songya.ErrConflict)` to identify it.
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
Empty reservations, ambiguous candidates, incomplete data, and conflicting data
are retained and reported. Recovery never promotes an unverified partial file or
overwrites a conflicting record. Readable partial staging data remains available
with `Pending=true` and diagnostics.
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

The store requires a local filesystem with hard links, advisory locks, and
directory syncing. It has no general record-deletion operation. Explicit
abandonment can remove its own uncommitted creations; the journal retains their
complete proposals.

## Discovery and search

`Scan(ctx)` returns the derived catalogue, including readable records with damaged
metadata or filenames. An orphan has a source path and no fabricated ID;
`ReadSource(path)` makes its original bytes inspectable. Scanning does not repair
or rewrite source files. `Search(ctx, Query)` is the bounded client-facing view.
Readable single-record creations retained in staging remain discoverable with
`pending: true` and no committed revision. New batch records stay hidden until
the entire batch commits.

Every query reads and hashes the current source bytes. The index caches parsing,
not filesystem freshness: edits and removals are reflected on the next query.
`Rebuild(ctx)` bypasses cached parsing entirely and writes a fresh derived view.
Deleting or corrupting `.index/` rebuilds it from Markdown. Cache format changes
also discard cached interpretations rather than migrating authoritative files.
An unavailable cache is a diagnostic; it cannot roll back a successful write.
This deliberately simple strategy reads the store on each query; it is not intended as a large remote
search service.

Search ranks title/name and alias matches, descriptions, prose, and episode time.
Use `kind`, `related_to`, and `relation_type` to restrict results. The relationship
filter matches stored outgoing targets; graph queries also resolve redirects.
Retired records are excluded unless `include_retired` is true. Results have
source paths, revisions, excerpts, and diagnostics. Engine list/search/graph pages
default to 10 items and cap at 50; follow `next` while `truncated` is true.
Pagination is over the current view. A direct edit between pages can change that
view, so refresh rather than treating offsets as a permanent snapshot.

## Graph, episode time, and stories

`Graph(ctx, GraphQuery)` supplies source-owned edges and derived backlinks. Use
`direction` incoming/outgoing/both, optional `type`, and a traversal `depth`
(default 1, maximum 5). No inverse relationship or symmetry is invented.
`entities`, `topics`, and topic `broader` lists are also exposed as edges named
for their metadata fields. Exact story references produce derived `contains`
edges, so a memory's backlinks also identify its containing stories. These edges
are not copied into source metadata. A retired starting ID resolves to its sole
known replacement; ambiguous replacements remain explicit. `Resolve(ctx, id)` exposes
the full redirect result. Missing or cyclic targets remain inspectable.
An edge's `resolved` flag concerns target availability, not the truth of a
relationship. Additional qualifiers remain in the source. Use an open name such
as `possibly_before` for uncertain ordering; only explicit `before`/`after`
claims participate in chronology.

Only `before` and `after` have temporal meaning. `Timeline(ctx, Query)` returns
records and constraints, not a guessed sorted biography. Episode metadata is:

```yaml
time:
  description: "the winter I moved away"
  earliest: 2006-12-01
  latest: 2007-03-31
```

Each field is optional. Bounds accept ISO dates, RFC3339 timestamps, or UTC
`YYYY-MM-DD HH:MM:SS`; an upper date includes its whole day. An unrecognized value
remains source data. Creation/update timestamps never become episode dates.
Explicit ordering is checked globally for cycles and incompatible date ranges.
Conflicting constraints are returned as unusable with diagnostics. Invalid date
ranges are ignored for inference without disabling independent relative order.
Date-proven constraints are derived between displayed records without
materializing every pair in the entire store. Displayed times remain the original stated bounds;
validation does not write inferred dates into records.

Stories use exact `[[m_0123456789ab]]` references (any valid record prefix works).
Ordinary Markdown links are ordinary prose. `Compose(ctx, storyID, offset, limit)`
expands canonical bodies, and nested stories, in narrative order. It never stores
copies of those narratives. Missing references have visible markers; ambiguous
redirects retain the retired account and identify replacements. Cycles and depth
beyond 32 retain a reference marker. One render allows 100,000 reference visits;
if that is exceeded, retrieve a nested story separately. Completed expansion
lengths skip shared prefixes when paging larger compositions. Output defaults to
4 KiB and caps at 16 KiB, with byte offsets and continuation. Unicode pages preserve
whole characters, even if that requires up to three bytes beyond a tiny requested
limit. Binary fragments use `base64` instead of losing bytes through JSON text.

Graph/timeline summaries bound descriptive fields and mark `details_truncated`.
Search summaries likewise abbreviate details; read the source for full metadata.
These display limits never change authoritative content or relationship semantics.

## Direct capture and domain writes

`SaveMemory(ctx, MemoryInput)` accepts text, time, entities and topics. It resolves
unique exact names/aliases and creates missing identities under the same store
lock as the durable related write. Explicit IDs select known records. Ambiguous
or unavailable connections remain in `unresolved_connections`; the memory and
healthy connections still save. Entity descriptions briefly identify their
subjects. Experiences, preferences and growing histories belong in memories linked
to every relevant entity. Use an existing memory ID and `Append` to enrich its
prose while preserving its connections; full prose is retained independently of
bounded MCP previews.

`SaveNamed(ctx, Entities|Topics, NamedInput)` creates or updates a named record.
`SaveStory(ctx, StoryInput)` creates or updates a story with ordered members.
Supplied members replace membership; omission preserves it. Supplied text replaces
prose; `Append` adds detail. `Metadata` is open data. A supplied internal `Key`
binds a retry to the same intent; omitting it starts an independent operation.
These methods use the same batch journals and recovery as the lower-level API.
Their `Relations` field replaces explicit links when supplied; nil preserves them
and an empty slice clears them. Targets accept canonical IDs or entity names;
unique names are reused and missing entities are created under the write lock.
Unresolved targets remain intact with diagnostics. Capture `Entities` and `Topics`
add participant links to the memory. Lower-level `Change.AddRelations` remains
explicitly additive.

## Related changes and retry safety

`Prepare(ctx, account, user, limit)` searches all record types together and returns a
fresh request key, shortlists, possible duplicate names, and identity ambiguities.
It uses deterministic lexical matching, with no LLM calls. A shortlist is evidence
to review, not an automatic identity assignment.

`ApplyBatch(ctx, BatchRequest)` accepts creations, updates by ID, and
story membership changes in one request. New changes can declare a `ref`; use
`$ref` values in metadata or `[[$ref]]` in body references. The engine assigns
final IDs. Existing changes supply `id`. Go callers may supply `Revision` to
check a previously read snapshot; MCP callers use IDs only. Omitted fields remain
intact; omitted `body` preserves it. `append` adds a paragraph, while
`add_relations` appends source-owned edges without discarding existing entries.
Story additions use an existing story ID, or its new `$ref`, and an
ordered member list. Unknown story IDs/references genuinely block safe changes;
unresolved ordinary relationships can remain in source metadata.

Supply one stable `key` and reuse exactly the same request on retry. Different
changes under that key are blocked. Keys bind the accepted intent; they do not
expire or become reusable after organization. Accounts remain in the journal and
in affected memories' `original_accounts`; they are never silently discarded if
capture creates only other record types.

The batch boundary is:

1. Before acceptance, prepare and check every change. No authoritative record is
   published by a rejected preparation.
2. Acceptance means the checksum-verified journal and its containing directories
   have been synced. It retains the original account, original/proposed bytes,
   allocated IDs, and request fingerprint under `.batches/`.
3. Reserve every affected record, then publish and sync each file. Until a commit
   marker exists, engine reads expose the prior view; new records stay hidden.
   Catalogue scans coordinate with writers to avoid mixing batch views.
4. After all proposed files are verified and synced, sync the commit marker.
   Cleanup failure becomes a diagnostic, not a failed capture. Retrying committed
   work returns the original receipt without replacing later edits.

Canonical files are published individually: raw filesystem readers can observe
intermediate versions. Separate individual reads are not one snapshot. External
editors do not acquire the advisory lock; a conflicting edit is exposed and
preserved, rather than hidden behind the old view. The final compare/rename race
with an uncooperative editor also applies to batch publication and withdrawal.

Receipts distinguish `not_accepted`, `pending`, `blocked`, `committed`, `abandoning`,
and `abandoned`. `BatchStatus(ctx, key)` inspects accepted work. Errors before confirmed
acceptance can still leave retained preparation; reuse the same key and inspect
rather than creating a duplicate under a different key. Recovery verifies journals
and content before resuming; incomplete, corrupt, or conflicting work is retained
and diagnosed independently of healthy records. After durable acceptance,
cancellation does not discard the work: publication may finish or be resumed on
open/recovery. A lost response is resolved through the same key. A read crossing a concurrent withdrawal may report a transient revision conflict;
retrying obtains a stable view rather than exposing an uncommitted proposal.

`BatchData(ctx, key, id, view)` reads retained `before`, `proposed`, or `account`
bytes. To resolve a blocked batch, review these and the current record. Restore a
reviewed base and recover, or explicitly call `AbandonBatch(ctx, key)` to withdraw
an uncommitted batch. Withdrawal itself is durable and resumable (`abandoning`
until complete). It restores only bytes matching this batch's publication,
preserves later edits/deletions, and retains every proposal. A committed batch
cannot be withdrawn. An abandoned key is terminal; retries never publish it again.
Use a new reviewed, revision-checked request for subsequent changes.

Keep `.batches/`, `.imports/`, and pending directories alongside authoritative
records when backing up unfinished work. `.index/` alone is disposable. There are
no automatic Git commits or pushes of user memories.

## Organization and import

`Merge(ctx, MergeRequest)` takes reviewed source IDs (with optional Go revision
checks), creates a new
canonical record, and retires sources through redirects. By default it preserves
their bodies in a combined account and carries names/aliases and relationships.
An explicit replacement narrative is a caller's reviewed choice. Original source
bodies remain available under retired IDs.

`Split(ctx, SplitRequest)` takes a reviewed source and explicit replacement parts.
The original redirects to all replacements. Backlinks cannot safely choose one;
that ambiguity stays visible until a reviewed relationship update resolves it.
Merge/split requests use the same batch keys and retry guarantees. They are
explicit operations, not automatic inference from shared words or names.

Imports are separate from normal capture. Configure source roots explicitly.
`PreviewImports(ctx, sourceRoot, offset, limit)` lists Markdown sources with exact
byte revisions. Read and review them, then call `Import(ctx, sourceRoot, request)`
with optional exact byte ranges (`start` inclusive, `end`
exclusive). Omitting parts imports the whole file verbatim. No automatic splitting,
identity assignment, rewriting, or inferred dates occur.

The importer preserves a checksum-named original under `.imports/` before applying
an idempotent batch. It returns source-to-record mappings and uncovered substantive
byte ranges. Non-whitespace gaps make `coverage_complete` false; omitted material
remains in the original. `ReadArchive(checksum)` verifies and reads that archive,
even after the external source is gone. Retries use the preserved source and the
same record IDs. Changing an accepted partition requires reviewed organization,
not importing a second active copy under the same path. Source roots remain
separate from the active store; source files are never rewritten.
