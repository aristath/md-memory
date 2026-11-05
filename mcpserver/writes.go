package mcpserver

import (
	"context"
	"unicode/utf8"

	"github.com/aristath/songya"
)

type operationKey struct{}

func requestKey(ctx context.Context) string { key, _ := ctx.Value(operationKey{}).(string); return key }

// Present the actual accepted records, including reused identities, without
// exposing temporary batch references or asking the model to coordinate writes.
func savedWrite(ctx context.Context, store *songya.Store, receipt songya.Receipt, main string, writeErr error) (any, error) {
	status := receipt.Status
	if status == "" {
		status = "not_accepted"
	}
	value := map[string]any{"status": status, "operation_id": receipt.Key, "diagnostics": receipt.Diagnostics}
	id := receipt.Refs[main]
	value["id"] = id
	diagnostics := append([]songya.Diagnostic{}, receipt.Diagnostics...)
	records := map[string]songya.Record{}
	for _, saved := range receipt.Records {
		data, err := store.BatchData(ctx, receipt.Key, saved.ID, "proposed")
		if err != nil {
			diagnostics = append(diagnostics, songya.Diagnostic{Path: saved.ID, Message: "Saved snapshot unavailable; read the record by ID: " + err.Error()})
			continue
		}
		doc := songya.Parse(data)
		doc.ID = saved.ID
		records[saved.ID] = doc.Record(kindFor(saved.ID))
	}
	if record, ok := records[id]; ok {
		if record.Kind == songya.Entities || record.Kind == songya.Topics {
			value["name"], value["description"], value["aliases"] = record.Title, clipText(record.Body, 2048), record.Aliases
			value["description_truncated"] = len(record.Body) > 2048
		} else {
			value["text"], value["title"], value["text_truncated"] = clipText(record.Body, 2048), record.Title, len(record.Body) > 2048
			if record.User != "" {
				value["user"] = record.User
			}
			if record.Kind == songya.Memories {
				value["time"] = record.Time
			}
		}
		connections := []any{}
		for _, edge := range record.Relations {
			connection := map[string]any{"type": edge.Type, "target": edge.Target}
			target, ok := records[edge.Target]
			if !ok && songya.ValidID(edge.Target) {
				if doc, err := store.Read(edge.Target); err == nil {
					target, ok = doc.Record(kindFor(edge.Target)), true
				}
			}
			if ok {
				connection["name"], connection["kind"] = target.Title, target.Kind
			} else {
				diagnostics = append(diagnostics, songya.Diagnostic{Path: edge.Target, Message: "Connection target unavailable; read the record to repair it"})
			}
			connections = append(connections, connection)
		}
		value["relations"] = connections
	}
	value["diagnostics"] = diagnostics
	return value, writeErr
}
func clipText(text string, n int) string {
	if len(text) <= n {
		return text
	}
	for n > 0 && !utf8.ValidString(text[:n]) {
		n--
	}
	return text[:n] + "…"
}

func memorySchema() map[string]any {
	return schema(map[string]any{
		"id": str("Existing memory ID to update; omit for a new memory"), "user": str("Owner of a new memory"), "title": str("Optional title"), "text": str("The memory in the user's own words; replaces prose when supplied"), "append": str("Enrich this memory with more detail; existing prose and links stay intact"),
		"time": schema(map[string]any{
			"date":     nullableDate("Exact event date"),
			"earliest": nullableDate("Earliest event date; null for an open bound"),
			"latest":   nullableDate("Latest event date; null for an open bound"),
		}),
		"metadata": object(), "relations": relationsSchema(),
	})
}
func namedSchema(entity bool) map[string]any {
	fields := map[string]any{"id": str("Existing ID; omit to reuse a unique matching name or create a record"), "name": str("Name, e.g. Alex Morgan or Cedar Café"), "aliases": array(str("Other known name")), "description": str("Brief identifying description; experiences, preferences and history belong in linked memories"), "append": str("Add a brief identifying detail"), "metadata": object(), "relations": relationsSchema()}
	if entity {
		fields["type"] = str("Free-form type, e.g. person, place or organization")
	}
	return schema(fields)
}

func nullableDate(description string) map[string]any {
	return map[string]any{"type": []string{"string", "null"}, "description": description + "; YYYY-MM-DD or null"}
}

func relationsSchema() map[string]any {
	named := schema(map[string]any{
		"kind": map[string]any{"type": "string", "enum": []string{"entities", "topics"}},
		"name": str("Name to create or reuse"), "type": str("Optional entity type"),
		"description": str("Brief identity or subject description"), "aliases": array(str("Equivalent name")),
	}, "name")
	value := array(schema(map[string]any{
		"type":   map[string]any{"type": "string", "enum": songya.RelationTypes[:]},
		"target": map[string]any{"anyOf": []any{str("Existing record ID or entity name"), named}},
	}, "type", "target"))
	value["description"] = "Every connection uses this list. Omit to preserve; append adds connections, other saves replace supplied relations; [] clears. Story memory links are in reading order."
	return value
}
