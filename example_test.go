package memory_test

import (
	"fmt"
	"os"

	memory "github.com/aristath/md-memory"
)

func ExampleStore() {
	path, err := os.MkdirTemp("", "md-memory-example-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(path)
	store, err := memory.Open(path)
	if err != nil {
		panic(err)
	}
	created, err := store.Create(memory.Memories, map[string]any{
		"title":  "Example",
		"custom": map[string]any{"labels": []string{"one", "two"}},
	}, "Markdown body.")
	if err != nil {
		panic(err)
	}
	read, err := store.Read(created.ID)
	if err != nil {
		panic(err)
	}
	fmt.Println(read.Body)
	updated, err := store.Update(read.ID, read.Revision, map[string]any{"title": "Edited"}, "More detail.")
	if err != nil {
		panic(err)
	}
	fmt.Println(updated.Body)
	// Output:
	// Markdown body.
	// More detail.
}
