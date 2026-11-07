package web

import (
	"context"
	"github.com/aristath/songya"
	"reflect"
	"testing"
)

func TestTypeSettingsAndEditorShareUserConfiguration(t *testing.T) {
	store, _ := songya.Open(t.TempDir())
	app := Handler(store)
	defer app.Close()
	code, initial := call(t, app, "GET", "/api/types/cook", nil)
	if code != 200 {
		t.Fatal(code, initial)
	}
	kinds := initial["kinds"].(map[string]any)
	entities := kinds["entities"].([]any)
	kinds["entities"] = append(entities, map[string]any{"id": "ingredient", "name": "Ingredient", "description": "An ingredient used for cooking."})
	payload := map[string]any{"key": "type-http-save", "version": initial["version"], "kinds": kinds}
	code, saved := call(t, app, "PUT", "/api/types/cook", payload)
	if code != 200 {
		t.Fatal(code, saved)
	}
	code, retry := call(t, app, "PUT", "/api/types/cook", payload)
	if code != 200 || retry["version"] != saved["version"] {
		t.Fatal("lost acknowledgement did not recover", code, retry)
	}
	code, other := call(t, app, "GET", "/api/types/life", nil)
	if code != 200 || len(other["kinds"].(map[string]any)["entities"].([]any)) != 12 {
		t.Fatal(other)
	}
	doc, err := store.Create(songya.Entities, map[string]any{"name": "Bulgur", "types": map[string]string{"cook": "ingredient", "life": "food"}}, "A food ingredient.")
	if err != nil {
		t.Fatal(err)
	}
	source := string(doc.OriginalSource())
	if source == "" {
		raw, _ := doc.Marshal()
		source = string(raw)
	}
	code, draft := call(t, app, "POST", "/api/draft", map[string]any{"source": source, "id": doc.ID, "kind": "entities", "classification_user": "cook", "classification_type": "object"})
	if code != 200 {
		t.Fatal(draft)
	}
	code, written := call(t, app, "POST", "/api/records", map[string]any{"key": "editor-classify", "id": doc.ID, "kind": "entities", "version": doc.Revision, "source": draft["source"], "classification_user": "cook"})
	if code != 200 {
		t.Fatal(written)
	}
	after, _ := store.Read(doc.ID)
	if after.TypeFor("cook") != "object" || after.TypeFor("life") != "food" || after.Body != doc.Body {
		t.Fatal(after)
	}
	code, listing := call(t, app, "GET", "/api/records?classification_user=life", nil)
	if code != 200 || listing["records"].([]any)[0].(map[string]any)["type"] != "food" {
		t.Fatal(listing)
	}
	before := after.Revision
	code, bad := call(t, app, "POST", "/api/records", map[string]any{"key": "cross-user", "id": doc.ID, "kind": "entities", "version": before, "source": "---\nname: Bulgur\ntypes: {cook: object, life: plant}\n---\nA food ingredient.", "classification_user": "cook"})
	if code != 422 || bad["rejected"] != true {
		t.Fatal(code, bad)
	}
	unchanged, _ := store.Read(doc.ID)
	if unchanged.Revision != before {
		t.Fatal("rejected write changed record")
	}
	cfg, _ := store.TypeConfiguration(context.Background(), "cook")
	if !reflect.DeepEqual(cfg.Kinds[songya.Memories], []songya.TypeDefinition{}) {
		t.Fatal(cfg)
	}
}
