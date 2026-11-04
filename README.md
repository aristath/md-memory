# sōngyā Memory Engine

A memory engine that keeps connected records in Markdown files. Use it from an
MCP client or as a Go library. Memories, entities, topics, and stories share one
store, with open metadata and relationships.

Durability comes first: preserve original content, recover interrupted writes,
and keep healthy records usable when something else is damaged. Search, graph
traversal, partial timelines, and story composition run locally without a model.

The repository and Go module use `github.com/aristath/songya`; the MCP
executable is `songya-mcp`.
There is no Clara dependency or integration.

## Start with MCP

Install Go at the version required by [go.mod](go.mod) or newer. Go's automatic
toolchain selection can download the required version. The store needs a local
filesystem with hard links, advisory locks, and directory syncing.

```sh
git clone https://github.com/aristath/songya.git
cd songya
go install ./cmd/songya-mcp
```

Go puts the executable in `GOBIN` when configured, otherwise in `bin` under the
first `GOPATH` directory (normally `$HOME/go/bin`). Check your paths with:

```sh
go env GOBIN GOPATH
```

Add the server to your MCP client's configuration, replacing both paths below
with absolute paths on your machine. Clients have their own configuration file
locations; this is the common `mcpServers` shape:

```json
{
  "mcpServers": {
    "songya": {
      "command": "/absolute/path/to/songya-mcp",
      "args": ["--store", "/absolute/path/to/memory-store"]
    }
  }
}
```

The server creates missing store directories. Your client launches it over stdio;
it needs no network port, authentication service, or inference model. Running the
executable directly waits for MCP protocol messages on stdin.

After connecting, ask your client to prepare and save a memory, for example:

> Remember that I had coffee with John today. Check existing records first and
> preserve any uncertainty about which John I mean.

The normal capture flow uses `prepare_memory`, then `save_memory` with the returned
key. Read the [MCP guide](docs/mcp.md) for the ten tools, concrete requests,
responses, retry handling, and optional imports.

## Use the Go library

From your application's Go module (for a new module, run
`go mod init example.com/songya-example` first):

```sh
go get github.com/aristath/songya
```

Save this as `main.go` and run `go run .`:

```go
package main

import (
	"fmt"
	"log"

	"github.com/aristath/songya"
)

func main() {
	store, err := songya.Open("./memory-store")
	if err != nil {
		log.Fatal(err)
	}
	saved, err := store.Create(songya.Memories,
		map[string]any{"user": "ari", "title": "Coffee with John"}, "I had coffee with John.")
	if err != nil {
		log.Fatal(err)
	}
	record, err := store.Read(saved.ID)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(record.ID, record.Body)
}
```

Each run creates a new record. For revision-checked updates and retry-safe related
changes, see the [executable examples](example_test.go) and
[engine reference](docs/engine.md).

## Files and backups

Records live in `memories/`, `entities/`, `topics/`, and `stories/`. Each Markdown
filename is its identity: a type prefix (`m_`, `e_`, `t_`, or `s_`) plus 12 random
lowercase letters or digits. Metadata is YAML frontmatter; the body is Markdown.
The engine assigns IDs and timestamps. No duplicate ID field is needed in YAML.
Memories and stories require a `user` field. Entities and topics are shared.
Select a user when searching, or use `user:all` to query everyone; this is grouping,
with no access restrictions. See [users](docs/engine.md#users).

Back up the **whole store**, including hidden files and directories. `.batches/`
retains transaction journals, original accounts, and before/proposed bytes;
`.imports/` retains imported originals and mappings. Hidden pending directories
retain unfinished writes. Only `.index/` is disposable.

For a consistent backup, stop writers and external editors while copying, or use
an atomic filesystem snapshot. Restore into a directory on a supported local
filesystem, then open that directory with the library or `--store`; recovery runs
automatically. Keep `.lock` in place while a store is in use.

See [durability and recovery](docs/engine.md#durable-writes-and-recovery) for what
pending writes mean and [batch recovery](docs/mcp.md#retries-and-recovery) for
inspecting unfinished work.

## Development

From the repository root:

```sh
go test -race -count=1 ./...
go vet ./...
go build -o /tmp/songya-mcp ./cmd/songya-mcp
/tmp/songya-mcp --help
```

The stdio tests build and launch the real server with the official MCP client.
To instrument those child executables with the race detector too:

```sh
GOFLAGS=-race GORACE=atexit_sleep_ms=0 go test -count=1 ./mcpserver
```

Tests use temporary stores and import fixtures. Project priorities are in
[AGENTS.md](AGENTS.md).
