package web

import (
	"fmt"
	"strings"
	"testing"

	"github.com/aristath/songya"
)

func TestRecordDateRejectionKeepsTheSavedSourceAndCanBeCorrected(t *testing.T) {
	store, _ := songya.Open(t.TempDir())
	app := Handler(store)
	defer app.Close()
	source := "---\nuser: sam\ntitle: Tea preference\ntime: {date: null, earliest: '2025-07-01', latest: null}\n---\nSince July 2025, Sam prefers tea."
	code, created := call(t, app, "POST", "/api/records", map[string]any{"key": "initial", "kind": "memories", "source": source})
	if code != 200 {
		t.Fatal(code, created)
	}
	id := fmt.Sprint(created["id"])
	version := created["version"]
	_, before := call(t, app, "GET", "/api/records/"+id, nil)
	code, bad := call(t, app, "POST", "/api/records", map[string]any{"key": "invalid-end", "id": id, "version": version, "kind": "memories", "source": strings.Replace(source, "latest: null", "latest: ongoing", 1)})
	if code != 422 || bad["rejected"] != true {
		t.Fatal("invalid end was not clearly rejected", code, bad)
	}
	_, after := call(t, app, "GET", "/api/records/"+id, nil)
	if after["source"] != before["source"] || after["version"] != version {
		t.Fatal("rejected date replaced saved source", after)
	}
	code, fixed := call(t, app, "POST", "/api/records", map[string]any{"key": "corrected-end", "id": id, "version": version, "kind": "memories", "source": strings.Replace(source, "latest: null", "latest: '2026-03-12'", 1)})
	if code != 200 || !strings.Contains(fmt.Sprint(fixed["source"]), "2026-03-12") {
		t.Fatal("date could not be corrected after rejection", code, fixed)
	}
}
