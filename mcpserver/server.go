// Package mcpserver exposes the standalone memory engine over MCP stdio.
package mcpserver

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/aristath/songya"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const Guide = `Songya stores connected Markdown records. IDs, save timestamps and retry bookkeeping are automatic.

Before every save, use prepare_memory(text) to find existing memories, entities and topics. Reuse existing records. One save can create a memory and its new connected identities.

Memories hold events, experiences, facts and preferences. Entities briefly identify people, places and things. Topics identify subjects. Stories compose memories through related_to links in reading order.

Every connection uses relations: type and target. Types: after, caused_by, part_of, related_to, possibly_same_as. Use related_to for entity/topic associations. Reverse connections are automatic. possibly_same_as never merges records.

A target is an existing record ID, an entity name, or an object with kind (entities or topics), name and optional brief description/type/aliases.

save_memory({"text":"Alex and Sam enjoy the lentil soup at Cedar Café.","relations":[{"type":"related_to","target":"Alex"},{"type":"related_to","target":"Sam"},{"type":"related_to","target":{"kind":"entities","name":"Cedar Café","type":"place"}}]})

save_memory creates a memory with text and no id; appends new prose with id and append; or updates selected fields with id and the fields to change. text replaces the full prose; text plus append replaces first, then appends. Omitted fields stay intact. Include enough context for the memory to stand alone, preserving meaning, attribution and meaningful quotations. Use separate connected memories for distinct events, especially when they have different dates. Entity/topic saves use name and description; save_story uses title, introductory text and ordered relations.

Use structured time as well as prose whenever event dates or bounds are supported. date is exact; earliest/latest are inclusive bounds, not asserted exact dates. Combine all temporal information, including related events and relative expressions; preserve the original wording and explain inferred bounds in prose. Do not invent precision. Example: Ari was born on 6 March 1981; his family moved to Mytilene a few months later, still in 1981. With May understood as the earliest plausible month for this account, save birth with time:{"date":"1981-03-06"}, and a separate move memory with time:{"date":null,"earliest":"1981-05-01","latest":"1981-12-31"}, prose explaining the bounds, and an after relation to the birth memory. The earliest bound cannot precede the known birth. This example is not a universal definition of "a few months".

Omitting time preserves it; supplied components patch only those components. Component null clears it; time:null clears all three. Clear date explicitly when changing an exact date to bounds. Omitted relations stay intact; nonempty append adds supplied relations, other saves replace them; [] always clears. Metadata patches supplied keys; omission preserves other keys. IDs and save timestamps are automatic and separate from event time.

Check outcome and changed_fields against the requested information. created/updated confirm a durable change; unchanged means no fields changed. Inspect the returned prose/time/relations, retrieving full text when truncated. Do not claim new facts were saved or repeat the same unchanged call. A publication status of committed alone does not establish that new facts were added.

Memories/stories have an owning user; integrations can supply it. Entities/topics are shared. user:"all" selects everyone. Ownership is separate from linked participants.

recall_memory(text) retrieves connected accounts; search_memory(text,mode=semantic) searches by meaning. Search kind=entities or topics to find identities/subjects. read_memory(id) reads prose; view=source includes metadata. explore_memory(id) follows links. Follow next as offset with the same query/user when results are paged.`

// App keeps import access separate from record-store access. Import roots are
// supplied by the process owner, never accepted as arbitrary client paths.
type App struct {
	Store       *songya.Store
	ImportRoots map[string]string
}

type pageInput struct {
	Key     string `json:"key,omitempty"`
	Archive string `json:"archive,omitempty"`
	ID      string `json:"id,omitempty"`
	Source  string `json:"source,omitempty"`
	Root    string `json:"root,omitempty"`
	View    string `json:"view,omitempty"`
	Offset  int    `json:"offset,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}
type recordPage struct {
	User          string   `json:"user,omitempty"`
	UserTruncated bool     `json:"user_truncated,omitempty"`
	ID            string   `json:"id,omitempty"`
	Source        string   `json:"source,omitempty"`
	Pending       bool     `json:"pending,omitempty"`
	Unconfirmed   bool     `json:"unconfirmed,omitempty"`
	Text          string   `json:"text,omitempty"`
	Base64        string   `json:"base64,omitempty"`
	Offset        int      `json:"offset"`
	Next          int      `json:"next,omitempty"`
	Truncated     bool     `json:"truncated"`
	Diagnostics   []string `json:"diagnostics,omitempty"`
}

func normalize(value any, key string) any {
	if key == "time" && value == nil {
		return map[string]any{"date": nil, "earliest": nil, "latest": nil}
	}
	if key == "relations" {
		switch v := value.(type) {
		case string:
			value = []any{map[string]any{"type": "related_to", "target": v}}
		case map[string]any:
			value = []any{v}
		case []any:
			for i, item := range v {
				if text, ok := item.(string); ok {
					v[i] = map[string]any{"type": "related_to", "target": text}
				}
			}
		}
	}

	if key == "metadata" {
		if value == nil {
			return nil
		}
		if _, ok := value.(map[string]any); ok {
			return value
		}
		return map[string]any{"unparsed_metadata": value}
	}
	if number, ok := value.(json.Number); ok {
		value = number.String()
	}
	switch value := value.(type) {
	case map[string]any:
		for name, item := range value {
			value[name] = normalize(item, name)
		}
		return value
	case []any:
		for i, item := range value {
			value[i] = normalize(item, "")
		}
		return value
	}
	switch key {
	case "offset", "limit", "depth", "start", "end":
		switch v := value.(type) {
		case string:
			text := strings.TrimSpace(v)
			if n, err := strconv.ParseInt(text, 10, 0); err == nil {
				return int(n)
			}
			n, err := strconv.ParseFloat(text, 64)
			maximum := int(^uint(0) >> 1)
			if key == "start" || key == "end" {
				// A reviewed source boundary cannot be guessed from a non-number, fraction,
				// or overflow. Leaving it unconverted produces an actionable decode error.
				if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || n != math.Trunc(n) || n >= float64(maximum) || n < float64(-maximum-1) {
					return v
				}
				return int(n)
			}
			if math.IsInf(n, 1) {
				return maximum
			}
			if math.IsInf(n, -1) {
				return -maximum - 1
			}
			if err != nil || math.IsNaN(n) {
				return 0
			}
			if n >= float64(maximum) {
				return maximum
			}
			if n <= float64(-maximum-1) {
				return -maximum - 1
			}
			return int(n)
		}
	case "user", "key", "original_text", "name", "description", "title", "relation", "id", "kind", "ref", "body", "append", "story", "text", "direction", "type", "related_to", "action", "root", "path", "source", "view", "archive", "relation_type", "target", "mode":
		switch v := value.(type) {
		case bool:
			return strconv.FormatBool(v)
		}
	}
	return value
}
func decode(raw json.RawMessage, target any) error {
	if len(raw) == 0 {
		raw = []byte("{}")
	}
	var input any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&input); err != nil {
		return err
	}
	data, err := json.Marshal(normalize(input, ""))
	if err != nil {
		return err
	}
	decoder = json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	return decoder.Decode(target)
}
func limit(value, maximum, defaultValue int) int {
	if value <= 0 {
		return defaultValue
	}
	if value > maximum {
		return maximum
	}
	return value
}
func schema(properties map[string]any, required ...string) map[string]any {
	value := map[string]any{"type": "object", "properties": properties}
	if len(required) > 0 {
		value["required"] = required
	}
	return value
}

func str(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}
func count() map[string]any {
	return map[string]any{"type": []string{"integer", "string"}, "description": "Optional page size or byte offset; numeric strings are accepted."}
}
func object() map[string]any {
	return map[string]any{"type": "object", "additionalProperties": true, "description": "Optional custom metadata. IDs and creation/update times are automatic."}
}

func array(item any) map[string]any { return map[string]any{"type": "array", "items": item} }

func tool[T any](server *mcp.Server, name, description string, input any, readOnly bool, handler func(context.Context, T) (any, error)) {
	closed := false
	server.AddTool(&mcp.Tool{Name: name, Description: description, InputSchema: input, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: readOnly, OpenWorldHint: &closed}}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var value T
		if err := decode(request.Params.Arguments, &value); err != nil {
			return result(nil, fmt.Errorf("cannot interpret arguments: %w", err)), nil
		}
		if key, ok := request.Params.Meta["songya/request-key"].(string); ok && key != "" {
			ctx = context.WithValue(ctx, operationKey{}, key)
		}
		output, err := handler(ctx, value)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return result(output, err), nil
	})
}

func result(value any, err error) *mcp.CallToolResult {
	summary := "ok"
	if saved, ok := value.(map[string]any); ok {
		if outcome, ok := saved["outcome"].(string); ok {
			summary = outcome
		}
	}
	if receipt, ok := value.(songya.Receipt); ok {
		if receipt.Status == "" {
			receipt.Status = "not_accepted"
		}
		summary = receipt.Status
		value = receiptPage(receipt, 0, 10)
	}
	envelope := map[string]any{"status": "ok", "data": value}
	if err != nil {
		code := "blocked"
		if errors.Is(err, songya.ErrConflict) {
			code = "conflict"
		}
		envelope["status"] = code
		envelope["error"] = clipError(err.Error())
	}
	payload, encodeErr := json.Marshal(envelope)
	if encodeErr != nil {
		return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "Result encoding failed; inspect the durable request receipt before retrying."}}}
	}
	var bounded map[string]any
	_ = json.Unmarshal(payload, &bounded)
	trimDiagnostics(bounded)
	payload, _ = json.Marshal(bounded)
	// Page by the encoded wire budget too: control characters may expand sixfold.
	// Keep both prose and its continuation instead of replacing a readable page.
	for len(payload) > 28*1024 {
		data, ok := bounded["data"].(map[string]any)
		if !ok {
			break
		}
		offset, ok := data["offset"].(float64)
		if !ok {
			break
		}
		text, _ := data["text"].(string)
		raw := []byte(text)
		if encoded, ok := data["base64"].(string); ok {
			raw, _ = base64.StdEncoding.DecodeString(encoded)
		}
		if len(raw) < 2 {
			break
		}
		part := bytePage(raw, 0, len(raw)/2)
		if !part.Truncated {
			break
		}
		delete(data, "text")
		delete(data, "base64")
		if part.Base64 != "" {
			data["base64"] = part.Base64
		} else {
			data["text"] = part.Text
		}
		data["next"] = offset + float64(part.Next)
		data["truncated"] = true
		payload, _ = json.Marshal(bounded)
	}
	if len(payload) > 28*1024 {
		bounded = map[string]any{"status": envelope["status"], "data": map[string]any{"truncated": true, "message": "Result exceeds the response budget. Use a smaller limit or read the receipt with view=source. The durable outcome is unchanged."}}
		if receipt, ok := value.(map[string]any); ok {
			data := bounded["data"].(map[string]any)
			for _, key := range []string{"status", "key", "key_hash", "key_truncated", "record_count", "batch_key", "id", "operation_id"} {
				if item, exists := receipt[key]; exists {
					data[key] = item
				}
			}
		}
		if err != nil {
			bounded["error"] = clipError(err.Error())
		}
	}
	if err != nil {
		summary = fmt.Sprint(envelope["status"])
	}
	return &mcp.CallToolResult{StructuredContent: bounded, IsError: err != nil, Content: []mcp.Content{&mcp.TextContent{Text: summary}}}
}
func clipError(value string) string {
	if len(value) > 1024 {
		return value[:1024]
	}
	return value
}
func trimDiagnostics(value any) {
	switch v := value.(type) {
	case map[string]any:
		for key, item := range v {
			if key == "diagnostics" {
				if items, ok := item.([]any); ok && len(items) > 4 {
					v[key] = items[:4]
					v["diagnostics_truncated"] = true
				}
			} else {
				trimDiagnostics(item)
			}
		}
	case []any:
		for _, item := range v {
			trimDiagnostics(item)
		}
	}
}

func bytePage(data []byte, offset, size int) recordPage {
	if offset < 0 {
		offset = 0
	}
	if offset > len(data) {
		offset = len(data)
	}
	size = limit(size, 12288, 8192)
	if utf8.Valid(data) {
		for offset > 0 && offset < len(data) && !utf8.RuneStart(data[offset]) {
			offset--
		}
	}
	end := offset + size
	if end > len(data) {
		end = len(data)
	}
	if utf8.Valid(data) && end < len(data) {
		for end > offset && !utf8.RuneStart(data[end]) {
			end--
		}
		if end == offset {
			_, n := utf8.DecodeRune(data[offset:])
			end = offset + n
		}
	}
	page := recordPage{Offset: offset, Truncated: end < len(data)}
	if utf8.Valid(data[offset:end]) {
		page.Text = string(data[offset:end])
	} else {
		page.Base64 = base64.StdEncoding.EncodeToString(data[offset:end])
	}
	if page.Truncated {
		page.Next = end
	}
	return page
}
func (a *App) read(ctx context.Context, input pageInput) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Canonical source paths use the same record view and pending status as IDs.
	// Damaged or misplaced filenames remain inspectable by their literal source.
	if input.ID == "" && input.Key == "" && input.Archive == "" && input.Root == "" {
		parts := strings.Split(input.Source, "/")
		if len(parts) == 2 && strings.HasSuffix(parts[1], ".md") {
			id := strings.TrimSuffix(parts[1], ".md")
			if songya.ValidID(id) && string(kindFor(id)) == parts[0] {
				input.ID = id
				input.View = "source"
			}
		}
	}
	var data []byte
	var err error
	user := ""
	pending := false
	unconfirmed := false
	var diagnostics []string
	if input.Key != "" {
		view := input.View
		if view == "original_text" {
			view = "account"
		}
		if view == "" {
			view = "proposed"
		}
		data, err = a.Store.BatchData(ctx, input.Key, input.ID, view)
	} else if input.Archive != "" {
		data, err = a.Store.ReadArchive(input.Archive)
	} else if input.Root != "" {
		root, ok := a.ImportRoots[input.Root]
		if !ok {
			return nil, fmt.Errorf("import root %q is not configured", input.Root)
		}
		data, _, err = a.Store.ImportSource(root, input.Source)
	} else if input.ID != "" {
		doc, readErr := a.Store.Read(input.ID)
		if readErr != nil {
			return nil, readErr
		}
		pending = doc.Pending
		unconfirmed = doc.Unconfirmed
		diagnostics = doc.Diagnostics()
		owned := kindFor(input.ID) == songya.Memories || kindFor(input.ID) == songya.Stories
		if owned {
			user = doc.Metadata().User
		}
		if input.View == "source" {
			source := string(kindFor(input.ID)) + "/" + input.ID + ".md"
			data, err = a.Store.ReadSource(source)
			if err == nil && owned {
				user = songya.Parse(data).Metadata().User
			}
		} else {
			data = []byte(doc.Body)
		}
	} else {
		data, err = a.Store.ReadSource(input.Source)
	}
	if err != nil {
		return nil, err
	}
	page := bytePage(data, input.Offset, input.Limit)
	page.UserTruncated = len(user) > 160
	if page.UserTruncated {
		user = user[:160]
	}
	page.User = user
	page.ID, page.Source, page.Pending, page.Unconfirmed = input.ID, input.Source, pending, unconfirmed
	for _, warning := range diagnostics {
		if len(page.Diagnostics) == 4 {
			break
		}
		page.Diagnostics = append(page.Diagnostics, clipError(warning))
	}
	return page, nil
}
func kindFor(id string) songya.Destination {
	switch id[:2] {
	case "m_":
		return songya.Memories
	case "e_":
		return songya.Entities
	case "t_":
		return songya.Topics
	default:
		return songya.Stories
	}
}

func New(lifetime context.Context, store *songya.Store, roots map[string]string) *mcp.Server {
	app := &App{Store: store, ImportRoots: roots}
	server := mcp.NewServer(&mcp.Implementation{Name: "songya", Version: "0.1.0"}, &mcp.ServerOptions{Instructions: Guide})
	// The SDK keeps request contexts independent of Run's context. Propagate our
	// process lifetime explicitly so shutdown cancels unaccepted lock waiters.
	server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, request mcp.Request) (mcp.Result, error) {
			ctx, cancel := context.WithCancel(ctx)
			stop := context.AfterFunc(lifetime, cancel)
			defer stop()
			defer cancel()
			if err := lifetime.Err(); err != nil {
				return nil, err
			}
			return next(ctx, method, request)
		}
	})
	tool(server, "prepare_memory", "Find existing memories, entities and topics related to text.", schema(map[string]any{"text": str("The memory or subject to search"), "user": str("User filter; all selects everyone"), "limit": count()}, "text"), true, func(ctx context.Context, input struct {
		Text  string `json:"text"`
		User  string `json:"user"`
		Limit int    `json:"limit"`
	}) (any, error) {
		prepared, err := store.Prepare(ctx, input.Text, input.User, limit(input.Limit, 8, 8))
		return map[string]any{"query": prepared.Query, "user": prepared.User, "matches": prepared.Matches, "ambiguities": prepared.Ambiguities, "possible_duplicates": prepared.PossibleDuplicates, "diagnostics": prepared.Diagnostics, "truncated": prepared.Truncated, "next": prepared.Next}, err
	})
	tool(server, "save_memory", memoryDescription, memorySchema(), false, func(ctx context.Context, input songya.MemoryInput) (any, error) {
		if input.ID == "" && (input.Text == nil || strings.TrimSpace(*input.Text) == "") && strings.TrimSpace(input.Append) == "" {
			return map[string]any{"status": "not_accepted"}, fmt.Errorf("provide the new memory's content in text; to modify an existing memory, supply id and the fields to change")
		}
		input.Key = requestKey(ctx)
		receipt, err := store.SaveMemory(ctx, input)
		return savedWrite(ctx, store, receipt, "memory", err)
	})
	for _, item := range []struct {
		name        string
		kind        songya.Destination
		description string
	}{{"save_entity", songya.Entities, "Save a person, place or thing with a brief identifying description. Save its experiences, preferences and history as linked memories. Reuses a unique name; id selects an existing entity."}, {"save_topic", songya.Topics, "Save a subject by name and description. Reuses a unique matching name; use id to update a specific topic."}} {
		inputSchema := namedSchema(item.kind == songya.Entities)
		tool(server, item.name, item.description, inputSchema, false, func(ctx context.Context, input songya.NamedInput) (any, error) {
			input.Key = requestKey(ctx)
			receipt, err := store.SaveNamed(ctx, item.kind, input)
			return savedWrite(ctx, store, receipt, "record", err)
		})
	}
	tool(server, "save_story", "Save a narrative. Order its related_to links to memories/stories in reading order. Use id to update.", schema(map[string]any{"id": str("Existing story ID; omit to create"), "user": str("Story owner"), "title": str("Story title"), "text": str("Narrative text"), "append": str("Add narrative text"), "metadata": object(), "relations": relationsSchema()}), false, func(ctx context.Context, input songya.StoryInput) (any, error) {
		input.Key = requestKey(ctx)
		receipt, err := store.SaveStory(ctx, input)
		return savedWrite(ctx, store, receipt, "story", err)
	})
	querySchema := schema(map[string]any{"user": str("User filter; all or omitted selects everyone. Entities and topics are shared."), "text": str("Search text; omit to list"), "kind": str("Record directory: memories, entities, topics or stories. Use entities for entity lookup."), "related_to": str("Optional outgoing target ID"), "relation_type": str("Optional relationship type"), "include_retired": map[string]any{"type": "boolean"}, "offset": count(), "limit": count()})
	searchProperties := maps.Clone(querySchema["properties"].(map[string]any))
	searchProperties["mode"] = str("lexical (default) or semantic embeddings for memories and entities")
	searchSchema := schema(searchProperties)

	tool(server, "recall_memory", "Recall a person, place or subject by name or record ID. Returns connected prose with record IDs, event times and story context. Follow next as offset with the same text and user when truncated.", schema(map[string]any{"text": str("Name, subject or record ID to recall"), "user": str("User filter; all or omitted selects everyone"), "offset": count(), "limit": count()}, "text"), true, func(ctx context.Context, input songya.RecallQuery) (any, error) {
		page, err := store.Recall(ctx, input)

		return page, err
	})
	tool(server, "search_memory", "Search names, aliases, descriptions and prose. Use mode=semantic to retrieve memories and entities by meaning. Find entities with kind=entities and a name or alias in text; omit text to list them. Direct file edits are refreshed automatically.", searchSchema, true, func(ctx context.Context, input songya.Query) (any, error) {
		input.Limit = limit(input.Limit, 8, 8)
		return store.Search(ctx, input)
	})
	tool(server, "read_memory", "Read a record’s prose by ID. Use view=source for original metadata and Markdown. Follow next when truncated.", schema(map[string]any{"id": str("Record ID"), "source": str("Relative source path"), "root": str("Configured import root name; omitted for the store"), "key": str("Saved request key for proposed, before or original_text views"), "archive": str("Preserved import archive checksum"), "view": str("body (default) or source; with key use proposed, before or original_text"), "offset": count(), "limit": count()}), true, app.read)
	tool(server, "explore_memory", "Outgoing links, derived backlinks and bounded traversal. Split redirects remain ambiguous instead of selecting one replacement.", schema(map[string]any{"user": str("User filter; all or omitted selects everyone. Entities and topics are shared."), "id": str("Starting ID; omit for paged edges"), "direction": str("incoming, outgoing or both"), "type": str("Optional edge type"), "depth": count(), "offset": count(), "limit": count()}), true, func(ctx context.Context, input songya.GraphQuery) (any, error) {
		input.Limit = limit(input.Limit, 20, 10)
		return store.Graph(ctx, input)
	})
	tool(server, "memory_timeline", "Partial chronology from event dates, bounds, after and caused_by; part_of groups events. Unknown order remains unknown; contradictory constraints are diagnosed.", querySchema, true, func(ctx context.Context, input songya.Query) (any, error) {
		input.Limit = limit(input.Limit, 8, 8)
		return store.Timeline(ctx, input)
	})
	tool(server, "compose_story", "Compose a story’s introduction and ordered related_to memories/stories in bounded byte pages. Missing, recursive and split targets remain explicit.", schema(map[string]any{"id": str("Story ID"), "offset": count(), "limit": count()}, "id"), true, func(ctx context.Context, input pageInput) (any, error) {
		return store.Compose(ctx, input.ID, input.Offset, limit(input.Limit, 12288, 8192))
	})
	type organization struct {
		Action   string             `json:"action"`
		Sources  []string           `json:"sources"`
		Source   string             `json:"source"`
		Kind     songya.Destination `json:"kind"`
		Metadata map[string]any     `json:"metadata"`
		Text     *string            `json:"text"`
		Parts    []struct {
			Kind     songya.Destination `json:"kind"`
			Metadata map[string]any     `json:"metadata"`
			Text     string             `json:"text"`
		} `json:"parts"`
	}
	part := schema(map[string]any{"kind": str("Record kind"), "metadata": object(), "text": str("Part narrative")}, "text")
	tool(server, "organize_memory", "Merge source IDs or split one source ID into parts. Original narratives remain readable.", schema(map[string]any{"action": str("merge or split"), "sources": array(str("Source ID to merge")), "source": str("Source ID to split"), "kind": str("Kind of the merged record"), "metadata": object(), "text": str("Merged narrative"), "parts": array(part)}, "action"), false, func(ctx context.Context, input organization) (any, error) {
		key := requestKey(ctx)
		if key == "" {
			key = "organize_" + rand.Text()
		}
		var receipt songya.Receipt
		var err error
		switch input.Action {
		case "merge":
			sources := []songya.Version{}
			for _, id := range input.Sources {
				sources = append(sources, songya.Version{ID: id})
			}
			receipt, err = store.Merge(ctx, songya.MergeRequest{Key: key, Sources: sources, Kind: input.Kind, Fields: input.Metadata, Body: input.Text})
		case "split":
			parts := []songya.Part{}
			for _, part := range input.Parts {
				parts = append(parts, songya.Part{Kind: part.Kind, Fields: part.Metadata, Body: part.Text})
			}
			receipt, err = store.Split(ctx, songya.SplitRequest{Key: key, Source: songya.Version{ID: input.Source}, Parts: parts})
		default:
			return nil, fmt.Errorf("choose merge or split")
		}
		value := map[string]any{"status": receipt.Status, "operation_id": receipt.Key, "diagnostics": receipt.Diagnostics}
		if input.Action == "merge" {
			value["id"] = receipt.Refs["merged"]
		} else {
			ids := []string{}
			for i := range input.Parts {
				ids = append(ids, receipt.Refs[fmt.Sprintf("part_%d", i+1)])
			}
			value["ids"] = ids
		}
		return value, err
	})
	type reviewedPart struct {
		Start  *int               `json:"start"`
		End    *int               `json:"end"`
		Kind   songya.Destination `json:"kind"`
		Fields map[string]any     `json:"metadata"`
	}
	type importInput struct {
		User   string         `json:"user"`
		Action string         `json:"action"`
		Root   string         `json:"root"`
		Path   string         `json:"path"`
		Parts  []reviewedPart `json:"parts"`
		Offset int            `json:"offset"`
		Limit  int            `json:"limit"`
	}
	importPart := schema(map[string]any{"start": count(), "end": count(), "kind": str("Optional directory"), "metadata": object()}, "start", "end")
	tool(server, "import_memories", "Preview or apply imports from an explicitly configured root. Parts are exact reviewed byte ranges; omitting parts preserves a whole file. Originals, mappings and coverage are retained.", schema(map[string]any{"user": str("Default owner for imported memories/stories; part metadata.user can override it."), "action": str("preview or apply"), "root": str("Configured root name"), "path": str("Relative source file"), "parts": array(importPart), "offset": count(), "limit": count()}, "action", "root"), false, func(ctx context.Context, input importInput) (any, error) {
		root, ok := roots[input.Root]
		if !ok {
			return nil, fmt.Errorf("import root %q is not configured", input.Root)
		}
		switch input.Action {
		case "preview":
			return store.PreviewImports(ctx, root, input.Offset, limit(input.Limit, 8, 8))
		case "apply":
			parts := []songya.ImportPart{}
			for _, part := range input.Parts {
				if part.Start == nil || part.End == nil {
					return nil, fmt.Errorf("each reviewed import part needs start and end byte offsets; omit parts to import the whole source")
				}
				parts = append(parts, songya.ImportPart{Start: *part.Start, End: *part.End, Kind: part.Kind, Fields: part.Fields})
			}
			mapping, err := store.Import(ctx, root, songya.ImportRequest{User: input.User, Path: input.Path, Parts: parts})
			return importPage(mapping, input.Offset, limit(input.Limit, 20, 10)), err
		default:
			return nil, fmt.Errorf("choose preview or apply")
		}
	})
	tool(server, "memory_maintenance", "Inspect diagnostics or a durable request receipt, retry recovery, or rebuild the disposable index. Never retry an accepted write under a new key.", schema(map[string]any{"action": str("status, receipt, recover, abandon, rebuild or embed"), "key": str("Request key for receipt or deliberate abandonment"), "view": str("source returns a byte page of the full receipt"), "offset": count(), "limit": count()}, "action"), false, func(ctx context.Context, input struct {
		Action string `json:"action"`
		View   string `json:"view"`
		Key    string `json:"key"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}) (any, error) {
		switch input.Action {
		case "status":
			return map[string]any{"diagnostics": store.Diagnostics(), "import_roots": rootNames(roots)}, nil
		case "receipt":
			receipt, err := store.BatchStatus(ctx, input.Key)
			if err != nil {
				return receipt, err
			}
			if input.View == "source" {
				data, err := json.Marshal(map[string]any{"operation_id": receipt.Key, "status": receipt.Status, "records": receipt.Records, "diagnostics": receipt.Diagnostics})
				if err != nil {
					return nil, err
				}
				page := bytePage(data, input.Offset, input.Limit)
				return page, nil
			}
			return receiptPage(receipt, input.Offset, limit(input.Limit, 20, 10)), nil
		case "abandon":
			return store.AbandonBatch(ctx, input.Key)
		case "recover":
			err := store.Recover()
			return map[string]any{"diagnostics": store.Diagnostics()}, err
		case "embed":
			return store.IndexEmbeddings(ctx)
		case "rebuild":
			catalog, err := store.Rebuild(ctx)
			return map[string]any{"records": len(catalog.Records), "diagnostics": catalog.Diagnostics}, err
		default:
			return nil, fmt.Errorf("choose status, receipt, recover, abandon, rebuild or embed")
		}
	})
	server.AddResource(&mcp.Resource{URI: "memory://guide", Name: "Memory engine guide", MIMEType: "text/markdown"}, func(_ context.Context, request *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: "memory://guide", MIMEType: "text/markdown", Text: Guide}}}, nil
	})
	server.AddResourceTemplate(&mcp.ResourceTemplate{URITemplate: "memory://records/{id}/{offset}", Name: "Paged canonical record", MIMEType: "application/json"}, func(ctx context.Context, request *mcp.ReadResourceRequest) (output *mcp.ReadResourceResult, err error) {
		defer func() {
			if err != nil {
				err = errors.New(clipError(err.Error()))
			}
		}()
		uri, err := url.Parse(request.Params.URI)
		if err != nil {
			return nil, err
		}
		parts := strings.Split(strings.Trim(uri.Path, "/"), "/")
		if uri.Host != "records" || len(parts) != 2 {
			return nil, fmt.Errorf("use memory://records/id/offset")
		}
		offset, err := strconv.Atoi(parts[1])
		if err != nil {
			return nil, err
		}
		value, err := app.read(ctx, pageInput{ID: parts[0], Offset: offset, View: "source"})
		if err != nil {
			return nil, err
		}
		page := value.(recordPage)
		// The source itself is available here; parsed diagnostics belong to the read
		// tool. Avoid duplicating them inside a second JSON-encoded resource envelope.
		page.Diagnostics = nil
		data, _ := json.Marshal(page)
		canonical := "memory://records/" + parts[0] + "/" + strconv.Itoa(page.Offset)
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: canonical, MIMEType: "application/json", Text: string(data)}}}, nil
	})
	return server
}
func rootNames(roots map[string]string) []string {
	names := []string{}
	for name := range roots {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
func receiptPage(receipt songya.Receipt, offset, size int) any {
	if offset < 0 {
		offset = 0
	}
	if offset > len(receipt.Records) {
		offset = len(receipt.Records)
	}
	size = limit(size, 20, 10)
	end := offset + min(size, len(receipt.Records)-offset)
	value := map[string]any{"status": receipt.Status, "records": receipt.Records[offset:end], "record_count": len(receipt.Records), "truncated": end < len(receipt.Records), "diagnostics": receipt.Diagnostics}
	if len(receipt.Key) <= 512 {
		value["key"] = receipt.Key
	} else {
		value["key_hash"] = memoryRevision([]byte(receipt.Key))
		value["key_truncated"] = true
	}
	if end < len(receipt.Records) {
		value["next"] = end
	}
	return value
}

func importPage(mapping songya.ImportMapping, offset, size int) any {
	if offset < 0 {
		offset = 0
	}
	if offset > len(mapping.Parts) {
		offset = len(mapping.Parts)
	}
	end := offset + min(size, len(mapping.Parts)-offset)
	value := map[string]any{"source": mapping.Source, "archive": mapping.Archive, "batch_key": mapping.BatchKey, "status": mapping.Status, "parts": mapping.Parts[offset:end], "part_count": len(mapping.Parts), "truncated": end < len(mapping.Parts), "coverage_complete": mapping.CoverageComplete, "uncovered": mapping.Uncovered[:min(20, len(mapping.Uncovered))], "uncovered_truncated": len(mapping.Uncovered) > 20, "diagnostics": mapping.Diagnostics}
	if end < len(mapping.Parts) {
		value["next"] = end
	}
	return value
}

func memoryRevision(data []byte) string { return fmt.Sprintf("%x", sha256.Sum256(data)) }
