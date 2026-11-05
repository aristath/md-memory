package songya

import (
	"context"
	"fmt"
	"os"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Targets are record IDs, entity names, or a named entity/topic to create or reuse.
// Persisted relations always contain the engine-assigned target ID.
type RelationInput struct {
	Type   string `json:"type" yaml:"type"`
	Target any    `json:"target" yaml:"target"`
}

func relationData(inputs []RelationInput) []any {
	if inputs == nil {
		return nil
	}
	data := []any{}
	for _, input := range inputs {
		data = append(data, map[string]any{"type": input.Type, "target": input.Target})
	}
	return data
}

func connectionTarget(ctx context.Context, root *os.Root, target any, changes *[]Change) (string, error) {
	kind := Entities
	var named NamedInput
	switch value := target.(type) {
	case string:
		value = strings.TrimSpace(value)
		if ValidID(value) {
			return value, nil
		}
		if kindOf(value) != "" || strings.TrimSpace(value) == "" {
			return "", fmt.Errorf("target %q needs a record ID or a name", value)
		}
		named.Name = value
	case map[string]any:
		if value["kind"] != nil {
			kind = Destination(strings.ToLower(strings.TrimSpace(fieldText(value["kind"]))))
			if kind == "entity" {
				kind = Entities
			}
			if kind == "topic" {
				kind = Topics
			}
		}
		if kind != Entities && kind != Topics {
			return "", fmt.Errorf("new named targets use kind entities or topics")
		}
		node, _ := encodeValue(value)
		if err := node.Decode(&named); err != nil {
			return "", err
		}
	default:
		return "", fmt.Errorf("provide a target ID, entity name, or named entity/topic")
	}
	if named.ID == "" && strings.TrimSpace(named.Name) == "" {
		return "", fmt.Errorf("a named target needs its name")
	}
	return relatedRecord(ctx, root, kind, named, changes)
}

// Resolve under the batch lock. One unreadable identity or incomplete connection
// is retained for repair while the memory and its healthy connections still save.
func resolveConnections(ctx context.Context, root *os.Root, changes *[]Change) error {
	for i := 0; i < len(*changes); i++ {
		inputs, supplied := (*changes)[i].Fields["relations"].([]RelationInput)
		if !supplied {
			continue
		}
		var existing *Document
		if id := (*changes)[i].ID; id != "" {
			name, err := recordName(id)
			if err == nil {
				existing, _ = readCopy(root, id, name)
			}
		}
		if existing != nil && ((*changes)[i].Append != "" || (*changes)[i].enrich) && len(inputs) > 0 {
			previous := []RelationInput{}
			for _, relation := range existing.Metadata().Relations {
				previous = append(previous, RelationInput{Type: relation.Type, Target: relation.Target})
			}
			inputs = append(previous, inputs...)
		}
		connections := []Relation{}
		var unresolved []any
		for _, input := range inputs {
			if err := ctx.Err(); err != nil {
				return err
			}
			kind := relationType(input.Type)
			if strings.TrimSpace(input.Type) == "" {
				kind = "related_to"
			}
			var target string
			var err error
			if kind == "" {
				err = fmt.Errorf("choose after, caused_by, part_of, related_to or possibly_same_as")
			} else {
				target, err = connectionTarget(ctx, root, input.Target, changes)
			}
			if err != nil {
				unresolved = append(unresolved, map[string]any{"type": input.Type, "target": input.Target, "issue": err.Error()})
				continue
			}
			connection := Relation{Type: kind, Target: target}
			found := false
			for _, previous := range connections {
				found = found || previous == connection
			}
			if !found {
				connections = append(connections, connection)
			}
		}
		(*changes)[i].Fields["relations"] = connections
		if len(unresolved) > 0 || existing != nil && metadataField(&existing.Frontmatter, "unresolved_connections") != nil {
			node := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
			if existing != nil && ((*changes)[i].Append != "" || (*changes)[i].enrich) && len(inputs) > 0 {
				node = sequenceNode(metadataField(&existing.Frontmatter, "unresolved_connections"))
			}
			for _, value := range unresolved {
				encoded, _ := encodeValue(value)
				node.Content = append(node.Content, encoded)
			}
			(*changes)[i].Fields["unresolved_connections"] = node
		}
	}
	return nil
}
