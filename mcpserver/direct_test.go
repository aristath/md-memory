package mcpserver_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aristath/songya"
	"github.com/aristath/songya/mcpserver"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func keyed(t *testing.T, ctx context.Context, s *mcp.ClientSession, name, key string, args any) map[string]any {
	t.Helper()
	r, err := s.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args, Meta: mcp.Meta{"songya/request-key": key}})
	if err != nil {
		t.Fatal(err)
	}
	if r.IsError {
		t.Fatalf("%s: %+v", name, r)
	}
	data, _ := json.Marshal(r.StructuredContent)
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return payload(t, result)
}
func TestDirectGuideCapture(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	path := t.TempDir()
	session := connect(t, ctx, binary(t), path, "")
	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Tools) != 13 {
		t.Fatal("missing dedicated tools", len(listed.Tools))
	}
	for _, tool := range listed.Tools {
		if !strings.HasPrefix(tool.Name, "save_") && tool.Name != "organize_memory" && tool.Name != "import_memories" {
			continue
		}
		schema, _ := json.Marshal(tool.InputSchema)
		for _, technical := range []string{`"changes"`, `"ref"`, `"revision"`, `"fields"`, `"key"`} {
			if strings.Contains(string(schema), technical) {
				t.Fatalf("%s still exposes %s", tool.Name, technical)
			}
		}
	}
	_, example, _ := strings.Cut(mcpserver.Guide, "save_memory(")
	example, _, _ = strings.Cut(example, ")\n")
	args := map[string]any{}
	if err := json.Unmarshal([]byte(example), &args); err != nil {
		t.Fatal(err)
	}
	args["user"] = "ari"
	saved := keyed(t, ctx, session, "save_memory", "guide", args)
	if saved["status"] != "committed" {
		t.Fatal(saved)
	}
	entities := saved["entities"].([]any)
	if len(entities) != 3 {
		t.Fatal("capture lost entities", saved)
	}
	id := saved["id"].(string)
	data, err := os.ReadFile(filepath.Join(path, "memories", id+".md"))
	if err != nil {
		t.Fatal(err)
	}
	doc := songya.Parse(data)
	if doc.Body != args["text"] || doc.Metadata().User != "ari" || len(doc.Metadata().Relations) != 3 {
		t.Fatal("capture lost content", string(data))
	}
	for _, v := range entities {
		entity := v.(map[string]any)
		if !strings.Contains(string(data), entity["id"].(string)) {
			t.Fatal("entity is not connected", entity)
		}
	}
	for _, text := range []string{"Alex Morgan", "Sam Lee", "Cedar Café"} {
		found, r := invoke(t, ctx, session, "search_memory", map[string]any{"text": text, "user": "ari"})
		if r.IsError || len(payload(t, found)["matches"].([]any)) != 2 {
			t.Fatal("saved knowledge cannot be found", found)
		}
	}
	prepared, r := invoke(t, ctx, session, "prepare_memory", map[string]any{"text": "Alex Morgan", "user": "ari"})
	if r.IsError || payload(t, prepared)["query"] != "Alex Morgan" || payload(t, prepared)["key"] != nil {
		t.Fatal(prepared)
	}
}
func TestDedicatedWritesOwnershipAndOrganization(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	session := connect(t, ctx, binary(t), t.TempDir(), "")
	john := keyed(t, ctx, session, "save_entity", "john", map[string]any{"name": "John", "type": "person", "aliases": []string{"Johnny"}, "description": "An old friend."})
	topic := keyed(t, ctx, session, "save_topic", "topic", map[string]any{"name": "Gardening", "description": "Plants and gardens."})
	a := keyed(t, ctx, session, "save_memory", "ari", map[string]any{"text": "Coffee with John.", "user": "ari", "entities": []any{map[string]any{"name": "Johnny", "type": "person", "description": "John grows roses.", "relation": "with"}}, "topics": []string{"Gardening"}})
	b := keyed(t, ctx, session, "save_memory", "bob", map[string]any{"text": "Bob met John.", "user": "bob", "entities": []string{"John"}})
	if a["entities"].([]any)[0].(map[string]any)["id"] != john["id"] || a["topics"].([]any)[0].(map[string]any)["id"] != topic["id"] {
		t.Fatal("exact names/aliases not reused", a)
	}
	for user, count := range map[string]int{"ari": 3, "bob": 3, "all": 4} {
		found, r := invoke(t, ctx, session, "search_memory", map[string]any{"user": user})
		if r.IsError || len(payload(t, found)["matches"].([]any)) != count {
			t.Fatal(user, found)
		}
	}
	story := keyed(t, ctx, session, "save_story", "story", map[string]any{"title": "Meetings", "user": "ari", "members": []any{a["id"], b["id"]}})
	composed, r := invoke(t, ctx, session, "compose_story", map[string]any{"id": story["id"]})
	if r.IsError || !strings.Contains(payload(t, composed)["text"].(string), "Bob met John") {
		t.Fatal(composed)
	}
	keyed(t, ctx, session, "save_entity", "enrich", map[string]any{"id": john["id"], "append": "A helpful neighbour."})
	keyed(t, ctx, session, "save_memory", "update", map[string]any{"id": a["id"], "append": "We discussed roses."})
	read, r := invoke(t, ctx, session, "read_memory", map[string]any{"id": john["id"]})
	if r.IsError || !strings.Contains(payload(t, read)["text"].(string), "John grows roses.") || !strings.Contains(payload(t, read)["text"].(string), "An old friend.") {
		t.Fatal(read)
	}
	merged := keyed(t, ctx, session, "organize_memory", "merge", map[string]any{"action": "merge", "sources": []any{a["id"], b["id"]}, "metadata": map[string]any{"user": "ari"}})
	split := keyed(t, ctx, session, "organize_memory", "split", map[string]any{"action": "split", "source": merged["id"], "parts": []any{map[string]any{"text": "First meeting"}, map[string]any{"text": "Second meeting"}}})
	if len(split["ids"].([]any)) != 2 {
		t.Fatal(split)
	}
}
func TestRetryMetadataSurvivesRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	bin, path := binary(t), t.TempDir()
	session := connect(t, ctx, bin, path, "")
	args := map[string]any{"user": "ari", "text": "Coffee with John", "entities": []string{"John"}}
	first := keyed(t, ctx, session, "save_memory", "stable-operation", args)
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	session = connect(t, ctx, bin, path, "")
	retried := keyed(t, ctx, session, "save_memory", "stable-operation", args)
	if first["id"] != retried["id"] || first["entities"].([]any)[0].(map[string]any)["id"] != retried["entities"].([]any)[0].(map[string]any)["id"] {
		t.Fatal("retry created new records", retried)
	}
	found, r := invoke(t, ctx, session, "search_memory", map[string]any{"user": "all"})
	if r.IsError || len(payload(t, found)["matches"].([]any)) != 2 {
		t.Fatal(found)
	}
	// The same narrative in a distinct operation is a legitimate new memory.
	second := keyed(t, ctx, session, "save_memory", "another-operation", args)
	if first["id"] == second["id"] {
		t.Fatal("a fresh capture was incorrectly deduplicated")
	}
	changed := map[string]any{"user": "ari", "text": "Different intent"}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "save_memory", Arguments: changed, Meta: mcp.Meta{"songya/request-key": "stable-operation"}})
	if err != nil || !result.IsError {
		t.Fatal("accepted operation was overwritten", result, err)
	}
}
func TestDirectArgumentsStayFlexible(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	path := t.TempDir()
	session := connect(t, ctx, binary(t), path, "")
	saved := keyed(t, ctx, session, "save_memory", "numbers", map[string]any{"text": 3, "user": 3, "time": "last summer", "entities": []any{map[string]any{"name": 3, "description": 3}}, "metadata": map[string]any{"number": json.Number("9007199254740993")}})
	data, err := os.ReadFile(filepath.Join(path, "memories", saved["id"].(string)+".md"))
	if err != nil {
		t.Fatal(err)
	}
	if songya.Parse(data).Body != "3" || !strings.Contains(string(data), "9007199254740993") || !strings.Contains(string(data), "last summer") {
		t.Fatal(string(data))
	}
}
func TestIncompleteIdentityDoesNotDiscardCapture(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	path := t.TempDir()
	store, err := songya.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := store.Create(songya.Entities, map[string]any{"name": "John"}, "Different John"); err != nil {
			t.Fatal(err)
		}
	}
	session := connect(t, ctx, binary(t), path, "")
	saved := keyed(t, ctx, session, "save_memory", "ambiguous", map[string]any{"text": "Dinner with John at Pizza AMI", "user": "ari", "entities": []any{map[string]any{"name": "John", "relation": "with"}, map[string]any{"name": "Pizza AMI", "type": "place", "relation": "at"}}})
	if len(saved["entities"].([]any)) != 1 || !strings.Contains(fmt.Sprint(saved["diagnostics"]), "identity resolution") {
		t.Fatal(saved)
	}
	data, _ := os.ReadFile(filepath.Join(path, "memories", saved["id"].(string)+".md"))
	if !strings.Contains(string(data), "unresolved_connections") || !strings.Contains(string(data), "John") || songya.Parse(data).Body != "Dinner with John at Pizza AMI" {
		t.Fatal(string(data))
	}
}

func TestMCPRelationsCanBeCorrectedByName(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	session := connect(t, ctx, binary(t), t.TempDir(), "")
	first := keyed(t, ctx, session, "save_entity", "venue", map[string]any{"name": "Cedar Café", "relations": []any{map[string]any{"type": "liked_by", "target": "e_sam"}}})
	corrected := keyed(t, ctx, session, "save_entity", "repair", map[string]any{"id": first["id"], "relations": []any{map[string]any{"type": "liked_by", "target": "Sam Lee"}}})
	relations := corrected["relations"].([]any)
	if len(relations) != 1 || !songya.ValidID(relations[0].(map[string]any)["target"].(string)) || len(corrected["diagnostics"].([]any)) != 0 {
		t.Fatal(corrected)
	}
	preserved := keyed(t, ctx, session, "save_entity", "append", map[string]any{"id": first["id"], "append": "A café."})
	if len(preserved["relations"].([]any)) != 1 {
		t.Fatal(preserved)
	}
	cleared := keyed(t, ctx, session, "save_entity", "clear", map[string]any{"id": first["id"], "relations": []any{}})
	if v, ok := cleared["relations"].([]any); ok && len(v) != 0 {
		t.Fatal(cleared)
	}
	people, r := invoke(t, ctx, session, "search_memory", map[string]any{"kind": "entities", "text": "Sam Lee"})
	if r.IsError || len(payload(t, people)["matches"].([]any)) != 1 {
		t.Fatal(people)
	}
}

func TestLongSharedMemoryIsEnrichedWithoutGrowingItsEntities(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	path := t.TempDir()
	session := connect(t, ctx, binary(t), path, "")
	text := "Ari and Daphne enjoy Pizza AMI's soup.\n" + strings.Repeat("Their shared history is recorded here.\n", 2000)
	saved := keyed(t, ctx, session, "save_memory", "history", map[string]any{"user": "ari", "text": text, "entities": []any{map[string]any{"name": "Ari", "type": "person", "relation": "about"}, map[string]any{"name": "Daphne", "type": "person", "relation": "about"}, map[string]any{"name": "Pizza AMI", "type": "place", "relation": "at"}}})
	if saved["text_truncated"] != true || len(saved["entities"].([]any)) != 3 {
		t.Fatal(saved)
	}
	updated := keyed(t, ctx, session, "save_memory", "enrich-history", map[string]any{"id": saved["id"], "append": "They also enjoy the salad."})
	if updated["id"] != saved["id"] || len(updated["entities"].([]any)) != 3 {
		t.Fatal(updated)
	}
	data, err := os.ReadFile(filepath.Join(path, "memories", saved["id"].(string)+".md"))
	if err != nil || songya.Parse(data).Body != text+"\n\nThey also enjoy the salad." {
		t.Fatal("long memory lost prose", err)
	}
	for _, item := range saved["entities"].([]any) {
		entity := item.(map[string]any)
		id := entity["id"].(string)
		data, err := os.ReadFile(filepath.Join(path, "entities", id+".md"))
		if err != nil || songya.Parse(data).Body != "" {
			t.Fatal("history leaked into identity", id, err)
		}
		graph, r := invoke(t, ctx, session, "explore_memory", map[string]any{"id": id, "direction": "incoming"})
		if r.IsError || len(payload(t, graph)["edges"].([]any)) != 1 {
			t.Fatal("memory missing from participant", graph)
		}
		found, r := invoke(t, ctx, session, "search_memory", map[string]any{"kind": "memories", "related_to": id, "user": "ari"})
		if r.IsError || len(payload(t, found)["matches"].([]any)) != 1 || payload(t, found)["matches"].([]any)[0].(map[string]any)["id"] != saved["id"] {
			t.Fatal("history cannot be queried through its entity", found)
		}
	}
}
