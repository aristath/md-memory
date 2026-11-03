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
	metadata, err := created.Metadata()
	if err != nil {
		panic(err)
	}
	read, err := store.Read(metadata.ID)
	if err != nil {
		panic(err)
	}
	fmt.Println(read.Body)
	// Output: Markdown body.
}
