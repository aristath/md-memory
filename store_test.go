package memory

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/flock"
	"go.yaml.in/yaml/v3"
)

func TestStoreInitializesAndPreservesExistingFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store")
	if err := os.MkdirAll(filepath.Join(path, "memories"), 0700); err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(path, "memories", "m_7k3m9x2q6v4c.md")
	if err := os.WriteFile(name, []byte(sample), 0600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		store, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.Read("m_7k3m9x2q6v4c"); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(name)
	if err != nil || string(data) != sample {
		t.Fatalf("existing file was changed: %v", err)
	}
	for _, directory := range []string{"memories", "entities", "topics", "stories", ".index"} {
		info, err := os.Stat(filepath.Join(path, directory))
		if err != nil || !info.IsDir() {
			t.Fatalf("missing directory %s: %v", directory, err)
		}
	}
}

func TestStoreCreatesAndReadsAllDestinations(t *testing.T) {
	path := t.TempDir()
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	before := time.Now()
	fields := map[string]any{
		"id":         "caller-owned-id",
		"title":      "α Title",
		"created_at": "invalid caller timestamp",
		"updated_at": 0,
		"relations":  []Relation{{Type: "a_custom_relation", Target: "e_7k3m9x2q6v4c"}},
		"custom":     map[string]any{"enabled": false, "number": 0, "values": []string{"α", "yes", "0001"}},
	}
	body := "\r\n# Original body\r\n\r\nLiteral --- text."
	for _, destination := range []Destination{Memories, Entities, Topics, Stories} {
		doc, err := store.Create(destination, fields, body)
		if err != nil {
			t.Fatal(err)
		}
		metadata, err := doc.Metadata()
		if err != nil {
			t.Fatal(err)
		}
		prefix, _ := destination.prefix()
		if !strings.HasPrefix(metadata.ID, prefix) || !ValidID(metadata.ID) {
			t.Fatalf("wrong ID for %s: %q", destination, metadata.ID)
		}
		if metadata.CreatedAt.Before(before) || metadata.CreatedAt.After(time.Now()) || !metadata.CreatedAt.Equal(metadata.UpdatedAt) || metadata.CreatedAt.Location() != time.UTC {
			t.Fatalf("timestamps were not engine-assigned in UTC: %+v", metadata)
		}
		stored, err := os.ReadFile(filepath.Join(path, string(destination), metadata.ID+".md"))
		if err != nil {
			t.Fatal(err)
		}
		read, err := store.Read(metadata.ID)
		if err != nil || read.Body != body {
			t.Fatalf("read failed or body changed: %v", err)
		}
		readBytes, err := read.Marshal()
		if err != nil || !bytes.Equal(readBytes, stored) {
			t.Fatalf("metadata changed after reading: %v", err)
		}
		var actual, expected yaml.Node
		if err := expected.Encode(fields["custom"]); err != nil {
			t.Fatal(err)
		}
		var values map[string]yaml.Node
		if err := read.Frontmatter.Decode(&values); err != nil {
			t.Fatal(err)
		}
		actual = values["custom"]
		got, _ := yaml.Marshal(&actual)
		want, _ := yaml.Marshal(&expected)
		if !bytes.Equal(got, want) {
			t.Fatalf("custom metadata changed: %s", got)
		}
	}
	if fields["id"] != "caller-owned-id" || fields["created_at"] != "invalid caller timestamp" {
		t.Fatal("Create mutated the caller's input")
	}
}

func TestStoreReadsSameSuffixInDifferentDestinations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "store")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, destination := range []Destination{Memories, Entities, Topics, Stories} {
		prefix, _ := destination.prefix()
		id := prefix + "7k3m9x2q6v4c"
		data := strings.Replace(sample, "id: m_7k3m9x2q6v4c", "id: "+id, 1)
		if err := os.WriteFile(filepath.Join(path, string(destination), id+".md"), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		read, err := store.Read(id)
		if err != nil {
			t.Fatal(err)
		}
		metadata, _ := read.Metadata()
		if metadata.ID != id {
			t.Fatalf("wrong record for %s: %s", destination, metadata.ID)
		}
	}
}

func TestStoreWorksWithoutDerivedIndex(t *testing.T) {
	path := t.TempDir()
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	lockBefore, err := os.Stat(filepath.Join(path, ".lock"))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := store.Create(Memories, nil, "Body")
	if err != nil {
		t.Fatal(err)
	}
	metadata, _ := doc.Metadata()
	if err := os.RemoveAll(filepath.Join(path, ".index")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read(metadata.ID); err != nil {
		t.Fatalf("read depended on index: %v", err)
	}
	if _, err := store.Create(Entities, nil, "Entity"); err != nil {
		t.Fatalf("creation depended on index: %v", err)
	}
	if _, err := Open(path); err != nil {
		t.Fatal(err)
	}
	lockAfter, err := os.Stat(filepath.Join(path, ".lock"))
	if err != nil || !os.SameFile(lockBefore, lockAfter) {
		t.Fatalf("initialization replaced the coordination lock: %v", err)
	}
}

func TestStoreRejectsInvalidInputAndIsolatesDamagedRecords(t *testing.T) {
	path := t.TempDir()
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, destination := range []Destination{"", "../outside", "unknown"} {
		if _, err := store.Create(destination, nil, "Body"); err == nil {
			t.Fatalf("accepted destination %q", destination)
		}
	}
	if _, err := store.Create(Memories, map[string]any{"relations": []Relation{{Type: "after", Target: "bad-id"}}}, "Body"); err == nil {
		t.Fatal("accepted invalid relation target")
	}
	entries, err := os.ReadDir(filepath.Join(path, "memories"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("invalid creation left files behind: %v", err)
	}
	for _, id := range []string{"../outside", "7k3m9x2q6v4c", "x_7k3m9x2q6v4c"} {
		if _, err := store.Read(id); err == nil {
			t.Fatalf("accepted invalid ID %q", id)
		}
	}
	if _, err := store.Read("m_000000000000"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing record did not retain ErrNotExist: %v", err)
	}
	for _, content := range []string{"---\nid: [broken\n---\nOriginal text", strings.Replace(sample, "id: m_7k3m9x2q6v4c", "id: m_b8r5n2w9d4hx", 1)} {
		name := filepath.Join(path, "memories", "m_7k3m9x2q6v4c.md")
		if err := os.WriteFile(name, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Read("m_7k3m9x2q6v4c"); err == nil || !strings.Contains(err.Error(), "m_7k3m9x2q6v4c.md") {
			t.Fatalf("damaged record was not identified: %v", err)
		}
		data, _ := os.ReadFile(name)
		if string(data) != content {
			t.Fatal("damaged record was rewritten")
		}
		healthy, err := store.Create(Memories, nil, "Healthy record")
		if err != nil {
			t.Fatalf("damaged record blocked healthy creation: %v", err)
		}
		metadata, _ := healthy.Metadata()
		if _, err := store.Read(metadata.ID); err != nil {
			t.Fatalf("damaged record blocked healthy reading: %v", err)
		}
	}
}

func TestPublicationNeverOverwritesExistingFiles(t *testing.T) {
	path := t.TempDir()
	if _, err := Open(path); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	name := filepath.Join("memories", "m_7k3m9x2q6v4c.md")
	if err := root.WriteFile(name, []byte("Existing contents"), 0600); err != nil {
		t.Fatal(err)
	}
	if published, err := publish(root, name, []byte(sample)); published || !errors.Is(err, fs.ErrExist) {
		t.Fatalf("publication overwrote an existing name: %v, %v", published, err)
	}
	data, _ := root.ReadFile(name)
	if string(data) != "Existing contents" {
		t.Fatal("existing contents were changed")
	}
	entries, _ := fs.ReadDir(root.FS(), "memories")
	if len(entries) != 1 {
		t.Fatalf("failed publication left staging files: %v", entries)
	}
}

func TestStoreRejectsSymlinksOutsideRoot(t *testing.T) {
	path, outside := t.TempDir(), t.TempDir()
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(outside, "record.md")
	if err := os.WriteFile(external, []byte(sample), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(path, "memories", "m_7k3m9x2q6v4c.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read("m_7k3m9x2q6v4c"); err == nil {
		t.Fatal("read followed an external record symlink")
	}
	if err := os.Symlink(outside, filepath.Join(path, "linked")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create("linked", nil, "Body"); err == nil {
		t.Fatal("accepted a non-record directory")
	}
	entries, _ := os.ReadDir(outside)
	if len(entries) != 1 {
		t.Fatal("store wrote outside its root")
	}
}

func TestStoreInitializationRejectsNonDirectoryAndExternalLinks(t *testing.T) {
	for _, name := range []string{"memories", ".index", ".lock"} {
		t.Run(name, func(t *testing.T) {
			path, outside := t.TempDir(), t.TempDir()
			if err := os.Symlink(outside, filepath.Join(path, name)); err != nil {
				t.Fatal(err)
			}
			if _, err := Open(path); err == nil {
				t.Fatal("opened a store with an external link")
			}
			entries, _ := os.ReadDir(outside)
			if len(entries) != 0 {
				t.Fatal("initialization wrote outside the store")
			}
		})
	}
	path := t.TempDir()
	name := filepath.Join(path, "memories")
	if err := os.WriteFile(name, []byte("Existing file"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil {
		t.Fatal("opened a store with a file in place of a directory")
	}
	data, _ := os.ReadFile(name)
	if string(data) != "Existing file" {
		t.Fatal("initialization replaced an existing file")
	}
}

func helperProcess(t *testing.T, role, path string) *exec.Cmd {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestStoreProcess$")
	command.Env = append(os.Environ(), "MD_MEMORY_TEST_ROLE="+role, "MD_MEMORY_TEST_PATH="+path, "GORACE=atexit_sleep_ms=0")
	return command
}

func TestStoreConcurrentProcessesPublishCompleteRecords(t *testing.T) {
	path := t.TempDir()
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	const workers = 4
	results := make(chan error, workers)
	for range workers {
		command := helperProcess(t, "writer", path)
		go func() {
			data, err := command.CombinedOutput()
			if err != nil {
				err = fmt.Errorf("writer: %w: %s", err, data)
			}
			results <- err
		}()
	}
	// Inspect published names directly, without the engine's read lock, while
	// separate writer processes are running. Staging files are not record names.
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	finished := 0
	for finished < workers {
		select {
		case err := <-results:
			if err != nil {
				t.Error(err)
			}
			finished++
		case <-ticker.C:
			for _, destination := range destinations {
				entries, err := os.ReadDir(filepath.Join(path, string(destination)))
				if err != nil {
					t.Fatal(err)
				}
				for _, entry := range entries {
					if !strings.HasSuffix(entry.Name(), ".md") {
						continue
					}
					data, err := os.ReadFile(filepath.Join(path, string(destination), entry.Name()))
					if err != nil {
						t.Fatal(err)
					}
					if _, err := Parse(data); err != nil {
						t.Fatalf("observed partially published record: %v", err)
					}
				}
			}
		}
	}
	count := 0
	for _, destination := range destinations {
		entries, _ := os.ReadDir(filepath.Join(path, string(destination)))
		for _, entry := range entries {
			if !strings.HasSuffix(entry.Name(), ".md") {
				t.Fatalf("unexpected leftover staging file %q", entry.Name())
			}
			id := strings.TrimSuffix(entry.Name(), ".md")
			document, err := store.Read(id)
			if err != nil || len(document.Body) != 128*1024 {
				t.Fatalf("incomplete saved record %q: %v", id, err)
			}
			count++
		}
	}
	if count != workers*8 {
		t.Fatalf("expected %d records, found %d", workers*8, count)
	}
}

func TestStoreLockIsReleasedWhenWriterProcessDies(t *testing.T) {
	path := t.TempDir()
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	command := helperProcess(t, "hold-lock", path)
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer command.Process.Kill()
	if line, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || line != "locked\n" {
		t.Fatalf("writer did not acquire the lock: %q, %v", line, err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := store.Create(Memories, nil, "After interruption")
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("creation bypassed another process's lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = command.Wait()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("creation remained blocked after lock owner died")
	}
}

// TestStoreProcess is a subprocess entry point, not an additional public API.
func TestStoreProcess(t *testing.T) {
	role, path := os.Getenv("MD_MEMORY_TEST_ROLE"), os.Getenv("MD_MEMORY_TEST_PATH")
	if role == "" {
		return
	}
	if role == "hold-lock" {
		lock := flock.New(filepath.Join(path, ".lock"))
		if err := lock.Lock(); err != nil {
			t.Fatal(err)
		}
		defer lock.Close()
		fmt.Println("locked")
		_, _ = io.Copy(io.Discard, os.Stdin)
		return
	}
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 8 {
		if _, err := store.Create(destinations[i%len(destinations)], map[string]any{"title": "Concurrent"}, strings.Repeat("x", 128*1024)); err != nil {
			t.Fatal(err)
		}
	}
}
