package songya

import (
	"context"
	"crypto/rand"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Key belongs to the caller's request metadata, never the model's arguments.
type MemoryInput struct {
	Key       string         `json:"-"`
	ID        string         `json:"id,omitempty"`
	User      string         `json:"user,omitempty"`
	Title     string         `json:"title,omitempty"`
	Text      *string        `json:"text,omitempty"`
	Append    string         `json:"append,omitempty"`
	Time      any            `json:"time,omitempty"`
	Entities  []NamedInput   `json:"entities,omitempty"`
	Topics    []NamedInput   `json:"topics,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	Relations []Relation     `json:"relations,omitempty"`
}
type NamedInput struct {
	Key         string         `json:"-"`
	ID          string         `json:"id,omitempty"`
	Name        string         `json:"name,omitempty"`
	Type        string         `json:"type,omitempty"`
	Aliases     []string       `json:"aliases,omitempty"`
	Description *string        `json:"description,omitempty"`
	Append      string         `json:"append,omitempty"`
	Relation    string         `json:"relation,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
	Relations   []Relation     `json:"relations,omitempty"`
}
type StoryInput struct {
	Key       string         `json:"-"`
	ID        string         `json:"id,omitempty"`
	User      string         `json:"user,omitempty"`
	Title     string         `json:"title,omitempty"`
	Text      *string        `json:"text,omitempty"`
	Append    string         `json:"append,omitempty"`
	Members   []string       `json:"members,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	Relations []Relation     `json:"relations,omitempty"`
}

func writeKey(key string) string {
	if key != "" {
		return key
	}
	return "write_" + rand.Text()
}

// SaveMemory translates the caller's concepts into one durable related write.
// Entity lookup and creation run under that write's process-wide store lock.
func (s *Store) SaveMemory(ctx context.Context, input MemoryInput) (Receipt, error) {
	input.Key = writeKey(input.Key)
	if input.ID != "" && kindOf(input.ID) != Memories {
		return Receipt{Key: input.Key, Status: "not_accepted"}, fmt.Errorf("%s is not a memory ID", input.ID)
	}
	fields := copyFields(input.Metadata)
	if input.Title != "" {
		fields["title"] = input.Title
	}
	if input.Time != nil {
		fields["time"] = input.Time
	}
	intent := map[string]any{"metadata": fields, "entities": namedData(input.Entities), "topics": namedData(input.Topics)}
	request := BatchRequest{Key: input.Key, User: input.User, Changes: []Change{{ID: input.ID, Fields: intent, Body: input.Text, Append: input.Append, AddRelations: input.Relations}}}
	if input.Text != nil {
		request.Account = *input.Text
	}
	request.prepare = func(root *os.Root) ([]Change, []StoryAddition, error) {
		memory := Change{Ref: "memory", ID: input.ID, Kind: Memories, Fields: fields, Body: input.Text, Append: input.Append, AddRelations: append([]Relation{}, input.Relations...)}
		changes := []Change{}
		unresolved := []any{}
		for _, group := range []struct {
			kind   Destination
			inputs []NamedInput
		}{{Entities, input.Entities}, {Topics, input.Topics}} {
			for _, item := range group.inputs {
				if err := ctx.Err(); err != nil {
					return nil, nil, err
				}
				target, err := relatedRecord(ctx, root, group.kind, item, &changes)
				if err != nil {
					data := namedData([]NamedInput{item})[0].(map[string]any)
					data["issue"], data["kind"] = err.Error(), string(group.kind)
					unresolved = append(unresolved, data)
					continue
				}
				relation := item.Relation
				if relation == "" {
					relation = string(group.kind)
				}
				memory.AddRelations = append(memory.AddRelations, Relation{Type: relation, Target: target})
			}
		}
		if len(unresolved) > 0 {
			node := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
			if input.ID != "" {
				name, _ := recordName(input.ID)
				if doc, err := readCopy(root, input.ID, name); err == nil {
					node = sequenceNode(metadataField(&doc.Frontmatter, "unresolved_connections"))
				}
			}
			for _, item := range unresolved {
				encoded, _ := encodeValue(item)
				node.Content = append(node.Content, encoded)
			}
			memory.Fields["unresolved_connections"] = node
		}
		return append([]Change{memory}, changes...), nil, nil
	}
	return s.ApplyBatch(ctx, request)
}

func namedData(inputs []NamedInput) []any {
	result := []any{}
	for _, v := range inputs {
		item := map[string]any{"id": v.ID, "name": v.Name, "type": v.Type, "aliases": v.Aliases, "relation": v.Relation, "metadata": v.Metadata, "append": v.Append, "relations": v.Relations}
		if v.Description != nil {
			item["description"] = *v.Description
		}
		result = append(result, item)
	}
	return result
}
func sameName(a, b string) bool {
	return strings.EqualFold(strings.Join(strings.Fields(a), " "), strings.Join(strings.Fields(b), " "))
}

func findNamed(ctx context.Context, root *os.Root, kind Destination, input NamedInput) (*Document, error) {
	if input.ID != "" {
		name, err := recordName(input.ID)
		if err != nil {
			return nil, err
		}
		if kindOf(input.ID) != kind {
			return nil, fmt.Errorf("%s is not in %s", input.ID, kind)
		}
		doc, err := readCopy(root, input.ID, name)
		if err == nil && (doc.Pending || doc.Unconfirmed) {
			return nil, fmt.Errorf("%s needs recovery before reuse", input.ID)
		}
		return doc, err
	}
	entries, err := fs.ReadDir(root.FS(), string(kind))
	if err != nil {
		return nil, err
	}
	var match *Document
	var unavailable error
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		id := strings.TrimSuffix(entry.Name(), ".md")
		if kindOf(id) != kind || !ValidID(id) {
			continue
		}
		doc, err := readCopy(root, id, string(kind)+"/"+entry.Name())
		if err != nil {
			unavailable = fmt.Errorf("cannot check %s: %w", id, err)
			continue
		}
		record := recordFromDocument("", doc, kind)
		names := append([]string{record.Title}, record.Aliases...)
		for _, name := range names {
			if !sameName(name, input.Name) {
				continue
			}
			storedType := strings.Join(stringsFromNode(metadataField(&doc.Frontmatter, "type")), "")
			if input.Type != "" && storedType != "" && !sameName(input.Type, storedType) {
				continue
			}
			if doc.Pending || doc.Unconfirmed {
				return nil, fmt.Errorf("%s needs recovery before reuse", id)
			}
			if match != nil && match.ID != doc.ID {
				return nil, fmt.Errorf("several %s named %q; choose an ID", kind, input.Name)
			}
			match = doc
			break
		}
	}
	if match == nil && unavailable != nil {
		return nil, unavailable
	}
	return match, nil
}

func namedChange(kind Destination, input NamedInput) Change {
	fields := copyFields(input.Metadata)
	if input.Name != "" {
		fields["name"] = input.Name
	}
	if input.Type != "" {
		fields["type"] = input.Type
	}
	if input.Aliases != nil {
		fields["aliases"] = input.Aliases
	}
	return Change{ID: input.ID, Kind: kind, Fields: fields, Body: input.Description, Append: input.Append, AddRelations: input.Relations}
}
func relatedRecord(ctx context.Context, root *os.Root, kind Destination, input NamedInput, changes *[]Change) (string, error) {
	// Repeated participants in one capture share one record.
	for i := range *changes {
		change := &(*changes)[i]
		matches := input.ID != "" && change.ID == input.ID
		if input.ID == "" && input.Name != "" {
			node, _ := encodeValue(change.Fields["aliases"])
			for _, name := range append([]string{fieldText(change.Fields["name"])}, stringsFromNode(node)...) {
				matches = matches || sameName(name, input.Name)
			}
			storedType := fieldText(change.Fields["type"])
			matches = matches && (input.Type == "" || storedType == "" || sameName(storedType, input.Type))
		}
		if change.Kind == kind && matches {
			mergeParticipant(root, change, input)
			if change.ID != "" {
				return change.ID, nil
			}
			return "$" + change.Ref, nil
		}
	}
	doc, err := findNamed(ctx, root, kind, input)
	if err != nil {
		return "", err
	}
	change := namedChange(kind, input)
	change.Ref = fmt.Sprintf("related_%d", len(*changes))
	if doc != nil {
		if !needsEnrichment(doc, input) {
			return doc.ID, nil
		}
		change.ID = doc.ID
		// Capture adds knowledge; standalone save_entity/save_topic replace a supplied description.
		change.Fields["name"] = doc.Record(kind).Title
		for i := range *changes {
			previous := &(*changes)[i]
			if previous.ID == doc.ID {
				mergeParticipant(root, previous, input)
				return doc.ID, nil
			}
		}
		if input.Description != nil && !strings.Contains(doc.Body, *input.Description) {
			change.Append = *input.Description + "\n" + input.Append
		}
		change.Body = nil
		aliases := input.Aliases
		if input.Name != "" && !sameName(input.Name, doc.Record(kind).Title) {
			aliases = append(append([]string{}, aliases...), input.Name)
		}
		if aliases != nil {
			change.Fields["aliases"] = appendAliases(metadataField(&doc.Frontmatter, "aliases"), aliases)
		}
	}
	*changes = append(*changes, change)
	if change.ID != "" {
		return change.ID, nil
	}
	return "$" + change.Ref, nil
}

func (s *Store) SaveNamed(ctx context.Context, kind Destination, input NamedInput) (Receipt, error) {
	input.Key = writeKey(input.Key)
	request := BatchRequest{Key: input.Key, Changes: []Change{{Kind: kind, Fields: map[string]any{"input": namedData([]NamedInput{input})}}}}
	request.prepare = func(root *os.Root) ([]Change, []StoryAddition, error) {
		doc, err := findNamed(ctx, root, kind, input)
		if err != nil {
			return nil, nil, err
		}
		change := namedChange(kind, input)
		change.Ref = "record"
		if doc != nil {
			change.ID = doc.ID
			if input.ID == "" {
				change.Fields["name"] = doc.Record(kind).Title
			}
		}
		return []Change{change}, nil, nil
	}
	return s.ApplyBatch(ctx, request)
}
func (s *Store) SaveStory(ctx context.Context, input StoryInput) (Receipt, error) {
	if input.ID != "" && kindOf(input.ID) != Stories {
		return Receipt{Key: input.Key, Status: "not_accepted"}, fmt.Errorf("%s is not a story ID", input.ID)
	}
	fields := copyFields(input.Metadata)
	if input.Title != "" {
		fields["title"] = input.Title
	}
	intent := map[string]any{"metadata": fields, "members": input.Members}
	request := BatchRequest{Key: writeKey(input.Key), User: input.User, Changes: []Change{{ID: input.ID, Kind: Stories, Fields: intent, Body: input.Text, Append: input.Append, AddRelations: input.Relations}}}
	request.prepare = func(root *os.Root) ([]Change, []StoryAddition, error) {
		change := Change{Ref: "story", ID: input.ID, Kind: Stories, Fields: fields, Body: input.Text, Append: input.Append, AddRelations: input.Relations}
		members := input.Members
		body := ""
		if input.ID != "" {
			name, err := recordName(input.ID)
			if err != nil {
				return nil, nil, err
			}
			doc, err := readCopy(root, input.ID, name)
			if err != nil {
				return nil, nil, err
			}
			body = doc.Body
			if input.Members == nil && input.Text != nil {
				for _, match := range storyReference.FindAllStringSubmatch(body, -1) {
					members = append(members, match[1])
				}
			}
		}
		if input.Members != nil {
			if input.Text != nil {
				body = *input.Text
			}
			body = strings.TrimSpace(storyReference.ReplaceAllString(body, ""))
			change.Body = &body
		}
		target := input.ID
		if target == "" {
			target = "$story"
		}
		return []Change{change}, []StoryAddition{{Story: target, Members: members}}, nil
	}
	return s.ApplyBatch(ctx, request)
}

func appendAliases(existing *yaml.Node, aliases []string) *yaml.Node {
	node := sequenceNode(existing)
	names := stringsFromNode(existing)
	for _, alias := range aliases {
		found := false
		for _, name := range names {
			found = found || name == alias
		}
		if !found {
			node.Content = append(node.Content, yamlValue(alias))
			names = append(names, alias)
		}
	}
	return node
}

func fieldText(value any) string {
	if value == nil {
		return ""
	}
	return fmt.Sprint(value)
}
func mergeParticipant(root *os.Root, change *Change, input NamedInput) {
	present := change.Append
	if change.Body != nil {
		present += *change.Body
	}
	if change.ID != "" {
		name, _ := recordName(change.ID)
		if doc, err := readCopy(root, change.ID, name); err == nil {
			present += doc.Body
		}
	}
	additions := []string{input.Append}
	if input.Description != nil {
		additions = append(additions, *input.Description)
	}
	for _, addition := range additions {
		if strings.TrimSpace(addition) != "" && !strings.Contains(present, addition) {
			change.Append += "\n" + addition
			present += "\n" + addition
		}
	}
	for key, value := range input.Metadata {
		change.Fields[key] = value
	}
	if input.Type != "" {
		change.Fields["type"] = input.Type
	}
	change.AddRelations = append(change.AddRelations, input.Relations...)
	if input.Aliases != nil {
		node, _ := encodeValue(change.Fields["aliases"])
		change.Fields["aliases"] = appendAliases(node, input.Aliases)
	}
}

func needsEnrichment(doc *Document, input NamedInput) bool {
	if len(input.Metadata) > 0 || len(input.Relations) > 0 {
		return true
	}
	if input.Type != "" && !sameName(input.Type, strings.Join(stringsFromNode(metadataField(&doc.Frontmatter, "type")), "")) {
		return true
	}
	if input.Description != nil && !strings.Contains(doc.Body, *input.Description) {
		return true
	}
	if input.Append != "" && !strings.Contains(doc.Body, input.Append) {
		return true
	}
	existing := nodeStrings("aliases", doc)
	if input.Name != "" {
		found := sameName(input.Name, doc.Record(kindOf(doc.ID)).Title)
		for _, alias := range existing {
			found = found || sameName(input.Name, alias)
		}
		if !found {
			return true
		}
	}
	for _, alias := range input.Aliases {
		found := false
		for _, name := range existing {
			found = found || name == alias
		}
		if !found {
			return true
		}
	}
	return false
}
