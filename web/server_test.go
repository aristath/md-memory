package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aristath/songya"
)

func call(t *testing.T, h http.Handler, method, path string, input any) (int, map[string]any) {
	t.Helper()
	data, _ := json.Marshal(input)
	request := httptest.NewRequest(method, path, bytes.NewReader(data))
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	var out map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &out); err != nil {
		t.Fatal(response.Body.String(), err)
	}
	return response.Code, out
}
func TestHTTPDocumentEditingAndDeletion(t *testing.T) {
	store, _ := songya.Open(t.TempDir())
	h := Handler(store)
	for _, kind := range []string{"memories", "entities", "topics", "stories"} {
		input := map[string]any{"key": kind, "kind": kind, "source": "---\nuser: ari\ntitle: Original\ncustom: {nested: [3, false, yes]}\n---\n# Body\n"}
		code, saved := call(t, h, "POST", "/api/records", input)
		if code != 200 {
			t.Fatal(code, saved)
		}
		_, retry := call(t, h, "POST", "/api/records", input)
		if retry["id"] != saved["id"] {
			t.Fatal("duplicate creation")
		}
		code, opened := call(t, h, "GET", "/api/records/"+saved["id"].(string), nil)
		if code != 200 || opened["version"] != saved["version"] {
			t.Fatal(opened)
		}
		_, composed := call(t, h, "POST", "/api/draft", map[string]any{"source": saved["source"], "kind": kind, "fields": []Field{{Key: "user", Value: "ari"}, {Key: "custom", Value: "{nested: [8, false]}"}, {Key: "created_at", Value: "1900-01-01"}}, "body": "Edited"})
		if strings.Contains(composed["source"].(string), "1900-01-01") {
			t.Fatal("visual editor changed automatic field")
		}
		code, changed := call(t, h, "POST", "/api/records", map[string]any{"key": kind + "edit", "kind": kind, "id": saved["id"], "version": saved["version"], "source": composed["source"]})
		if code != 200 {
			t.Fatal(changed)
		}
		if !strings.Contains(changed["source"].(string), "nested") || strings.Contains(changed["source"].(string), "title:") {
			t.Fatal("full metadata replacement failed", changed)
		}
		code, _ = call(t, h, "POST", "/api/records", map[string]any{"key": kind + "stale", "id": saved["id"], "version": saved["version"], "source": "---\nuser: ari\n---\nStale"})
		if code != 409 {
			t.Fatal("stale overwrite")
		}
	}
	target, _ := store.Create(songya.Entities, map[string]any{"name": "Alex"}, "")
	incoming, _ := store.Create(songya.Stories, map[string]any{"user": "ari", "title": "A story"}, "[["+target.ID+"]]")
	request := httptest.NewRequest("DELETE", "/api/records/"+target.ID, nil)
	request.Header.Set("If-Match", target.Revision)
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != 409 || !strings.Contains(response.Body.String(), incoming.ID) || !strings.Contains(response.Body.String(), "A story") {
		t.Fatal(response.Body.String())
	}
	if _, err := store.Update(incoming.ID, incoming.Revision, nil, ""); err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
}

func TestTableProjectsMetadataAndRevisionFromTheCatalog(t *testing.T) {
	store, err := songya.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.Create(songya.Entities, map[string]any{"name": "Cedar", "description": "A tree", "type": "plant taxon", "aliases": []string{"Cedrus", "Atlas cedar"}}, "Botanical notes")
	if err != nil {
		t.Fatal(err)
	}
	for pass := 0; pass < 2; pass++ { // Fresh scan and cached projection.
		code, result := call(t, Handler(store), "GET", "/api/records", nil)
		if code != 200 {
			t.Fatal(code, result)
		}
		rows := result["records"].([]any)
		if len(rows) != 1 {
			t.Fatal(result)
		}
		row := rows[0].(map[string]any)
		if row["title"] != "Cedar" || row["description"] != "A tree" || row["type"] != "plant taxon" || row["version"] != record.Revision || len(row["aliases"].([]any)) != 2 || result["types"].(map[string]any)["plant taxon"] != true {
			t.Fatal("table lost metadata or revision", result)
		}
	}
}
func TestBundledAssetsAndCrossSiteWrites(t *testing.T) {
	store, _ := songya.Open(t.TempDir())
	h := Handler(store)
	for _, path := range []string{"/", "/assets/app.js", "/assets/app.css", "/assets/editors.css"} {
		response := httptest.NewRecorder()
		h.ServeHTTP(response, httptest.NewRequest("GET", path, nil))
		if response.Code != 200 || response.Body.Len() == 0 {
			t.Fatal(path, response.Code)
		}
	}
	request := httptest.NewRequest("POST", "/api/records", strings.NewReader(`{}`))
	request.Header.Set("Origin", "https://another-site.invalid")
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != 403 {
		t.Fatal("foreign origin wrote store")
	}
}

func TestVisualMetadataEditsPreserveAnchorsAndCustomValues(t *testing.T) {
	store, _ := songya.Open(t.TempDir())
	h := Handler(store)
	original := "---\nuser: ari\ntitle: Original\ndefaults: &defaults\n  nested: [3, false, yes]\nlinked: *defaults\n---\nBody"
	fields := describe(original).Fields
	for i := range fields {
		if fields[i].Key == "title" {
			fields[i].Value = "Changed"
		}
	}
	code, value := call(t, h, "POST", "/api/draft", map[string]any{"source": original, "fields": fields})
	if code != 200 {
		t.Fatal(value)
	}
	doc := songya.Parse([]byte(value["source"].(string)))
	var data map[string]any
	if err := doc.Frontmatter.Decode(&data); err != nil {
		t.Fatal(err)
	}
	if data["title"] != "Changed" || data["linked"] == nil || data["defaults"] == nil {
		t.Fatal("aliases or fields lost", data)
	}
}

func TestDraftConnectionsUseCurrentSourceIncludingRedirects(t *testing.T) {
	store, _ := songya.Open(t.TempDir())
	target, err := store.Create(songya.Entities, map[string]any{"name": "Target"}, "")
	if err != nil {
		t.Fatal(err)
	}
	h := Handler(store)
	input := map[string]any{"kind": "entities", "source": "---\nname: Draft\nredirects: [" + target.ID + "]\n---\n"}
	code, draft := call(t, h, "POST", "/api/draft", input)
	if code != 200 {
		t.Fatal(code, draft)
	}
	links := draft["links"].([]any)
	if len(links) != 1 || links[0].(map[string]any)["type"] != "redirects" || links[0].(map[string]any)["target_title"] != "Target" {
		t.Fatal("redirect missing from draft", draft)
	}
	input["action"], input["type"], input["target"] = "remove", "redirects", target.ID
	code, draft = call(t, h, "POST", "/api/draft", input)
	if code != 200 || len(draft["links"].([]any)) != 0 {
		t.Fatal("removed redirect still in draft", draft)
	}
}

func TestInvalidUTF8CannotBeSilentlyRewrittenThroughTheEditor(t *testing.T) {
	path := t.TempDir()
	store, _ := songya.Open(path)
	id := "m_000000000097"
	original := []byte("---\nuser: ari\n---\nBad byte: ")
	original = append(original, 0xff)
	if err := os.WriteFile(filepath.Join(path, "memories", id+".md"), original, 0600); err != nil {
		t.Fatal(err)
	}
	h := Handler(store)
	code, _ := call(t, h, "GET", "/api/records/"+id, nil)
	if code != 422 {
		t.Fatal("lossy editor opened damaged encoding")
	}
	after, err := os.ReadFile(filepath.Join(path, "memories", id+".md"))
	if err != nil || !bytes.Equal(after, original) {
		t.Fatal("original bytes changed")
	}
	healthy, err := store.Create(songya.Entities, map[string]any{"name": "Healthy"}, "Readable")
	if err != nil {
		t.Fatal(err)
	}
	if code, _ := call(t, h, "GET", "/api/records/"+healthy.ID, nil); code != 200 {
		t.Fatal("one corrupt record disabled healthy reads")
	}
}
