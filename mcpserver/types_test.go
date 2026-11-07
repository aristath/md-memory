package mcpserver_test

import (
	"context"
	"encoding/json"
	"github.com/aristath/songya"
	"github.com/aristath/songya/mcpserver"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"strings"
	"testing"
	"time"
)

func TestUserTypeChoicesReachSchemasAndActualMCPWrites(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	root := t.TempDir()
	store, _ := songya.Open(root)
	cfg, _ := store.TypeConfiguration(ctx, "cook")
	cfg.Kinds[songya.Entities] = append(cfg.Kinds[songya.Entities], songya.TypeDefinition{ID: "ingredient", Name: "Ingredient", Description: "An ingredient used in a recipe."})
	if _, err := store.SaveTypeConfiguration(ctx, cfg, cfg.Version, nil, "custom"); err != nil {
		t.Fatal(err)
	}
	session := connect(t, ctx, binary(t), root, "")
	configured := keyed(t, ctx, session, "type_configuration", "read", map[string]any{"user": "cook", "schemas": true})
	raw, _ := json.Marshal(configured["write_schemas"])
	if !strings.Contains(string(raw), "ingredient") {
		t.Fatal(configured)
	}
	saved := keyed(t, ctx, session, "save_entity", "new-ingredient", map[string]any{"create": true, "name": "Bulgur", "type_user": "cook", "type": "ingredient", "description": "Cracked wheat."})
	if saved["type"] != "ingredient" || saved["type_user"] != "cook" {
		t.Fatal("save feedback lost the selected user's classification", saved)
	}
	id := saved["id"].(string)
	_, bad := invoke(t, ctx, session, "save_entity", map[string]any{"id": id, "type_user": "other", "type": "ingredient"})
	if !bad.IsError {
		t.Fatal("other user's list was accepted")
	}
	other := keyed(t, ctx, session, "save_entity", "other-food", map[string]any{"id": id, "type_user": "other", "type": "food"})
	if other["type"] != "food" {
		t.Fatal(other)
	}
	doc, _ := store.Read(id)
	if doc.TypeFor("cook") != "ingredient" || doc.TypeFor("other") != "food" || doc.Body != "Cracked wheat." {
		t.Fatal(doc)
	}
	_, bad = invoke(t, ctx, session, "save_entity", map[string]any{"id": id, "type_user": "cook", "metadata": map[string]any{"types": map[string]string{"cook": "ingredient", "other": "object"}}})
	if !bad.IsError {
		t.Fatal("cross-user metadata bypass accepted")
	}
	// A user-bound MCP endpoint publishes the exact enum in tools/list as well.
	ct, st := mcp.NewInMemoryTransports()
	server, err := mcpserver.NewForUser(ctx, store, nil, "cook").Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	client, err := mcp.NewClient(&mcp.Implementation{Name: "type-test", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	tools, err := client.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tool := range tools.Tools {
		if tool.Name == "save_entity" {
			data, _ := json.Marshal(tool.InputSchema)
			found = strings.Contains(string(data), "ingredient") && strings.Contains(string(data), `"enum"`)
		}
	}
	if !found {
		t.Fatal("tools/list did not expose the user's configured enum")
	}
}
