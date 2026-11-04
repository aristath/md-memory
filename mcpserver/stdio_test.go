package mcpserver_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/aristath/songya"
	"github.com/gofrs/flock"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func binary(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "songya-mcp")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "go", "build", "-o", path, "../cmd/songya-mcp")
	if data, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, data)
	}
	return path
}
func connect(t *testing.T, ctx context.Context, bin, store, source string) *mcp.ClientSession {
	t.Helper()
	arguments := []string{"--store", store}
	if source != "" {
		arguments = append(arguments, "--import-root", "notes="+source)
	}
	command := exec.CommandContext(ctx, bin, arguments...)
	command.Stderr = os.Stderr
	client := mcp.NewClient(&mcp.Implementation{Name: "acceptance-client", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: command}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}
func invoke(t *testing.T, ctx context.Context, session *mcp.ClientSession, name string, args any) (map[string]any, *mcp.CallToolResult) {
	t.Helper()
	if input, ok := args.(map[string]any); ok && (name == "save_memory" || name == "import_memories") {
		if _, supplied := input["user"]; !supplied {
			input["user"] = "test"
		}
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]any
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope, result
}
func payload(t *testing.T, envelope map[string]any) map[string]any {
	t.Helper()
	data, ok := envelope["data"].(map[string]any)
	if !ok {
		t.Fatalf("missing structured result: %+v", envelope)
	}
	return data
}

func TestStdioUserFiltersKeepEntitiesSharedAndRequireOwnedWrites(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	session := connect(t, ctx, binary(t), t.TempDir(), "")
	envelope, result := invoke(t, ctx, session, "save_memory", map[string]any{
		"user": "ari", "key": "ari-capture", "changes": []any{
			map[string]any{"ref": "john", "kind": "entities", "fields": map[string]any{"name": "John"}},
			map[string]any{"ref": "episode", "fields": map[string]any{"title": "Coffee with John"}, "body": "Ari's account"},
			map[string]any{"ref": "story", "kind": "stories", "fields": map[string]any{"title": "Coffee story"}},
		}, "stories": []any{map[string]any{"story": "$story", "members": []any{"$episode"}}},
	})
	if result.IsError || payload(t, envelope)["status"] != "committed" {
		t.Fatal(envelope)
	}
	refs := payload(t, envelope)["refs"].(map[string]any)
	for _, user := range []any{"bob", 3} {
		envelope, result = invoke(t, ctx, session, "save_memory", map[string]any{
			"user": user, "key": fmt.Sprint(user), "changes": []any{map[string]any{"body": "Another user's coffee"}},
		})
		if result.IsError {
			t.Fatal(envelope)
		}
	}
	for user, count := range map[string]int{"ari": 3, "bob": 2, "3": 2, "all": 5} {
		envelope, result = invoke(t, ctx, session, "search_memory", map[string]any{"user": user})
		if result.IsError || len(payload(t, envelope)["matches"].([]any)) != count {
			t.Fatalf("scope %s: %+v", user, envelope)
		}
	}
	envelope, result = invoke(t, ctx, session, "prepare_memory", map[string]any{"account": "coffee John", "user": "bob"})
	if result.IsError || len(payload(t, envelope)["matches"].([]any)) != 2 {
		t.Fatal("preparation did not combine scoped memories and shared identities", envelope)
	}
	envelope, result = invoke(t, ctx, session, "read_memory", map[string]any{"id": refs["story"], "view": "source"})
	if result.IsError || !strings.Contains(payload(t, envelope)["text"].(string), "user: ari") {
		t.Fatal("story did not persist the user", envelope)
	}
	envelope, result = invoke(t, ctx, session, "read_memory", map[string]any{"id": refs["john"], "view": "source"})
	if result.IsError || strings.Contains(payload(t, envelope)["text"].(string), "user:") {
		t.Fatal("shared entity received an owner", envelope)
	}
	envelope, result = invoke(t, ctx, session, "save_memory", map[string]any{
		"user": "", "key": "missing-user", "changes": []any{map[string]any{"body": "Not yet attributable"}},
	})
	if !result.IsError || payload(t, envelope)["status"] != "not_accepted" {
		t.Fatal("missing ownership was accepted", envelope)
	}
}

func TestActualStdioCaptureRetrievalOrganizationAndRestart(t *testing.T) {
	bin := binary(t)
	store, source := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "old.md"), []byte("An original imported narrative."), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	session := connect(t, ctx, bin, store, source)
	if info := session.InitializeResult().ServerInfo; info == nil || info.Name != "songya" {
		t.Fatalf("server identity: %+v", info)
	}
	tools, err := session.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil || len(tools.Tools) != 10 {
		t.Fatalf("tool discovery: %+v %v", tools, err)
	}
	resources, err := session.ListResources(ctx, &mcp.ListResourcesParams{})
	if err != nil || len(resources.Resources) == 0 {
		t.Fatal("guide resource unavailable")
	}
	templates, err := session.ListResourceTemplates(ctx, &mcp.ListResourceTemplatesParams{})
	if err != nil || len(templates.ResourceTemplates) == 0 {
		t.Fatal("record resources unavailable")
	}
	// These are the only two tool calls in ordinary capture. No schema, per-entity,
	// or separate ID lookup is required after preparation.
	prepared, result := invoke(t, ctx, session, "prepare_memory", map[string]any{"account": "John, also known as Johnny, is a gardener. I had coffee with him.", "limit": "3"})
	if result.IsError {
		t.Fatal(prepared)
	}
	key := payload(t, prepared)["key"].(string)
	request := map[string]any{"key": key, "account": "John, also known as Johnny, is a gardener. I had coffee with him.", "changes": []any{
		map[string]any{"ref": "john", "kind": "entities", "fields": map[string]any{"name": "John", "aliases": []string{"Johnny"}, "type": "person"}, "body": "John is a gardener."},
		map[string]any{"ref": "memory", "fields": map[string]any{"title": "Coffee with John", "relations": []any{map[string]any{"type": "with", "target": "$john"}}}, "body": "We had coffee."},
		map[string]any{"ref": "story", "kind": "stories", "fields": map[string]any{"title": "Coffee stories"}},
	}, "stories": []any{map[string]any{"story": "$story", "members": []string{"$memory"}}}}
	createdAfter := time.Now().UTC()
	saved, result := invoke(t, ctx, session, "save_memory", request)
	if result.IsError {
		t.Fatalf("capture: %+v", saved)
	}
	receipt := payload(t, saved)
	if receipt["status"] != "committed" {
		t.Fatal(receipt)
	}
	refs := receipt["refs"].(map[string]any)
	for _, text := range []string{"John", "Johnny", ""} {
		entities, result := invoke(t, ctx, session, "search_memory", map[string]any{"kind": "entities", "text": text})
		matches := payload(t, entities)["matches"].([]any)
		if result.IsError || len(matches) != 1 || matches[0].(map[string]any)["id"] != refs["john"] {
			t.Fatalf("entity lookup %q: %+v", text, entities)
		}
	}
	entity, result := invoke(t, ctx, session, "read_memory", map[string]any{"id": refs["john"], "view": "source"})
	if result.IsError {
		t.Fatal(entity)
	}
	document := songya.Parse([]byte(payload(t, entity)["text"].(string)))
	fields := map[string]any{}
	if err := document.Frontmatter.Decode(&fields); err != nil {
		t.Fatal(err)
	}
	if fields["name"] != "John" || fields["type"] != "person" || document.Body != "John is a gardener." {
		t.Fatal("entity lost its identity metadata or descriptive knowledge", entity)
	}
	id := refs["memory"].(string)
	metadata := func() songya.Metadata {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(store, "memories", id+".md"))
		if err != nil {
			t.Fatal(err)
		}
		return songya.Parse(data).Metadata()
	}
	created := metadata()
	if created.CreatedAt.Before(createdAfter) || created.CreatedAt.After(time.Now().UTC()) || !created.UpdatedAt.Equal(created.CreatedAt) {
		t.Fatalf("creation without timestamp inputs did not assign the current time: %+v", created)
	}
	read, result := invoke(t, ctx, session, "read_memory", map[string]any{"id": id})
	if result.IsError {
		t.Fatal(read)
	}
	current := payload(t, read)
	if current["text"] != "We had coffee." {
		t.Fatal(current)
	}
	revision := current["revision"].(string)
	update := map[string]any{"key": "enrich", "changes": []any{map[string]any{"id": id, "revision": revision, "append": "I remember a blue guitar."}}}
	enriched, result := invoke(t, ctx, session, "save_memory", update)
	if result.IsError {
		t.Fatal(enriched)
	}
	modified := metadata()
	if !modified.CreatedAt.Equal(created.CreatedAt) || !modified.UpdatedAt.After(created.UpdatedAt) {
		t.Fatalf("update without timestamp inputs did not preserve creation time and advance update time: %+v", modified)
	}
	stale, result := invoke(t, ctx, session, "save_memory", map[string]any{"key": "stale", "changes": []any{map[string]any{"id": id, "revision": revision, "body": "Erase earlier details"}}})
	if !result.IsError || stale["status"] != "conflict" {
		t.Fatalf("stale edit accepted: %+v", stale)
	}
	searched, result := invoke(t, ctx, session, "search_memory", map[string]any{"text": "blue guitar", "limit": "2"})
	if result.IsError || len(payload(t, searched)["matches"].([]any)) != 1 {
		t.Fatal(searched)
	}
	story, result := invoke(t, ctx, session, "compose_story", map[string]any{"id": refs["story"]})
	if result.IsError || !strings.Contains(payload(t, story)["text"].(string), "blue guitar") {
		t.Fatal(story)
	}
	resource, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: "memory://records/" + id + "/0"})
	if err != nil || len(resource.Contents) != 1 {
		t.Fatal(err)
	}
	imported, result := invoke(t, ctx, session, "import_memories", map[string]any{"action": "preview", "root": "notes"})
	if result.IsError {
		t.Fatal(imported)
	}
	file := payload(t, imported)["files"].([]any)[0].(map[string]any)
	imported, result = invoke(t, ctx, session, "import_memories", map[string]any{"action": "apply", "root": "notes", "path": file["path"], "revision": file["revision"]})
	if result.IsError || payload(t, imported)["coverage_complete"] != true {
		t.Fatal(imported)
	}
	// Discard the original receipt and restart the process. Retry still returns
	// exactly the original IDs rather than replaying creation or the old body.
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	session = connect(t, ctx, bin, store, source)
	retried, result := invoke(t, ctx, session, "save_memory", request)
	if result.IsError || payload(t, retried)["refs"].(map[string]any)["memory"] != id {
		t.Fatal("restart lost idempotency")
	}
	read, result = invoke(t, ctx, session, "read_memory", map[string]any{"id": id})
	if result.IsError || !strings.Contains(payload(t, read)["text"].(string), "blue guitar") {
		t.Fatal("retry replayed old content")
	}
	encoded, _ := json.Marshal(read)
	if len(encoded) > 32*1024 {
		t.Fatal("read exceeded the response budget")
	}
}

func TestStdioCancellationAndEOFDoNotPublishWaitingRequests(t *testing.T) {
	bin := binary(t)
	path := t.TempDir()
	store, err := songya.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	original, err := store.Create(songya.Memories, map[string]any{"user": "test"}, "Keep")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	session := connect(t, ctx, bin, path, "")
	lock := flock.New(filepath.Join(path, ".lock"))
	if err := lock.Lock(); err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	callCtx, stop := context.WithTimeout(ctx, 100*time.Millisecond)
	defer stop()
	_, err = session.CallTool(callCtx, &mcp.CallToolParams{Name: "save_memory", Arguments: map[string]any{"key": "cancelled", "changes": []any{map[string]any{"id": original.ID, "revision": original.Revision, "body": "Must not publish"}}}})
	if err == nil {
		t.Fatal("blocked request ignored cancellation")
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if err := lock.Unlock(); err != nil {
		t.Fatal(err)
	}
	reopened, err := songya.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	read, err := reopened.Read(original.ID)
	if err != nil || read.Body != "Keep" {
		t.Fatal("cancelled waiting request published")
	}
}

func TestEquivalentScalarArgumentsAndBinaryPages(t *testing.T) {
	bin := binary(t)
	path := t.TempDir()
	store, err := songya.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := store.Create(songya.Memories, map[string]any{"user": "test"}, string([]byte{255, 254, 0}))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session := connect(t, ctx, bin, path, "")
	read, result := invoke(t, ctx, session, "read_memory", map[string]any{"id": doc.ID, "offset": "0", "limit": "3"})
	if result.IsError || payload(t, read)["base64"] != "//4A" {
		t.Fatal("binary bytes were silently normalized")
	}
	data, _ := json.Marshal(read)
	if bytes.Contains(data, []byte("replacement")) {
		t.Fatal("binary source became replacement characters")
	}
	saved, result := invoke(t, ctx, session, "save_memory", map[string]any{"key": "exact-numbers", "account": 3, "changes": []any{map[string]any{"ref": "numbers", "fields": map[string]any{"title": 3, "integer": json.Number("9007199254740993"), "decimal": json.Number("1.234567890123456789")}}}})
	if result.IsError {
		t.Fatal(saved)
	}
	numberID := payload(t, saved)["refs"].(map[string]any)["numbers"]
	source, result := invoke(t, ctx, session, "read_memory", map[string]any{"id": numberID, "view": "source"})
	if result.IsError {
		t.Fatal(source)
	}
	text := payload(t, source)["text"].(string)
	if !strings.Contains(text, "9007199254740993") || !strings.Contains(text, "1.234567890123456789") {
		t.Fatalf("MCP rounded metadata numbers: %s", text)
	}
}

func TestLargeStdioReceiptsRemainBoundedAndFullyInspectable(t *testing.T) {
	bin := binary(t)
	path := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	session := connect(t, ctx, bin, path, "")
	// Oversized keys and ref labels are accepted data, not a reason to lose a
	// successful write's durable outcome or return an oversized protocol message.
	key := strings.Repeat("request-", 1000)
	label := strings.Repeat("reference-", 1000)
	request := map[string]any{"key": key, "account": "Original account", "changes": []any{map[string]any{"ref": label, "body": "A preserved narrative"}}}
	envelope, result := invoke(t, ctx, session, "save_memory", request)
	receipt := payload(t, envelope)
	if result.IsError || receipt["status"] != "committed" || receipt["key_truncated"] != true || receipt["refs_truncated"] != true {
		t.Fatal(receipt)
	}
	data, _ := json.Marshal(result)
	if len(data) > 32*1024 {
		t.Fatalf("receipt exceeded budget: %d", len(data))
	}
	id := receipt["records"].([]any)[0].(map[string]any)["id"].(string)
	var whole strings.Builder
	offset := 0
	for range 100 {
		envelope, result = invoke(t, ctx, session, "memory_maintenance", map[string]any{"action": "receipt", "view": "source", "key": key, "offset": offset, "limit": 4096})
		if result.IsError {
			t.Fatal(envelope)
		}
		page := payload(t, envelope)
		whole.WriteString(page["text"].(string))
		if page["truncated"] != true {
			break
		}
		offset = int(page["next"].(float64))
	}
	var full songya.Receipt
	if err := json.Unmarshal([]byte(whole.String()), &full); err != nil {
		t.Fatal(err)
	}
	if full.Key != key || full.Refs[label] != id {
		t.Fatal("full receipt lost original labels or identity")
	}
	envelope, result = invoke(t, ctx, session, "read_memory", map[string]any{"key": key, "id": id, "view": "proposed"})
	if result.IsError || !strings.Contains(payload(t, envelope)["text"].(string), "A preserved narrative") {
		t.Fatal(envelope)
	}
	envelope, result = invoke(t, ctx, session, "read_memory", map[string]any{"key": key, "view": "account"})
	if result.IsError || payload(t, envelope)["text"] != "Original account" {
		t.Fatal(envelope)
	}
	// A refused withdrawal still reports the committed outcome accurately.
	envelope, result = invoke(t, ctx, session, "memory_maintenance", map[string]any{"action": "abandon", "key": key})
	if !result.IsError || payload(t, envelope)["status"] != "committed" {
		t.Fatal("committed outcome hidden on withdrawal refusal")
	}
}

func TestStdioShutdownCancelsUnacceptedWaitingWrite(t *testing.T) {
	bin := binary(t)
	for _, shutdown := range []string{"signal", "eof"} {
		t.Run(shutdown, func(t *testing.T) {
			path := t.TempDir()
			store, err := songya.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			doc, err := store.Create(songya.Memories, map[string]any{"user": "test"}, "Keep")
			if err != nil {
				t.Fatal(err)
			}
			lock := flock.New(filepath.Join(path, ".lock"))
			if err := lock.Lock(); err != nil {
				t.Fatal(err)
			}
			defer lock.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, bin, "--store", path)
			stdout, err := command.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			stdin, err := command.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			command.Stderr = os.Stderr
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			defer command.Process.Kill()
			client := mcp.NewClient(&mcp.Implementation{Name: "signal-client", Version: "1"}, nil)
			session, err := client.Connect(ctx, &mcp.IOTransport{Reader: stdout, Writer: stdin}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { command.Process.Kill(); session.Close() }()
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				_, _ = session.CallTool(ctx, &mcp.CallToolParams{Name: "save_memory", Arguments: map[string]any{"key": "shutdown-waiting", "changes": []any{map[string]any{"id": doc.ID, "revision": doc.Revision, "body": "Must not publish"}}}})
			}()
			// Allow the real transport to dispatch the request. The held lock prevents any
			// acceptance; this delay does not determine the expected write outcome.
			time.Sleep(100 * time.Millisecond)
			if shutdown == "signal" {
				if err := command.Process.Signal(syscall.SIGTERM); err != nil {
					t.Fatal(err)
				}
			} else if err := stdin.Close(); err != nil {
				t.Fatal(err)
			}
			waited := make(chan error, 1)
			go func() { waited <- command.Wait() }()
			select {
			case err := <-waited:
				if err != nil {
					t.Fatalf("shutdown: %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("shutdown left the server waiting on an unaccepted write")
			}
			<-finished
			if err := lock.Unlock(); err != nil {
				t.Fatal(err)
			}
			current, err := store.Read(doc.ID)
			if err != nil || current.Body != "Keep" {
				t.Fatal("shutdown published an unaccepted request")
			}

		})
	}
}

func TestStdioGraphChronologyMergeSplitAndMaintenance(t *testing.T) {
	bin := binary(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	session := connect(t, ctx, bin, t.TempDir(), "")
	envelope, result := invoke(t, ctx, session, "save_memory", map[string]any{"key": "connected", "changes": []any{
		map[string]any{"ref": "first", "fields": map[string]any{"title": "First", "time": map[string]any{"earliest": "2000-01-01", "latest": "2000-01-01"}}, "body": "First account"},
		map[string]any{"ref": "later", "fields": map[string]any{"title": "Later", "relations": []any{map[string]any{"type": "after", "target": "$first"}}}, "body": "Later account"},
	}})
	if result.IsError {
		t.Fatal(envelope)
	}
	refs := payload(t, envelope)["refs"].(map[string]any)
	first, later := refs["first"].(string), refs["later"].(string)
	envelope, result = invoke(t, ctx, session, "explore_memory", map[string]any{"id": first, "direction": "incoming"})
	if result.IsError || len(payload(t, envelope)["edges"].([]any)) != 1 {
		t.Fatal("backlinks unavailable over stdio")
	}
	envelope, result = invoke(t, ctx, session, "memory_timeline", map[string]any{})
	if result.IsError || len(payload(t, envelope)["constraints"].([]any)) != 1 {
		t.Fatal("partial chronology unavailable over stdio")
	}
	sources := []any{}
	for _, id := range []string{first, later} {
		envelope, result = invoke(t, ctx, session, "read_memory", map[string]any{"id": id})
		if result.IsError {
			t.Fatal(envelope)
		}
		sources = append(sources, map[string]any{"id": id, "revision": payload(t, envelope)["revision"]})
	}
	merge := map[string]any{"action": "merge", "key": "merge-stdio", "sources": sources}
	envelope, result = invoke(t, ctx, session, "organize_memory", merge)
	if result.IsError {
		t.Fatal(envelope)
	}
	merged := payload(t, envelope)["refs"].(map[string]any)["merged"].(string)
	envelope, result = invoke(t, ctx, session, "organize_memory", merge)
	if result.IsError || payload(t, envelope)["refs"].(map[string]any)["merged"] != merged {
		t.Fatal("merge retry lost identity")
	}
	envelope, result = invoke(t, ctx, session, "read_memory", map[string]any{"id": merged})
	if result.IsError {
		t.Fatal(envelope)
	}
	split := map[string]any{"action": "split", "key": "split-stdio", "source": map[string]any{"id": merged, "revision": payload(t, envelope)["revision"]}, "parts": []any{map[string]any{"body": "First account"}, map[string]any{"body": "Later account"}}}
	envelope, result = invoke(t, ctx, session, "organize_memory", split)
	if result.IsError {
		t.Fatal(envelope)
	}
	envelope, result = invoke(t, ctx, session, "explore_memory", map[string]any{"id": first})
	if result.IsError {
		t.Fatal(envelope)
	}
	resolution := payload(t, envelope)["resolution"].(map[string]any)
	if resolution["ambiguous"] != true || len(resolution["targets"].([]any)) != 2 {
		t.Fatal("split ambiguity disappeared over MCP")
	}
	for _, action := range []string{"status", "recover", "rebuild"} {
		envelope, result = invoke(t, ctx, session, "memory_maintenance", map[string]any{"action": action})
		if result.IsError {
			t.Fatalf("%s: %+v", action, envelope)
		}
	}
}

func TestStdioResourcesAndErrorsStayWithinBudget(t *testing.T) {
	bin := binary(t)
	path := t.TempDir()
	if _, err := songya.Open(path); err != nil {
		t.Fatal(err)
	}
	id := "m_000000000000"
	var raw strings.Builder
	raw.WriteString("---\ntitle: Source\ncreated_at: 2000-01-01\nupdated_at: 2000-01-01\n")
	for i := 0; i < 4; i++ {
		key := strconv.Quote(strings.Repeat("\x00", 900) + strconv.Itoa(i))
		raw.WriteString(key + ": one\n" + key + ": two\n")
	}
	raw.WriteString("---\nAccount")
	if err := os.WriteFile(filepath.Join(path, "memories", id+".md"), []byte(raw.String()), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	session := connect(t, ctx, bin, path, "")
	resource, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: "memory://records/" + id + "/0"})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(resource)
	if err != nil || len(encoded) > 32*1024 {
		t.Fatalf("resource exceeded budget: %d %v", len(encoded), err)
	}
	_, err = session.ReadResource(ctx, &mcp.ReadResourceParams{URI: "memory://records/" + strings.Repeat("a", 100000) + "/0"})
	if err == nil || len(err.Error()) > 1500 {
		t.Fatalf("resource error was not bounded: %v", err)
	}
}

func TestStdioRetainedSingleCreationRemainsSearchableAndReadable(t *testing.T) {
	bin := binary(t)
	path := t.TempDir()
	if _, err := songya.Open(path); err != nil {
		t.Fatal(err)
	}
	id := "m_000000000000"
	pending := filepath.Join(path, "memories", "."+id+".md.pending")
	if err := os.Mkdir(pending, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(path, "memories", id+".md"), 0700); err != nil {
		t.Fatal(err)
	}
	raw := []byte("---\ntitle: Retained\ncreated_at: 2000-01-01\nupdated_at: 2000-01-01\n---\nRetained account")
	checksum := fmt.Sprintf("%x", sha256.Sum256(raw))
	if err := os.WriteFile(filepath.Join(pending, checksum+".tmp"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	session := connect(t, ctx, bin, path, "")
	envelope, result := invoke(t, ctx, session, "search_memory", map[string]any{"text": "Retained"})
	if result.IsError {
		t.Fatal(envelope)
	}
	matches := payload(t, envelope)["matches"].([]any)
	if len(matches) != 1 || matches[0].(map[string]any)["pending"] != true {
		t.Fatal("pending body vanished from MCP discovery")
	}
	for _, args := range []map[string]any{{"id": id}, {"source": "memories/" + id + ".md"}} {
		envelope, result = invoke(t, ctx, session, "read_memory", args)
		if result.IsError {
			t.Fatal(envelope)
		}
		page := payload(t, envelope)
		if page["pending"] != true || page["revision"] != "" || !strings.Contains(page["text"].(string), "Retained account") {
			t.Fatalf("pending content claimed a committed revision: %+v", page)
		}
	}
}

func TestStdioImportRequiresUnambiguousReviewedBounds(t *testing.T) {
	bin := binary(t)
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "reviewed.md"), []byte("abcdef"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	session := connect(t, ctx, bin, t.TempDir(), source)
	envelope, result := invoke(t, ctx, session, "import_memories", map[string]any{"action": "preview", "root": "notes"})
	if result.IsError {
		t.Fatal(envelope)
	}
	revision := payload(t, envelope)["files"].([]any)[0].(map[string]any)["revision"]
	for _, part := range []map[string]any{{"start": "not a number", "end": 3}, {"start": 0.5, "end": 3}, {"start": 0}} {
		envelope, result = invoke(t, ctx, session, "import_memories", map[string]any{"action": "apply", "root": "notes", "path": "reviewed.md", "revision": revision, "parts": []any{part}})
		if !result.IsError {
			t.Fatalf("unreviewed range was guessed: %+v", part)
		}
	}
	envelope, result = invoke(t, ctx, session, "import_memories", map[string]any{"action": "apply", "root": "notes", "path": "reviewed.md", "revision": revision, "parts": []any{map[string]any{"start": "0.0", "end": "3.0"}}})
	if result.IsError {
		t.Fatal(envelope)
	}
	id := payload(t, envelope)["parts"].([]any)[0].(map[string]any)["id"]
	envelope, result = invoke(t, ctx, session, "read_memory", map[string]any{"id": id})
	if result.IsError || payload(t, envelope)["text"] != "abc" {
		t.Fatal("equivalent integral bounds failed")
	}
	envelope, result = invoke(t, ctx, session, "read_memory", map[string]any{"id": id, "offset": strconv.Itoa(int(^uint(0) >> 1))})
	if result.IsError || payload(t, envelope)["offset"] != float64(3) || payload(t, envelope)["truncated"] != false {
		t.Fatal("large offset wrapped to the beginning")
	}
}

func TestStdioUnconfirmedSnapshotIsReadableWithoutACommittedRevision(t *testing.T) {
	bin := binary(t)
	path := t.TempDir()
	if _, err := songya.Open(path); err != nil {
		t.Fatal(err)
	}
	id := "m_000000000000"
	raw := []byte("---\ntitle: Uncertain\ncreated_at: 2000-01-01\nupdated_at: 2000-01-01\n---\nReadable uncertain account")
	if err := os.WriteFile(filepath.Join(path, "memories", id+".md"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	pending := filepath.Join(path, "memories", "."+id+".md.pending")
	if err := os.Mkdir(pending, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pending, "batch"), []byte(strings.Repeat("0", 64)), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	session := connect(t, ctx, bin, path, "")
	envelope, result := invoke(t, ctx, session, "read_memory", map[string]any{"id": id})
	if result.IsError {
		t.Fatal(envelope)
	}
	page := payload(t, envelope)
	if page["unconfirmed"] != true || page["revision"] != "" || page["text"] != "Readable uncertain account" {
		t.Fatalf("coordination damage concealed content or claimed commitment: %+v", page)
	}
}
