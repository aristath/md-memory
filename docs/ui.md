# Standalone UI development

The `songya-web` command serves Go templates, CSS and an Alpine.js component over
Go's standard HTTP server. `web.Handler` is also available to Go applications.
Its HTTP handlers call the Songya engine directly. The MCP is independent.

The front page starts with a 50vh graph on a black background. Its 3D / 2D toggle
switches between locally bundled [3d-force-graph](https://github.com/vasturiano/3d-force-graph)
and [force-graph](https://github.com/vasturiano/force-graph). Entities are
green, memories amber, topics white and stories red; nodes grow with their incoming
and outgoing connection counts. Hover shows the name/title, and clicking opens
read-only inspection. The graph shows the whole store independently of table
filters and refreshes after saves and deletions. No CDN is required.
Its search matches names/titles, aliases and IDs, highlighting matches while
dimming other points. Select a result or press Enter to move the camera to it;
click the point to inspect. Clear the search or press Escape to restore the overview.
Search is retained when switching views. The 2D view works without WebGL;
the table and editors also remain usable if graph rendering is unavailable.

Below it is a sortable, filterable table with title, description, record
kind, metadata type, aliases, owner and row actions. The Go catalogue supplies
metadata and the exact revision used for row deletion. Full-content search and
ownership selection use engine queries; column filters and sorting refine the
returned rows without changing the records.

Titles open read-only inspection; Edit and New record open a native modal dialog.
The browser keeps focus inside it. Close, Escape or clicking the backdrop retains
unsaved drafts locally without a server request; reopening a record restores them.
The background table retains its filters and sorting while a modal is open.
Table deletion uses the same engine checks as the editor; blocked deletion opens
the record with navigable referring records.

The compiled executable embeds `web/assets`, including the generated JavaScript,
editor styles and third-party license notices. Builds and tests use no live store.

## Build and check

Go alone builds the application from the checked-in assets:

```sh
go build -o songya-web ./cmd/songya-web
./songya-web --store /path/to/store --listen 127.0.0.1:8664
```

To change frontend sources, install Node and npm and work in `web`:

```sh
cd web
npm ci
npm run build
npm run format:check
npx playwright install chromium
npm test
```

`npm test` rebuilds the bundle, compiles the standalone Go executable into a
temporary directory, starts it against a disposable store, and uses real Chromium
for desktop and phone workflows. It removes its store and stops the process on
completion. Screenshots are written to `/tmp/songya-ui-review`.

Run the complete Go/MCP suite from the repository root:

```sh
go test -race -count=1 ./...
go vet ./...
```

Commit generated `web/assets/app.js`, `editors.css`, `NOTICE.txt`, and any emitted
legal-comment file along with frontend source changes. The asset builder uses
esbuild; Alpine, CodeMirror and ProseMirror are direct, framework-independent
libraries. Package versions and the lockfile are committed.

## Editing and durability

The browser sends source drafts to Go for parsing and metadata/relationship edits.
It does not maintain another memory format or its own relationship graph.
Unchanged YAML values retain their nodes, including aliases and custom values.
Common scalar controls are conveniences; custom and nested values remain editable.

`Store.SaveSource` uses the existing batch journal and a stable write key. It
replaces the full document while preserving the engine's identity/clock rules.
The read snapshot's revision prevents silent overwrites by concurrent writers.
Retries return the original committed proposal, not an unrelated later snapshot.
The UI resolves a lost acknowledgement before publishing newer draft edits. A
request rejected before persistence can be corrected and submitted again.

`Store.Delete` checks current and pending references under the engine's exclusive
cross-process writer lock. It removes only the requested, unchanged record and
syncs the directory before reporting success. It refuses an incomplete incoming
link check and unfinished writes that might republish the record. Completed batch
receipts never republish deleted records during recovery. It adds no trash,
revisions or soft-delete mechanism, and it does not erase historical transaction
journal payloads.

The interface uses explicit Save. Unsaved work stays in the tab when moving
between records; failed operations retain it. Closing the tab discards drafts
after the browser's unsaved-work warning. This is not a durable draft/history store.

The loopback default is intended for a trusted local operator. The listening
address is configurable, but the application has no login system. Cross-origin
browser mutations are rejected. No account-management or deployment system is
included.

A file containing invalid UTF-8 bytes cannot be safely round-tripped through a
text editor. Opening it reports an encoding problem and leaves its bytes intact;
other records remain usable. Repair that file's encoding before editing it here.
