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
		value["relations"] = record.Relations
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
		entities, topics := []any{}, []any{}
		for _, edge := range record.Relations {
			target, ok := records[edge.Target]
			if !ok && songya.ValidID(edge.Target) {
				if doc, err := store.Read(edge.Target); err == nil {
					target, ok = doc.Record(kindFor(edge.Target)), true
				}
			}
			if !ok {
				continue
			}
			connection := map[string]any{"id": edge.Target, "name": target.Title, "relation": edge.Type}
			switch target.Kind {
			case songya.Entities:
				entities = append(entities, connection)
			case songya.Topics:
				topics = append(topics, connection)
			}
		}
		if record.Kind == songya.Memories {
			value["entities"], value["topics"] = entities, topics
		}
		if record.Kind == songya.Stories {
			members := []string{}
			for _, edge := range record.Relations {
				if edge.Type == "contains" {
					members = append(members, edge.Target)
				}
			}
			value["members"] = members
		}
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
		"id": str("Existing memory ID to update; omit for a new memory"), "user": str("Owner of a new memory"), "title": str("Optional title"), "text": str("The memory in the user's own words; replaces prose when supplied"), "append": str("Add detail to an existing memory"),
		"time":     map[string]any{"description": "When the event happened: ordinary words or known date bounds", "anyOf": []any{str("Approximate time in ordinary words"), schema(map[string]any{"description": str("When it happened"), "earliest": str("Earliest known date"), "latest": str("Latest known date")})}},
		"entities": array(namedSchema(true)), "topics": array(namedSchema(false)), "metadata": object(), "relations": array(schema(map[string]any{"type": str("Relationship"), "target": str("Existing record ID")}, "type", "target")),
	})
}
func namedSchema(entity bool) map[string]any {
	fields := map[string]any{"id": str("Existing ID; omit to reuse a unique matching name or create a record"), "name": str("Name, e.g. John Pavlou or Pizza AMI"), "aliases": array(str("Other known name")), "description": str("Known facts about this person, place or subject"), "append": str("Add facts to an existing description"), "relation": str("How this relates to the memory, e.g. with or at"), "metadata": object(), "relations": array(schema(map[string]any{"type": str("Relationship"), "target": str("Existing record ID")}, "type", "target"))}
	if entity {
		fields["type"] = str("Free-form type, e.g. person, place or organization")
	}
	return schema(fields)
}
