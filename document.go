// Package memory stores connected Markdown records.
package memory

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

type Relation struct {
	Type   string `yaml:"type"`
	Target string `yaml:"target"`
}

type Metadata struct {
	Title     string     `yaml:"title"`
	CreatedAt time.Time  `yaml:"created_at"`
	UpdatedAt time.Time  `yaml:"updated_at"`
	Relations []Relation `yaml:"relations,omitempty"`
}

// Document keeps the complete YAML tree and Markdown body, including damaged
// fields. Diagnostics reports problems without making the record unavailable.
type Document struct {
	ID          string `yaml:"-"`
	Pending     bool   `yaml:"-"`
	Frontmatter yaml.Node
	Body        string
	warnings    []string
}

func NewDocument(metadata Metadata, body string) (*Document, error) {
	metadata.CreatedAt = metadata.CreatedAt.UTC()
	metadata.UpdatedAt = metadata.UpdatedAt.UTC()
	d := &Document{Body: body}
	err := d.Frontmatter.Encode(metadata)
	return d, err
}

// Parse always retains the content. Unreadable YAML is kept verbatim in an
// unparsed_frontmatter field; when boundaries are unknown, the full source is body.
func Parse(data []byte) *Document {
	d := &Document{Frontmatter: yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}, Body: string(data)}
	content := bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	line, rest, _ := bytes.Cut(content, []byte("\n"))
	if !delimiter(line) {
		d.warnings = append(d.warnings, "No frontmatter; original content retained as body")
		return d
	}
	var header bytes.Buffer
	for len(rest) > 0 {
		line, remaining, newline := bytes.Cut(rest, []byte("\n"))
		if delimiter(line) {
			d.Body = string(remaining)
			var node yaml.Node
			decoder := yaml.NewDecoder(bytes.NewReader(header.Bytes()))
			err := decoder.Decode(&node)
			decoded := err == nil
			if err == io.EOF {
				err = nil
			}
			if err == nil {
				var extra yaml.Node
				if trailing := decoder.Decode(&extra); trailing != io.EOF {
					err = fmt.Errorf("content after the first YAML document is retained")
				}
			}
			if node.Kind == yaml.DocumentNode && len(node.Content) == 1 {
				node = *node.Content[0]
			}
			if decoded && node.Kind == yaml.MappingNode {
				d.Frontmatter = node
			}
			if err == nil && node.Kind == 0 {
				return d // Empty frontmatter is an empty mapping.
			}
			if err == nil && node.Kind == yaml.MappingNode {
				d.Frontmatter = node
			} else {
				_ = d.Set("unparsed_frontmatter", header.String())
				message := "Frontmatter is not a mapping; original retained"
				if err != nil {
					message = "Original frontmatter retained: " + err.Error()
				}
				d.warnings = append(d.warnings, message)
			}
			return d
		}
		header.Write(line)
		if newline {
			header.WriteByte('\n')
		}
		rest = remaining
	}
	d.warnings = append(d.warnings, "Unclosed frontmatter; entire original retained as body")
	return d
}

func delimiter(line []byte) bool {
	return bytes.Equal(bytes.TrimRight(line, " \t\r"), []byte("---"))
}

func field(node *yaml.Node, name string) *yaml.Node {
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == name {
			return node.Content[i+1]
		}
	}
	return nil
}

func (d *Document) metadata() (m Metadata, warnings []string) {
	decode := func(name string, value any) {
		if node := field(&d.Frontmatter, name); node != nil {
			err := node.Decode(value)
			if timestamp, ok := value.(*time.Time); ok && err != nil {
				for _, layout := range []string{time.RFC3339Nano, time.DateTime, time.DateOnly} {
					if parsed, parseErr := time.Parse(layout, node.Value); parseErr == nil {
						*timestamp, err = parsed.UTC(), nil
						break
					}
				}
			}
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("%s: %v", name, err))
			}
		}
	}
	decode("title", &m.Title)
	decode("created_at", &m.CreatedAt)
	decode("updated_at", &m.UpdatedAt)
	if m.CreatedAt.IsZero() {
		warnings = append(warnings, "created_at is unknown")
	}
	if m.UpdatedAt.IsZero() {
		warnings = append(warnings, "updated_at is unknown")
	}
	seen := map[string]bool{}
	for i := 0; i+1 < len(d.Frontmatter.Content); i += 2 {
		name := d.Frontmatter.Content[i].Value
		if seen[name] {
			warnings = append(warnings, "Duplicate field "+name+" retained; the metadata view uses its first value")
		}
		seen[name] = true
	}
	if node := field(&d.Frontmatter, "relations"); node != nil {
		if node.Kind == yaml.AliasNode {
			node = node.Alias
		}
		nodes := node.Content
		if node.Kind != yaml.SequenceNode {
			nodes = []*yaml.Node{node}
		}
		for i, entry := range nodes {
			var relation Relation
			if err := entry.Decode(&relation); err != nil {
				warnings = append(warnings, fmt.Sprintf("relation %d: %v", i, err))
			}
			relation.Type = strings.TrimSpace(relation.Type)
			relation.Target = strings.TrimSpace(relation.Target)
			if relation.Type == "" || !ValidID(relation.Target) {
				warnings = append(warnings, fmt.Sprintf("relation %d is unresolved", i))
			}
			if relation.Type != "" || relation.Target != "" {
				m.Relations = append(m.Relations, relation)
			}
		}
	}
	return m, warnings
}

// Metadata reads independent fields; one broken value cannot hide the others.
func (d *Document) Metadata() Metadata {
	metadata, _ := d.metadata()
	return metadata
}

func (d *Document) Diagnostics() []string {
	_, warnings := d.metadata()
	warnings = append(warnings, valueIssues(&d.Frontmatter, "metadata")...)
	if field(&d.Frontmatter, "unparsed_frontmatter") != nil {
		warnings = append(warnings, "Original unparsed frontmatter is retained")
	}
	return append(append([]string{}, d.warnings...), warnings...)
}

// Set edits one field, leaving other fields and their original representations intact.
func (d *Document) Set(name string, value any) (err error) {
	// Keep unexpected encoding failures local to this field.
	defer func() {
		if failure := recover(); failure != nil {
			err = fmt.Errorf("encode %s: %v", name, failure)
		}
	}()
	node := encodeValue(value)
	if previous := field(&d.Frontmatter, name); previous != nil {
		preserveAliases(&d.Frontmatter, previous)
		node.Anchor, node.HeadComment = previous.Anchor, previous.HeadComment
		node.LineComment, node.FootComment = previous.LineComment, previous.FootComment
		*previous = node
	} else {
		d.Frontmatter.Kind, d.Frontmatter.Tag = yaml.MappingNode, "!!map"
		d.Frontmatter.Content = append(d.Frontmatter.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name}, &node)
	}
	return nil
}

// Move a removed nested anchor to its first surviving reference. Other aliases
// keep their names, including recursive references, without dangling definitions.
func preserveAliases(root, replaced *yaml.Node) {
	removed := map[*yaml.Node]*yaml.Node{}
	var collect func(*yaml.Node)
	collect = func(node *yaml.Node) {
		if node.Anchor != "" {
			removed[node] = nil
		}
		for _, child := range node.Content {
			collect(child)
		}
	}
	for _, child := range replaced.Content {
		collect(child)
	}
	if len(removed) == 0 {
		return
	}
	// A moved definition must not shadow a different definition with the same
	// name. Reserve all names before assigning unique ones to removed anchors.
	names := map[string]int{}
	var count func(*yaml.Node)
	count = func(node *yaml.Node) {
		if node.Anchor != "" {
			names[node.Anchor]++
		}
		for _, child := range node.Content {
			count(child)
		}
	}
	count(root)
	for node := range removed {
		if names[node.Anchor] < 2 {
			continue
		}
		name := node.Anchor
		for suffix := 1; names[node.Anchor] != 0; suffix++ {
			node.Anchor = fmt.Sprintf("%s_%d", name, suffix)
		}
		names[node.Anchor] = 1
	}
	var visit func(*yaml.Node) *yaml.Node
	visit = func(node *yaml.Node) *yaml.Node {
		if node == replaced {
			return node
		}
		target := node
		if node.Kind == yaml.AliasNode {
			target = node.Alias
		}
		if definition, affected := removed[target]; affected {
			if definition != nil {
				return &yaml.Node{Kind: yaml.AliasNode, Value: target.Anchor, Alias: definition,
					HeadComment: node.HeadComment, LineComment: node.LineComment, FootComment: node.FootComment}
			}
			if node.Kind == yaml.AliasNode {
				copy := *target
				copy.HeadComment = strings.TrimSpace(copy.HeadComment + "\n" + node.HeadComment)
				copy.LineComment = strings.TrimSpace(copy.LineComment + "\n" + node.LineComment)
				copy.FootComment = strings.TrimSpace(copy.FootComment + "\n" + node.FootComment)
				node = &copy
			}
			removed[target] = node
		}
		// Copy the slots so relocating recursive definitions cannot turn an
		// alias in the original subtree into a direct cycle in Content.
		node.Content = append([]*yaml.Node(nil), node.Content...)
		for i, child := range node.Content {
			node.Content[i] = visit(child)
		}
		return node
	}
	visit(root)
}

// Marshal preserves damaged metadata as data rather than rejecting the record.
func (d *Document) Marshal() ([]byte, error) {
	var output bytes.Buffer
	output.WriteString("---\n")
	encoder := yaml.NewEncoder(&output)
	encoder.SetIndent(2)
	if err := encoder.Encode(&d.Frontmatter); err != nil {
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	output.WriteString("---\n")
	output.WriteString(d.Body)
	return output.Bytes(), nil
}
