package songya

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestConcurrentCapturesShareOneEntity(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var writers sync.WaitGroup
	for i := range 6 {
		writers.Go(func() {
			_, err := store.SaveMemory(context.Background(), MemoryInput{Key: fmt.Sprintf("capture-%d", i), User: "ari", Text: textValue(fmt.Sprintf("Meeting %d with John", i)), Entities: []NamedInput{{Name: "John", Type: "person", Relation: "with"}}})
			if err != nil {
				t.Error(err)
			}
		})
	}
	writers.Wait()
	catalog, err := store.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	entities, memories := 0, 0
	for _, r := range catalog.Records {
		switch r.Kind {
		case Entities:
			entities++
		case Memories:
			memories++
			if len(r.Relations) != 1 {
				t.Fatal(r)
			}
		}
	}
	if entities != 1 || memories != 6 {
		t.Fatal("concurrent capture duplicated an identity", entities, memories)
	}
}
func TestRepeatedParticipantKeepsEveryFact(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		receipt, err := store.SaveMemory(context.Background(), MemoryInput{Key: fmt.Sprint(i), User: "ari", Text: textValue("Dinner with John"), Entities: []NamedInput{{Name: "John", Type: "person", Aliases: []string{"Johnny"}, Description: textValue("John grows roses."), Relation: "with"}, {Name: "Johnny", Description: textValue("John plays guitar."), Relation: "friend"}}})
		if err != nil {
			t.Fatal(err)
		}
		memory, _ := store.Read(receipt.Refs["memory"])
		doc, err := store.Read(memory.Metadata().Relations[0].Target)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Count(doc.Body, "John grows roses.") != 1 || strings.Count(doc.Body, "John plays guitar.") != 1 {
			t.Fatal("participant details lost", doc.Body)
		}
	}
}
func TestBrokenUnrelatedEntityDoesNotHideHealthyMatch(t *testing.T) {
	path := t.TempDir()
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	john, err := store.Create(Entities, map[string]any{"name": "John"}, "A friend")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/nonexistent", filepath.Join(path, "entities", "e_000000000000.md")); err != nil {
		t.Fatal(err)
	}
	receipt, err := store.SaveMemory(context.Background(), MemoryInput{Key: "healthy", User: "ari", Text: textValue("Dinner with John"), Entities: []NamedInput{{Name: "John"}}})
	if err != nil {
		t.Fatal("unrelated damage blocked a known identity", receipt, err)
	}
	memory, _ := store.Read(receipt.Refs["memory"])
	if len(memory.Metadata().Relations) != 1 || memory.Metadata().Relations[0].Target != john.ID {
		t.Fatal(memory)
	}
}
func TestUnusableEntityDirectoryStillPreservesMemory(t *testing.T) {
	path := t.TempDir()
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(path, "entities")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "entities"), []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	receipt, err := store.SaveMemory(context.Background(), MemoryInput{Key: "preserve", User: "ari", Text: textValue("Dinner with John"), Entities: []NamedInput{{Name: "John", Description: textValue("A long-standing friend")}}})
	if err != nil || receipt.Status != "committed" {
		t.Fatal(receipt, err)
	}
	doc, err := store.Read(receipt.Refs["memory"])
	if err != nil || doc.Body != "Dinner with John" || len(doc.Diagnostics()) == 0 {
		t.Fatal(doc, err)
	}
	data, _ := doc.Marshal()
	if !strings.Contains(string(data), "A long-standing friend") {
		t.Fatal("unresolved input was discarded", string(data))
	}
}

func TestStoryTextEditsKeepMembershipAndMembersReplaceInOrder(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a, _ := store.Create(Memories, map[string]any{"user": "ari"}, "First")
	b, _ := store.Create(Memories, map[string]any{"user": "ari"}, "Second")
	created, err := store.SaveStory(context.Background(), StoryInput{Key: "create", User: "ari", Text: textValue("A narrative"), Members: []string{a.ID, b.ID}})
	if err != nil {
		t.Fatal(err)
	}
	id := created.Refs["story"]
	if _, err := store.SaveStory(context.Background(), StoryInput{Key: "edit", ID: id, Text: textValue("Revised narrative")}); err != nil {
		t.Fatal(err)
	}
	doc, _ := store.Read(id)
	if !strings.Contains(doc.Body, a.ID) || !strings.Contains(doc.Body, b.ID) || !strings.Contains(doc.Body, "Revised narrative") {
		t.Fatal(doc.Body)
	}
	input := StoryInput{Key: "order", ID: id, Members: []string{b.ID, a.ID}}
	if _, err := store.SaveStory(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveStory(context.Background(), input); err != nil {
		t.Fatal("retry failed", err)
	}
	doc, _ = store.Read(id)
	if strings.Index(doc.Body, b.ID) > strings.Index(doc.Body, a.ID) {
		t.Fatal(doc.Body)
	}
	if _, err := store.SaveStory(context.Background(), StoryInput{Key: "clear", ID: id, Members: []string{}}); err != nil {
		t.Fatal(err)
	}
	doc, _ = store.Read(id)
	if strings.Contains(doc.Body, "[[") || !strings.Contains(doc.Body, "Revised narrative") {
		t.Fatal(doc.Body)
	}
}

func TestCaptureReusesIdentityWithoutRewritingAndKeepsCanonicalName(t *testing.T) {
	path := t.TempDir()
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	john, err := store.SaveNamed(context.Background(), Entities, NamedInput{Name: "John Pavlou", Aliases: []string{"Johnny"}, Description: textValue("A friend.")})
	if err != nil {
		t.Fatal(err)
	}
	id := john.Refs["record"]
	filename := filepath.Join(path, "entities", id+".md")
	before, _ := os.ReadFile(filename)
	_, err = store.SaveMemory(context.Background(), MemoryInput{User: "ari", Text: textValue("Coffee with Johnny"), Entities: []NamedInput{{Name: "Johnny"}}})
	if err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filename)
	if string(before) != string(after) {
		t.Fatal("reusing an identity rewrote it")
	}
	_, err = store.SaveNamed(context.Background(), Entities, NamedInput{Name: "Johnny", Append: "A gardener."})
	if err != nil {
		t.Fatal(err)
	}
	doc, _ := store.Read(id)
	if doc.Record(Entities).Title != "John Pavlou" {
		t.Fatal("alias lookup renamed the canonical record", doc)
	}
	_, err = store.SaveMemory(context.Background(), MemoryInput{User: "ari", Text: textValue("Another meeting"), Entities: []NamedInput{{ID: id, Name: "John P."}}})
	if err != nil {
		t.Fatal(err)
	}
	doc, _ = store.Read(id)
	if !strings.Contains(strings.Join(doc.Record(Entities).Aliases, ","), "John P.") {
		t.Fatal("new known alias lost", doc)
	}
}

func TestRelationsReplacePreserveClearAndResolveNames(t *testing.T) {
	for _, kind := range []Destination{Memories, Entities, Topics, Stories} {
		t.Run(string(kind), func(t *testing.T) {
			store, err := Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			save := func(key, id string, relations []Relation) Receipt {
				t.Helper()
				var r Receipt
				var err error
				switch kind {
				case Memories:
					r, err = store.SaveMemory(context.Background(), MemoryInput{Key: key, ID: id, User: "ari", Text: textValue("A memory"), Relations: relations})
				case Stories:
					r, err = store.SaveStory(context.Background(), StoryInput{Key: key, ID: id, User: "ari", Text: textValue("A story"), Relations: relations})
				default:
					r, err = store.SaveNamed(context.Background(), kind, NamedInput{Key: key, ID: id, Name: "Cedar Café", Relations: relations})
				}
				if err != nil {
					t.Fatal(r, err)
				}
				return r
			}
			r := save("create", "", []Relation{{Type: "liked_by", Target: "e_daphne"}})
			id := r.Records[0].ID
			input := []Relation{{Type: "liked_by", Target: "Sam Lee"}}
			repaired := save("repair", id, input)
			if input[0].Target != "Sam Lee" {
				t.Fatal("caller input mutated", input)
			}
			if len(repaired.Diagnostics) > 0 {
				t.Fatal(repaired.Diagnostics)
			}
			doc, _ := store.Read(id)
			relations := doc.Metadata().Relations
			if len(relations) != 1 || !ValidID(relations[0].Target) {
				t.Fatal("wrong link not replaced", relations)
			}
			target := relations[0].Target
			// The accepted request replays after reopening, without resolving a new identity.
			store, err = Open(store.path)
			if err != nil {
				t.Fatal(err)
			}
			save("repair", id, input)
			save("preserve", id, nil)
			doc, _ = store.Read(id)
			if len(doc.Metadata().Relations) != 1 || doc.Metadata().Relations[0].Target != target {
				t.Fatal("omission lost links", doc)
			}
			save("clear", id, []Relation{})
			doc, _ = store.Read(id)
			if len(doc.Metadata().Relations) != 0 {
				t.Fatal("empty list did not clear links", doc)
			}
		})
	}
}

func TestLongMemoryEnrichmentKeepsHistoryOutOfEntities(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	body := strings.Repeat("Alex and Sam remember their school days together.\n", 3000)
	created, err := store.SaveMemory(context.Background(), MemoryInput{Key: "long", User: "ari", Text: &body, Time: map[string]any{"earliest": "1995-01-01", "latest": "1998-12-31"}, Entities: []NamedInput{{Name: "Alex Morgan", Type: "person", Description: textValue("A person."), Relation: "about"}, {Name: "Sam Lee", Type: "person", Description: textValue("A school friend."), Relation: "about"}}})
	if err != nil {
		t.Fatal(err)
	}
	id := created.Refs["memory"]
	doc, _ := store.Read(id)
	entities := doc.Metadata().Relations
	before, _ := store.Read(entities[1].Target)
	original, _ := before.Marshal()
	if _, err := store.SaveMemory(context.Background(), MemoryInput{Key: "enrich", ID: id, Append: "They later met at Cedar Café.", Entities: []NamedInput{{Name: "Cedar Café", Type: "place", Relation: "at"}}}); err != nil {
		t.Fatal(err)
	}
	doc, _ = store.Read(id)
	if doc.Body != body+"\n\nThey later met at Cedar Café." || doc.Metadata().User != "ari" || len(doc.Metadata().Relations) != 3 {
		t.Fatal("long memory or its participants changed", len(doc.Body), doc.Metadata())
	}
	after, _ := store.Read(entities[1].Target)
	unchanged, _ := after.Marshal()
	if string(original) != string(unchanged) {
		t.Fatal("enriching the memory rewrote the identity")
	}
	graph, err := store.Graph(context.Background(), GraphQuery{ID: entities[1].Target, Direction: "incoming"})
	if err != nil || len(graph.Edges) != 1 || graph.Edges[0].Source != id {
		t.Fatal("history not reachable from entity", graph, err)
	}
	timeline, err := store.Timeline(context.Background(), Query{Kind: Memories, User: "ari"})
	if err != nil {
		t.Fatal(err)
	}
	if len(timeline.Records) != 1 || timeline.Records[0].ID != id {
		t.Fatal("school history missing from timeline", timeline)
	}
	if doc.Record(Memories).Time.Earliest != "1995-01-01" || doc.Record(Memories).Time.Latest != "1998-12-31" {
		t.Fatal("time span lost")
	}
}

func TestAmbiguousNamedRelationSurvivesAndCanBeCorrected(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	first, _ := store.Create(Entities, map[string]any{"name": "Sam Lee"}, "One person")
	store.Create(Entities, map[string]any{"name": "Sam Lee"}, "Another person")
	r, err := store.SaveMemory(context.Background(), MemoryInput{Key: "ambiguous-target", User: "ari", Text: textValue("A shared preference"), Relations: []Relation{{Type: "about", Target: "Sam Lee"}}})
	if err != nil || r.Status != "committed" || len(r.Diagnostics) == 0 {
		t.Fatal("ambiguous connection blocked the memory or vanished", r, err)
	}
	id := r.Refs["memory"]
	doc, _ := store.Read(id)
	if len(doc.Metadata().Relations) != 1 || doc.Metadata().Relations[0].Target != "Sam Lee" {
		t.Fatal("unresolved intent lost", doc)
	}
	r, err = store.SaveMemory(context.Background(), MemoryInput{Key: "correct-target", ID: id, Relations: []Relation{{Type: "about", Target: first.ID}}})
	if err != nil || len(r.Diagnostics) > 0 {
		t.Fatal("corrected relation still diagnosed as broken", r, err)
	}
}
