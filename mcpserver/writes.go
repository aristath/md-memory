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
		if saved.ID == id {
			value["outcome"], value["changed_fields"] = saved.Outcome, saved.ChangedFields
			if saved.Outcome == "unchanged" {
				diagnostics = append(diagnostics, songya.Diagnostic{Path: id, Message: "No memory fields changed. To add information, supply append; to replace prose, supply text; or supply the time, relations, title, user or metadata to update. Do not repeat the same call expecting new facts to be saved."})
			}
		}
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
	timing := schema(map[string]any{
		"date":     nullableDate("Exact event date; use null when only bounds are known"),
		"earliest": nullableDate("Earliest supported date, using relative timing and related events too"),
		"latest":   nullableDate("Latest supported date"),
	})
	timing["type"] = []string{"object", "null"}
	timing["description"] = "When the event happened, not when saved. Omission preserves time. Supplied components update only those components; null clears a component. time:null clears all three. To switch from an exact date to a range, include date:null. Keep temporal wording and the basis of inferred bounds in prose."
	value := schema(map[string]any{
		"id":     str("Existing memory ID for append or update; omit to create a new memory"),
		"user":   str("Owner: required for creation unless supplied by the integration. Omit on update to preserve ownership; supply only to change it"),
		"title":  str("Memory title; omission preserves it"),
		"text":   str("Complete standalone memory prose. Preserve meaning, attribution and meaningful quotations without inventing facts. Creates a memory without id; replaces the entire prose with id. Omit to preserve prose; an empty string explicitly clears it on update"),
		"append": str("Additional prose to append literally, separated by a blank line. Supply id to enrich an existing memory. Existing prose stays intact unless text is also supplied: text replaces first, then append is added"),
		"time":   timing, "metadata": object(), "relations": relationsSchema(),
	})
	updates := []any{}
	for _, name := range []string{"text", "append", "time", "relations", "title", "metadata", "user"} {
		updates = append(updates, map[string]any{"required": []string{name}})
	}
	value["anyOf"] = []any{
		map[string]any{"required": []string{"text"}, "not": map[string]any{"required": []string{"id"}}},
		map[string]any{"required": []string{"id"}, "anyOf": updates},
	}
	value["examples"] = []any{
		map[string]any{"text": "Ari was born in Thessaloniki on 6 March 1981.", "user": "ari", "time": map[string]any{"date": "1981-03-06"}},
		map[string]any{"id": "EXISTING_MEMORY_ID", "append": "His family moved a few months after his birth."},
		map[string]any{"id": "EXISTING_MEMORY_ID", "text": "The complete corrected account, including the details to keep."},
		map[string]any{"id": "EXISTING_MEMORY_ID", "time": map[string]any{"date": nil, "earliest": "1981-05-01", "latest": "1981-12-31"}},
		map[string]any{"id": "EXISTING_MEMORY_ID", "relations": []any{map[string]any{"type": "related_to", "target": "EXISTING_ENTITY_ID"}, map[string]any{"type": "related_to", "target": "NEW_ENTITY_ID"}}},
	}
	return value
}

const memoryDescription = `Create, append to, or update a lasting memory. Before saving, prepare_memory finds relevant existing records; read enough of a record before changing it.
CREATE: omit id; supply standalone prose in text and a user unless the integration supplies it. Include known event dates in time and connections in relations.
APPEND: supply id and append with the new information. This literally adds a blank line and the additional prose. Supplied relations are added, preserving existing connections.
UPDATE: supply id and the fields to change. text replaces all prose; include everything to keep. If text and append are both supplied, replace first, then append. Date-only, connection-only, title-only and metadata-only updates are allowed. Supplied relations replace the list without nonempty append; relations:[] always clears it. To add only a connection, read and include the existing connections to keep along with the new one.
Omitted fields are preserved. Metadata patches supplied keys, including explicit null values; metadata:{} preserves it. time patches supplied components; component null clears it; time:null clears all event dates. title:"" clears the title. IDs and save timestamps are automatic.
Use separate connected memories for separately dated events. Derive supported date bounds from the whole account, including relative timing; preserve original temporal wording and explain inferred bounds in prose. Never invent an exact date.
Check outcome and changed_fields against the facts you meant to save. created/updated mean changed data was durably committed; unchanged means no fields changed. Do not claim new facts were saved or repeat an unchanged call.`

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
