package songya

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
)

// SemanticRecallQuery is one source message, never a conversation-wide query.
type SemanticRecallQuery struct {
	Text  string `json:"text"`
	Limit int    `json:"limit"`
}
type Recollection struct {
	ID      string  `json:"id"`
	Score   float64 `json:"score"`
	Content string  `json:"content"`
}
type SemanticRecallResult struct {
	Mode        string         `json:"mode"`
	Items       []Recollection `json:"items"`
	Incomplete  bool           `json:"incomplete"`
	Diagnostics []Diagnostic   `json:"diagnostics,omitempty"`
}

func cosine(a, b []float32) float64 {
	if len(a) != len(b) {
		return -1
	}
	var score float64
	for i, n := range a {
		score += float64(n) * float64(b[i])
	}
	return min(1, score)
}
func chunkScore(a, b []embeddedChunk) float64 {
	score := -1.0
	for _, x := range a {
		for _, y := range b {
			score = max(score, cosine(x.Vector, y.Vector))
		}
	}
	return score
}

// RecallSemantic scans/indexes once for the whole batch. Query vectors use the
// same content/config cache as indexed records, so unchanged messages are reused.
// Owners are explicit; shared identities can rank only the selected owners' accounts.
// There is no lexical fallback: unavailable embeddings produce honest diagnostics.
func (s *Store) RecallSemantic(ctx context.Context, queries []SemanticRecallQuery, owners []string) ([]SemanticRecallResult, error) {
	c, err := s.Scan(ctx)
	if err != nil {
		return nil, err
	}
	c = c.ForUsers(owners)
	active := []Record{}
	for _, r := range c.Records {
		if r.ID != "" && (len(r.Redirects) == 0 || r.Unconfirmed) {
			active = append(active, r)
		}
	}
	c.Records = active
	e, err := s.embeddingSession()
	if err != nil {
		return nil, err
	}
	defer e.root.Close()
	queryVectors := make([][]embeddedChunk, len(queries))
	results := make([]SemanticRecallResult, len(queries))
	for i, q := range queries {
		results[i] = SemanticRecallResult{Mode: "semantic", Items: []Recollection{}}
		queryVectors[i], err = e.vectors(ctx, q.Text, true)
		if err != nil {
			results[i].Incomplete = true
			results[i].Diagnostics = append(results[i].Diagnostics, Diagnostic{"query", err.Error()})
		}
	}
	vectors := map[string][]embeddedChunk{}
	diagnostics := append([]Diagnostic{}, c.Diagnostics...)
	for _, r := range c.Records {
		if text := embeddingText(r); text != "" {
			vectors[r.ID], err = e.vectors(ctx, text, true)
			if err != nil {
				diagnostics = append(diagnostics, Diagnostic{r.Source, err.Error()})
			}
		}
	}
	if e.cacheIssue != nil {
		diagnostics = append(diagnostics, Diagnostic{".index/embeddings", "derived cache unavailable; results remain usable"})
	}
	records := catalogMap(c)
	edges := graphEdges(records)
	for i, q := range queries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		result := &results[i]
		result.Incomplete = result.Incomplete || len(diagnostics) > 0
		result.Diagnostics = boundedDiagnostics(append(result.Diagnostics, diagnostics...))
		if len(queryVectors[i]) == 0 {
			continue
		}
		entities := []Recollection{}
		for _, r := range c.Records {
			if r.Kind == Entities && len(vectors[r.ID]) > 0 {
				entities = append(entities, Recollection{ID: r.ID, Score: chunkScore(queryVectors[i], vectors[r.ID])})
			}
		}
		sort.Slice(entities, func(i, j int) bool {
			if entities[i].Score != entities[j].Score {
				return entities[i].Score > entities[j].Score
			}
			return entities[i].ID < entities[j].ID
		})
		linked := map[string]float64{}
		for _, entity := range entities[:min(3, len(entities))] {
			for _, edge := range edges {
				target := ""
				if edge.Source == entity.ID {
					target = edge.Target
				}
				if edge.Target == entity.ID {
					target = edge.Source
				}
				if r := records[target]; r.Kind == Memories {
					if previous, ok := linked[target]; !ok || entity.Score > previous {
						linked[target] = entity.Score
					}
				}
			}
		}
		for _, r := range c.Records {
			if (r.Kind != Memories && r.Kind != Entities) || len(vectors[r.ID]) == 0 {
				continue
			}
			score := chunkScore(queryVectors[i], vectors[r.ID])
			if entity, ok := linked[r.ID]; ok {
				score = max(score, (score+entity)/2)
			}
			result.Items = append(result.Items, Recollection{ID: r.ID, Score: score})
		}
		sort.Slice(result.Items, func(i, j int) bool {
			if result.Items[i].Score != result.Items[j].Score {
				return result.Items[i].Score > result.Items[j].Score
			}
			return result.Items[i].ID < result.Items[j].ID
		})
		limit := q.Limit
		if limit <= 0 {
			limit = 10
		}
		result.Items = result.Items[:min(limit, len(result.Items))]
		for j := range result.Items {
			result.Items[j].Content = recollectionText(records[result.Items[j].ID], c.Records, records)
		}
	}
	return results, ctx.Err()
}

func recollectionText(r Record, records []Record, byID map[string]Record) string {
	connections := []map[string]string{}
	for _, edge := range r.Relations {
		if target := byID[edge.Target]; target.Kind == Entities || target.Kind == Topics {
			name := target.Name
			if name == "" {
				name = target.Title
			}
			connections = append(connections, map[string]string{"type": edge.Type, "target": target.ID, "name": name})
		}
	}
	label, _ := json.Marshal(map[string]any{
		"id": r.ID, "kind": r.Kind, "user": r.User, "title": r.Title, "time": r.Time,
		"name": r.Name, "aliases": r.Aliases, "description": clip(r.Description, 512),
		"relations": r.Relations, "connections": connections,
		"pending": r.Pending, "unconfirmed": r.Unconfirmed, "diagnostics": r.Diagnostics,
	})
	body := r.Body
	if r.Kind == Entities {
		body = clip(body, 512)
	}
	text := string(label) + "\n" + body
	if r.Context != "" {
		text += "\n" + r.Context
	}
	// A directly containing story supplies its introduction and neighboring events.
	for _, story := range records {
		if story.Kind != Stories {
			continue
		}
		members := storyMembers(story)
		for index, member := range members {
			if member != r.ID {
				continue
			}
			storyLabel, _ := json.Marshal(map[string]any{"id": story.ID, "user": story.User, "title": story.Title, "time": story.Time})
			text += fmt.Sprintf("\nStory context %s:\n%s", storyLabel, clip(story.Body, 600))
			for _, neighbor := range members[max(0, index-1):min(len(members), index+2)] {
				if event, exists := byID[neighbor]; exists && neighbor != r.ID {
					text += fmt.Sprintf("\n%s (%s): %s", event.Title, event.ID, clip(event.Body, 600))
				}
			}
			return text
		}
	}
	return text
}
