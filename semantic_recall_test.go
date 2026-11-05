package songya

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSemanticRecallBatchOwnershipConnectionsStoriesAndCache(t *testing.T) {
	f := embeddingService(t)
	s, _ := Open(t.TempDir(), EmbeddingConfig{BaseURL: f.server.URL})
	entity := createEmbeddedRecord(t, s, Entities, map[string]any{"name": "Alex"}, "A violoncello player")
	connected := createEmbeddedRecord(t, s, Memories, map[string]any{"user": "ari", "time": map[string]any{}, "emotion": "uncertain tenderness", "relations": []Relation{{Type: "related_to", Target: entity.ID}}}, "Around college, we talked after rehearsal.")
	clara := createEmbeddedRecord(t, s, Memories, map[string]any{"user": "clara"}, "I enjoyed the violoncello performance.")
	createEmbeddedRecord(t, s, Memories, map[string]any{"user": "bob"}, "Bob's private violoncello account")
	createEmbeddedRecord(t, s, Memories, map[string]any{"user": "ari"}, "Unrelated account.")
	before := createEmbeddedRecord(t, s, Memories, map[string]any{"user": "ari"}, "Before the rehearsal")
	after := createEmbeddedRecord(t, s, Memories, map[string]any{"user": "ari"}, "Afterwards we left.")
	story := createEmbeddedRecord(t, s, Stories, map[string]any{"user": "ari", "title": "College", "relations": []Relation{{"related_to", before.ID}, {"related_to", connected.ID}, {"related_to", after.ID}}}, "College rehearsals. "+strings.Repeat("x", 2000)+" FAR AWAY")
	createEmbeddedRecord(t, s, Stories, map[string]any{"user": "bob", "relations": []Relation{{"related_to", connected.ID}}}, "OTHER OWNER")
	queries := []SemanticRecallQuery{{Text: "orchestral performer", Limit: 10}, {Text: "garden", Limit: 1}}
	results, err := s.RecallSemantic(context.Background(), queries, []string{"ari", "clara"})
	if err != nil || len(results) != 2 || results[0].Incomplete {
		t.Fatal(results, err)
	}
	if len(results[0].Items) != 6 || len(results[1].Items) != 1 {
		t.Fatal(results)
	}
	seen := map[string]Recollection{}
	for _, r := range results[0].Items {
		seen[r.ID] = r
		if strings.Contains(r.Content, "OTHER OWNER") || strings.Contains(r.Content, "Bob's") {
			t.Fatal("wrong owner", r)
		}
	}
	if seen[clara.ID].Score < 0.99 || seen[connected.ID].Score < 0.49 {
		t.Fatal("entity connection or Clara memory lost", seen)
	}
	for _, word := range []string{connected.ID, "ari", "Around college", "uncertain tenderness", "Alex", story.ID, "Before the rehearsal", "Afterwards we left"} {
		if !strings.Contains(seen[connected.ID].Content, word) {
			t.Fatal("recall lost context", word, seen)
		}
	}
	if strings.Contains(seen[connected.ID].Content, "FAR AWAY") {
		t.Fatal("unbounded story")
	}
	count := f.count()
	_, err = s.RecallSemantic(context.Background(), queries, []string{"ari", "clara"})
	if err != nil || f.count() != count {
		t.Fatal("unchanged messages or records re-embedded", err, f.count(), count)
	}
	doc, _ := s.Read(connected.ID)
	doc.Body = "A garden of roses."
	raw, _ := doc.Marshal()
	if err := os.WriteFile(filepath.Join(s.path, "memories", connected.ID+".md"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	changed, err := s.RecallSemantic(context.Background(), queries, []string{"ari", "clara"})
	if err != nil || f.count() != count+1 || changed[1].Items[0].ID != connected.ID {
		t.Fatal("edit/cache freshness", changed, err, f.count(), count)
	}
	changedModel, _ := Open(s.path, EmbeddingConfig{BaseURL: f.server.URL, Model: "different-model"})
	_, err = changedModel.RecallSemantic(context.Background(), queries, []string{"ari", "clara"})
	if err != nil || f.count() <= count+1 {
		t.Fatal("model cache not invalidated", err)
	}
}

func TestSemanticRecallFailureNeverProducesLexicalScoresAndCacheRecovers(t *testing.T) {
	f := embeddingService(t)
	s, _ := Open(t.TempDir(), EmbeddingConfig{BaseURL: f.server.URL})
	memory := createEmbeddedRecord(t, s, Memories, map[string]any{"user": "ari"}, "A violoncello performance")
	query := []SemanticRecallQuery{{Text: "orchestral", Limit: 1}}
	_, err := s.RecallSemantic(context.Background(), query, []string{"ari"})
	if err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(s.path, ".index", "embeddings")
	entries, _ := os.ReadDir(cache)
	for _, entry := range entries {
		if err := os.WriteFile(filepath.Join(cache, entry.Name()), []byte("damaged"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	recovered, err := s.RecallSemantic(context.Background(), query, []string{"ari"})
	if err != nil || recovered[0].Incomplete || recovered[0].Items[0].ID != memory.ID {
		t.Fatal("cache not healed", recovered, err)
	}
	f.mu.Lock()
	f.status = func(string) int { return http.StatusServiceUnavailable }
	f.mu.Unlock()
	failed, err := s.RecallSemantic(context.Background(), []SemanticRecallQuery{{Text: "a new uncached message", Limit: 1}}, []string{"ari"})
	if err != nil || !failed[0].Incomplete || len(failed[0].Items) != 0 || failed[0].Mode != "semantic" || len(failed[0].Diagnostics) == 0 {
		t.Fatal("failure mistaken for semantic results", failed, err)
	}
	if _, err := s.Create(Memories, map[string]any{"user": "ari"}, "A memory saved while embeddings are offline"); err != nil {
		t.Fatal("embedding failure blocked writes", err)
	}
	f.mu.Lock()
	f.status = nil
	f.mu.Unlock()
	retried, err := s.RecallSemantic(context.Background(), query, []string{"ari"})
	if err != nil || retried[0].Incomplete {
		t.Fatal("service did not recover", retried, err)
	}
}
