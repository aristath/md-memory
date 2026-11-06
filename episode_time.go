package songya

import (
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"
)

// EpisodeTime describes the event, independently of automatic record timestamps.
type EpisodeTime struct {
	Date     *string `json:"date" yaml:"date"`
	Earliest *string `json:"earliest" yaml:"earliest"`
	Latest   *string `json:"latest" yaml:"latest"`
}

// A supplied component patches the existing event time; omission preserves it.
// Malformed values follow the ordinary retention path instead of losing data.
func patchEventTime(previous *yaml.Node, value any) any {
	patch, _ := encodeValue(value)
	if patch.Kind != yaml.MappingNode || len(duplicateFields(patch)) > 0 {
		return value
	}
	base, issue := eventTimeNode(previous)
	if issue != "" {
		// Keep unreadable prior information beside the repaired components.
		base = yamlValue(map[string]any{"unparsed_previous_time": previous})
	}
	doc := &Document{Frontmatter: *base}
	for i := 0; i+1 < len(patch.Content); i += 2 {
		name, ok := fieldName(patch.Content[i])
		if !ok {
			return value
		}
		_ = doc.Set(name, patch.Content[i+1])
	}
	return &doc.Frontmatter
}

func dateValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func episodeTime(node *yaml.Node) EpisodeTime {
	if node != nil && len(duplicateFields(dereference(node))) > 0 {
		return EpisodeTime{}
	}
	read := func(name string) *string {
		entry := dereference(metadataField(node, name))
		if entry == nil || entry.Kind != yaml.ScalarNode || entry.ShortTag() == "!!null" {
			return nil
		}
		value := strings.TrimSpace(entry.Value)
		if value == "" {
			return nil
		}
		return &value
	}
	return EpisodeTime{Date: read("date"), Earliest: read("earliest"), Latest: read("latest")}
}

func (t EpisodeTime) bounds() (string, string) {
	if t.Date != nil {
		if t.Earliest != nil && *t.Earliest != *t.Date || t.Latest != nil && *t.Latest != *t.Date {
			return "", ""
		}
		return *t.Date, *t.Date
	}
	return dateValue(t.Earliest), dateValue(t.Latest)
}

// Unknown fields remain intact for repair rather than losing the account's data.
func eventTimeNode(node *yaml.Node) (*yaml.Node, string) {
	node = dereference(node)
	if node == nil || node.ShortTag() == "!!null" {
		return yamlValue(map[string]any{"date": nil, "earliest": nil, "latest": nil}), ""
	}
	if node.Kind == yaml.ScalarNode {
		if _, ok := parseBound(node.Value, false); ok && (node.ShortTag() == "!!str" || node.ShortTag() == "!!timestamp") {
			return yamlValue(map[string]any{"date": node.Value, "earliest": nil, "latest": nil}), ""
		}
		return node, "event time needs date, earliest and latest; original value retained"
	}
	if node.Kind != yaml.MappingNode {
		return node, "event time needs a date/range object; original value retained"
	}
	if len(duplicateFields(node)) > 0 {
		return node, "duplicate event time fields retained for repair"
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key, _ := fieldName(node.Content[i])
		if key != "date" && key != "earliest" && key != "latest" {
			return node, fmt.Sprintf("unrecognized event time field %q retained for repair", key)
		}
		value := dereference(node.Content[i+1])
		if value != nil && value.Kind != yaml.ScalarNode {
			return node, fmt.Sprintf("unreadable event time %s retained for repair", key)
		}
	}
	t := episodeTime(node)
	if t.Date != nil {
		if t.Earliest != nil && *t.Earliest != *t.Date || t.Latest != nil && *t.Latest != *t.Date {
			return node, "exact event date conflicts with its range; original values retained"
		}
		t.Earliest, t.Latest = nil, nil
	}
	// Keep field comments and annotations while filling the canonical three keys.
	result, _ := normalizeNode(node)
	values := map[string]*string{"date": t.Date, "earliest": t.Earliest, "latest": t.Latest}
	for _, key := range []string{"date", "earliest", "latest"} {
		value := values[key]
		previous := field(result, key)
		replacement := yamlValue(nil)
		if value != nil {
			replacement = yamlValue(*value)
		}
		if previous == nil {
			result.Content = append(result.Content, yamlValue(key), replacement)
		} else if value == nil && previous.ShortTag() != "!!null" || value != nil && previous.Value != *value {
			mergeComments(replacement, previous)
			replacement.Anchor = previous.Anchor
			assignNode(previous, replacement)
		}
	}
	return result, ""
}
