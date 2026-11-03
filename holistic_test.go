package memory

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestHealthyMetadataSurvivesDamagedLeavesAndReread(t *testing.T) {
	path := t.TempDir()
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	cycle := map[string]any{"fact": "Keep cyclic neighbour"}
	cycle["self"] = cycle
	doc, err := store.Create(Memories, map[string]any{
		"title":       3,
		"details":     map[string]any{"fact": "Keep fact", "bad": func() {}, "list": []any{"Keep item", make(chan int)}},
		"cycle":       cycle,
		"broken_node": &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{{Kind: yaml.ScalarNode, Value: "missing value"}}},
	}, "Original body")
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Diagnostics()) != 4 {
		t.Fatalf("creation issues: %v", doc.Diagnostics())
	}
	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	read, err := store.Read(doc.ID)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := read.Frontmatter.Decode(&fields); err != nil {
		t.Fatal(err)
	}
	details := fields["details"].(map[string]any)
	if details["fact"] != "Keep fact" || details["list"].([]any)[0] != "Keep item" || fields["cycle"].(map[string]any)["fact"] != "Keep cyclic neighbour" {
		t.Fatalf("healthy neighbours were discarded: %v", fields)
	}
	if read.Body != "Original body" || read.Metadata().Title != "3" || len(read.Diagnostics()) != 4 {
		t.Fatalf("content or persistent issues changed: %v", read.Diagnostics())
	}
	for _, location := range []string{"metadata.details.bad", "metadata.details.list[1]", "metadata.cycle.self", "metadata.broken_node"} {
		if !strings.Contains(strings.Join(read.Diagnostics(), "\n"), location) {
			t.Fatalf("issue lacks location %s: %v", location, read.Diagnostics())
		}
	}
}

func TestCreateRetriesItsOwnUnfinishedInitialization(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	parent := filepath.Join(t.TempDir(), "parent")
	if err := os.Mkdir(parent, 0300); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(parent, 0700)
	store, err := Open(filepath.Join(parent, "store"))
	if err != nil || len(store.Diagnostics()) == 0 {
		t.Fatalf("initialization was not deferred: %v", err)
	}
	if err := os.Chmod(parent, 0700); err != nil {
		t.Fatal(err)
	}
	doc, err := store.Create(Memories, nil, "Recovered without reopening")
	if err != nil || doc.Pending || len(store.Diagnostics()) != 0 {
		t.Fatalf("same-handle retry failed: %v, %v", err, store.Diagnostics())
	}
	if read, err := store.Read(doc.ID); err != nil || read.Body != doc.Body {
		t.Fatalf("recovered write unavailable: %v", err)
	}
}

func TestStagingReservationAndAmbiguityStayIsolated(t *testing.T) {
	path := t.TempDir()
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	name := "memories/m_000000000000.md"
	if err := root.Mkdir(pendingName(name), 0700); err != nil {
		t.Fatal(err)
	}
	if retained, err := stage(root, name, []byte(sample)); retained || !errors.Is(err, fs.ErrExist) {
		t.Fatalf("reused claimed ID: %v", err)
	}
	if err := store.Recover(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read("m_000000000000"); err == nil || errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("empty reservation mistaken for an absent record: %v", err)
	}
	for _, data := range [][]byte{[]byte(sample), []byte("Different retained body")} {
		if err := root.WriteFile(temporaryName(name, data), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Recover(); err != nil {
		t.Fatal(err)
	}
	if _, err := root.Lstat(name); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("arbitrarily selected one of two staged records")
	}
	for _, data := range [][]byte{[]byte(sample), []byte("Different retained body")} {
		if got, err := root.ReadFile(temporaryName(name, data)); err != nil || !bytes.Equal(got, data) {
			t.Fatal("ambiguous data was discarded")
		}
	}
	if _, err := store.Create(Stories, nil, "Unrelated work continues"); err != nil {
		t.Fatal(err)
	}
}

func TestRecoveryRetainsForeignPendingData(t *testing.T) {
	path := t.TempDir()
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := store.Create(Memories, nil, "Committed")
	if err != nil {
		t.Fatal(err)
	}
	name := filepath.Join("memories", doc.ID+".md")
	foreign := filepath.Join(path, pendingName(name))
	if err := os.WriteFile(foreign, []byte("Unrecognized data"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.Recover(); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(foreign); err != nil || string(data) != "Unrecognized data" {
		t.Fatal("deleted unknown pending data")
	}
	if read, err := store.Read(doc.ID); err != nil || read.Body != "Committed" || read.Pending {
		t.Fatalf("foreign data disabled a committed record: %v", err)
	}
}

func TestRecoveryFinishesInterruptedStagingCleanup(t *testing.T) {
	path := t.TempDir()
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	name := "memories/m_000000000000.md"
	data := []byte(sample)
	if _, err := stage(root, name, data); err != nil {
		t.Fatal(err)
	}
	if err := root.Link(temporaryName(name, data), name); err != nil {
		t.Fatal(err)
	}
	if err := syncPath(root, "memories"); err != nil {
		t.Fatal(err)
	}
	// Publication finished; cleanup removed the payload but not its directory.
	if err := root.Remove(temporaryName(name, data)); err != nil {
		t.Fatal(err)
	}
	if err := store.Recover(); err != nil || len(store.Diagnostics()) != 0 {
		t.Fatalf("interrupted cleanup remained blocked: %v, %v", err, store.Diagnostics())
	}
	if _, err := root.Lstat(pendingName(name)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("empty reservation was not cleaned up")
	}
	if read, err := store.Read("m_000000000000"); err != nil || read.Body != Parse(data).Body || read.Pending {
		t.Fatalf("cleanup harmed the committed record: %v", err)
	}
}
