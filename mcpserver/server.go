// Package mcpserver exposes the standalone memory engine over MCP stdio.
package mcpserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/aristath/songya"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const Guide = `Songya keeps connected Markdown records.

Kinds: memories describe remembered events or facts; entities describe people, places or things; topics group a subject; stories arrange memories into a narrative. Entities and topics are shared. Memories and stories belong to a user. IDs and creation/update times are automatic.

Capture: prepare_memory(original_text) searches the user's words for existing records and returns a key. Reuse matching entities/topics; create missing ones with the memory in one save_memory call. Pass the same original_text to preserve it. Query results show the searched text; an empty result only describes that search.

For new records, ref assigns a local label. Use $label as a relation target within the same save. Result refs maps those labels to saved IDs. Existing records use id; append adds knowledge, body replaces prose, add_relations adds links. Metadata goes in fields: title for memories/stories, name and optional aliases/type for entities/topics. Type is free-form, e.g. person or place.

Example: after preparing "Around June–July 2025 I had dinner with John at Pizza AMI", use the returned key:
save_memory({"key":"RETURNED_KEY","user":"ari","original_text":"Around June–July 2025 I had dinner with John at Pizza AMI","changes":[{"ref":"john","kind":"entities","fields":{"name":"John","type":"person"},"body":"My friend John."},{"ref":"pizza","kind":"entities","fields":{"name":"Pizza AMI","type":"place"},"body":"A restaurant."},{"ref":"dinner","kind":"memories","fields":{"title":"Dinner with John","time":{"description":"Around June–July 2025","earliest":"2025-06-01","latest":"2025-07-31"}},"body":"I had dinner with John at Pizza AMI.","add_relations":[{"type":"with","target":"$john"},{"type":"at","target":"$pizza"}]}]})

Time describes when the event happened. Use description for approximate wording, earliest/latest for known bounds; an exact day uses equal bounds. Preserve uncertainty.

Search or list any kind with search_memory; omit text to list. read_memory(id) returns metadata and prose; explore_memory(id) shows connections. Stories use ordered members via save_memory.stories. user selects an owner; all queries every user.

Retry a save with the same key and arguments. Preparation creates a fresh independent key. A committed result confirms saving; follow next on paged results. Tool results are in structuredContent.`

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
	if key == "fields" {
		return value
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
	case "user", "key", "original_text", "id", "kind", "ref", "body", "append", "story", "text", "direction", "type", "related_to", "action", "root", "path", "source", "view", "archive", "relation_type", "target":
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
	return map[string]any{"type": "object", "additionalProperties": true, "description": "Open metadata. IDs and creation/update times are automatic.", "properties": map[string]any{
		"title": str("Memory or story title"), "name": str("Entity or topic name"), "aliases": array(str("Alternative name")), "type": str("Free-form entity type, e.g. person or place"),
		"time": schema(map[string]any{"description": str("When the event happened, in ordinary words"), "earliest": str("Earliest known date, YYYY-MM-DD"), "latest": str("Latest known date, YYYY-MM-DD; equal to earliest for an exact day")}),
	}}
}
func array(item any) map[string]any { return map[string]any{"type": "array", "items": item} }

func tool[T any](server *mcp.Server, name, description string, input any, readOnly bool, handler func(context.Context, T) (any, error)) {
	closed := false
	server.AddTool(&mcp.Tool{Name: name, Description: description, InputSchema: input, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: readOnly, IdempotentHint: !readOnly, OpenWorldHint: &closed}}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var value T
		if err := decode(request.Params.Arguments, &value); err != nil {
			return result(nil, fmt.Errorf("cannot interpret arguments: %w", err)), nil
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
	if len(payload) > 28*1024 {
		bounded = map[string]any{"status": envelope["status"], "data": map[string]any{"truncated": true, "message": "Result exceeds the response budget. Use a smaller limit or read the receipt with view=source. The durable outcome is unchanged."}}
		if receipt, ok := value.(map[string]any); ok {
			data := bounded["data"].(map[string]any)
			for _, key := range []string{"status", "key", "key_hash", "key_truncated", "record_count", "batch_key"} {
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
	size = limit(size, 4096, 2048)
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
		if input.View == "source" || input.View == "" {
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
	tool(server, "prepare_memory", "Find existing memories, entities and topics mentioned in the original text. Returns candidates and a key for saving.", schema(map[string]any{"original_text": str("The user's original words describing what to remember"), "user": str("User filter; all or omitted selects everyone. Entities and topics are shared."), "limit": count()}, "original_text"), true, func(ctx context.Context, input struct {
		OriginalText string `json:"original_text"`
		User         string `json:"user"`
		Limit        int    `json:"limit"`
	}) (any, error) {
		return store.Prepare(ctx, input.OriginalText, input.User, limit(input.Limit, 8, 8))
	})
	edge := schema(map[string]any{"type": str("Open relationship name"), "target": str("Existing record ID, or $label for a record created in this save")}, "type", "target")
	change := schema(map[string]any{"ref": str("Label for this new record, e.g. john; reference it as $john within this save"), "id": str("Existing record ID; omit to create"), "kind": str("memories, entities, topics or stories; default memories"), "fields": object(), "body": str("Markdown narrative or entity description; replaces existing prose when supplied"), "append": str("Append detail without replacing existing narrative"), "add_relations": array(edge)})
	story := schema(map[string]any{"story": str("Story ID or $local_ref"), "members": array(str("Record ID or $local_ref"))}, "story", "members")
	tool(server, "save_memory", "Save memories with their entities, topics and links together. New records use ref labels; updates use IDs. Put prose in body, metadata in fields, links in add_relations. Return refs maps labels to IDs. Retry with the same key and arguments.", schema(map[string]any{"user": str("Default owner for new memories/stories; fields.user can specify a different user. all is only a query selector."), "key": str("Stable request key from preparation"), "original_text": str("The user's original words to preserve verbatim"), "changes": array(change), "stories": array(story)}, "key", "changes"), false, func(ctx context.Context, input songya.BatchRequest) (any, error) { return store.ApplyBatch(ctx, input) })
	querySchema := schema(map[string]any{"user": str("User filter; all or omitted selects everyone. Entities and topics are shared."), "text": str("Lexical search; omit to list"), "kind": str("Record directory: memories, entities, topics or stories. Use entities for entity lookup."), "related_to": str("Optional outgoing target ID"), "relation_type": str("Optional relationship type"), "include_retired": map[string]any{"type": "boolean"}, "offset": count(), "limit": count()})
	tool(server, "search_memory", "Bounded lexical retrieval across names, aliases, descriptions, prose and episode time. Find entities with kind=entities and a name or alias in text; omit text to list them. Direct file edits are refreshed automatically.", querySchema, true, func(ctx context.Context, input songya.Query) (any, error) {
		input.Limit = limit(input.Limit, 8, 8)
		return store.Search(ctx, input)
	})
	tool(server, "read_memory", "Read a record's metadata and Markdown by ID. Use view=body for prose only. Follow next when truncated.", schema(map[string]any{"id": str("Record ID"), "source": str("Relative source path"), "root": str("Configured import root name; omitted for the store"), "key": str("Saved request key for proposed, before or original_text views"), "archive": str("Preserved import archive checksum"), "view": str("source (default) or body; with key use proposed, before or original_text"), "offset": count(), "limit": count()}), true, app.read)
	tool(server, "explore_memory", "Outgoing links, derived backlinks and bounded traversal. Split redirects remain ambiguous instead of selecting one replacement.", schema(map[string]any{"user": str("User filter; all or omitted selects everyone. Entities and topics are shared."), "id": str("Starting ID; omit for paged edges"), "direction": str("incoming, outgoing or both"), "type": str("Optional edge type"), "depth": count(), "offset": count(), "limit": count()}), true, func(ctx context.Context, input songya.GraphQuery) (any, error) {
		input.Limit = limit(input.Limit, 20, 10)
		return store.Graph(ctx, input)
	})
	tool(server, "memory_timeline", "Partial chronology from stated bounds and before/after relations. Unknown order remains unknown; contradictory constraints are diagnosed.", querySchema, true, func(ctx context.Context, input songya.Query) (any, error) {
		input.Limit = limit(input.Limit, 8, 8)
		return store.Timeline(ctx, input)
	})
	tool(server, "compose_story", "Expand canonical [[record_id]] references in narrative order, in bounded byte pages. Missing, recursive and split references remain explicit.", schema(map[string]any{"id": str("Story ID"), "offset": count(), "limit": count()}, "id"), true, func(ctx context.Context, input pageInput) (any, error) {
		return store.Compose(ctx, input.ID, input.Offset, limit(input.Limit, 4096, 2048))
	})
	type organization struct {
		Action  string             `json:"action"`
		Key     string             `json:"key"`
		Sources []songya.Version   `json:"sources"`
		Source  songya.Version     `json:"source"`
		Kind    songya.Destination `json:"kind"`
		Fields  map[string]any     `json:"fields"`
		Body    *string            `json:"body"`
		Parts   []songya.Part      `json:"parts"`
	}
	version := schema(map[string]any{"id": str("Source record ID")}, "id")
	part := schema(map[string]any{"kind": str("Optional directory"), "fields": object(), "body": str("Reviewed part narrative")}, "body")
	tool(server, "organize_memory", "Merge source records or split one into parts. Original narratives remain readable; retry with the same key and arguments.", schema(map[string]any{"action": str("merge or split"), "key": str("Stable operation key"), "sources": array(version), "source": version, "kind": str("Optional canonical directory"), "fields": object(), "body": str("Optional reviewed merge narrative"), "parts": array(part)}, "action", "key"), false, func(ctx context.Context, input organization) (any, error) {
		switch input.Action {
		case "merge":
			return store.Merge(ctx, songya.MergeRequest{Key: input.Key, Sources: input.Sources, Kind: input.Kind, Fields: input.Fields, Body: input.Body})
		case "split":
			return store.Split(ctx, songya.SplitRequest{Key: input.Key, Source: input.Source, Parts: input.Parts})
		default:
			return nil, fmt.Errorf("choose merge or split")
		}
	})
	type reviewedPart struct {
		Start  *int               `json:"start"`
		End    *int               `json:"end"`
		Kind   songya.Destination `json:"kind"`
		Fields map[string]any     `json:"fields"`
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
	importPart := schema(map[string]any{"start": count(), "end": count(), "kind": str("Optional directory"), "fields": object()}, "start", "end")
	tool(server, "import_memories", "Preview or apply imports from an explicitly configured root. Parts are exact reviewed byte ranges; omitting parts preserves a whole file. Originals, mappings and coverage are retained.", schema(map[string]any{"user": str("Default owner for imported memories/stories; part fields.user can override it."), "action": str("preview or apply"), "root": str("Configured root name"), "path": str("Relative source file"), "parts": array(importPart), "offset": count(), "limit": count()}, "action", "root"), false, func(ctx context.Context, input importInput) (any, error) {
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
	tool(server, "memory_maintenance", "Inspect diagnostics or a durable request receipt, retry recovery, or rebuild the disposable index. Never retry an accepted write under a new key.", schema(map[string]any{"action": str("status, receipt, recover, abandon or rebuild"), "key": str("Request key for receipt or deliberate abandonment"), "view": str("source returns a byte page of the full receipt"), "offset": count(), "limit": count()}, "action"), false, func(ctx context.Context, input struct {
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
				data, err := json.Marshal(receipt)
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
		case "rebuild":
			catalog, err := store.Rebuild(ctx)
			return map[string]any{"records": len(catalog.Records), "diagnostics": catalog.Diagnostics}, err
		default:
			return nil, fmt.Errorf("choose status, receipt, recover, abandon or rebuild")
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
	refs := map[string]string{}
	included := map[string]bool{}
	for _, record := range receipt.Records[offset:end] {
		included[record.ID] = true
	}
	refsTruncated := false
	for ref, id := range receipt.Refs {
		if included[id] && len(ref) <= 512 {
			refs[ref] = id
		} else {
			refsTruncated = true
		}
	}
	value := map[string]any{"status": receipt.Status, "records": receipt.Records[offset:end], "record_count": len(receipt.Records), "refs": refs, "refs_truncated": refsTruncated, "truncated": end < len(receipt.Records), "diagnostics": receipt.Diagnostics}
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
