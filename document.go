// Package memory implements the Markdown record format for md-memory.
package memory

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// Relation is an outgoing connection. Type names have no built-in semantics.
type Relation struct {
	Type   string `yaml:"type"`
	Target string `yaml:"target"`
}

// Metadata is a view of the core fields. Additional fields remain in Frontmatter.
type Metadata struct {
	ID        string     `yaml:"id"`
	Title     string     `yaml:"title"`
	CreatedAt time.Time  `yaml:"created_at"`
	UpdatedAt time.Time  `yaml:"updated_at"`
	Relations []Relation `yaml:"relations,omitempty"`
}

// Document retains the YAML syntax tree and the exact Markdown body.
// Keeping the tree avoids discarding metadata, tags, aliases, and comments.
type Document struct {
	Frontmatter yaml.Node
	Body        string
}

// NewDocument creates a document from core metadata. Further fields can be set
// through Set or by editing the YAML tree.
func NewDocument(metadata Metadata, body string) (*Document, error) {
	metadata.CreatedAt = metadata.CreatedAt.UTC()
	metadata.UpdatedAt = metadata.UpdatedAt.UTC()
	d := &Document{Body: body}
	if err := d.Frontmatter.Encode(metadata); err != nil {
		return nil, err
	}
	if _, err := d.Metadata(); err != nil {
		return nil, err
	}
	return d, nil
}

// Parse reads a YAML-frontmatter Markdown document without changing its source.
func Parse(data []byte) (*Document, error) {
	data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	line, rest, _ := bytes.Cut(data, []byte("\n"))
	if !delimiter(line) {
		return nil, errors.New("missing opening YAML frontmatter delimiter")
	}
	var header bytes.Buffer
	for len(rest) > 0 {
		line, remaining, newline := bytes.Cut(rest, []byte("\n"))
		if delimiter(line) {
			d := &Document{Body: string(remaining)}
			if err := yaml.Unmarshal(header.Bytes(), &d.Frontmatter); err != nil {
				return nil, fmt.Errorf("frontmatter: %w", err)
			}
			if d.Frontmatter.Kind == yaml.DocumentNode && len(d.Frontmatter.Content) == 1 {
				d.Frontmatter = *d.Frontmatter.Content[0]
			}
			if _, err := d.Metadata(); err != nil {
				return nil, err
			}
			return d, nil
		}
		header.Write(line)
		if newline {
			header.WriteByte('\n')
		}
		rest = remaining
	}
	return nil, errors.New("missing closing YAML frontmatter delimiter")
}

func delimiter(line []byte) bool {
	return bytes.Equal(bytes.TrimSpace(line), []byte("---"))
}

// Metadata reads and validates core fields without removing additional fields.
func (d *Document) Metadata() (Metadata, error) {
	var metadata Metadata
	if d == nil || d.Frontmatter.Kind != yaml.MappingNode {
		return metadata, errors.New("frontmatter must be a YAML mapping")
	}
	if err := d.Frontmatter.Decode(&metadata); err != nil {
		return metadata, fmt.Errorf("frontmatter: %w", err)
	}
	if !ValidID(metadata.ID) {
		return metadata, errors.New("id must contain 12 lowercase letters or digits")
	}
	if metadata.CreatedAt.IsZero() || metadata.UpdatedAt.IsZero() {
		return metadata, errors.New("created_at and updated_at must be timestamps")
	}
	for i, relation := range metadata.Relations {
		if strings.TrimSpace(relation.Type) == "" || !ValidID(relation.Target) {
			return metadata, fmt.Errorf("relation %d requires a type and a valid target ID", i)
		}
	}
	return metadata, nil
}

// Set replaces one top-level field, preserving other fields and their order.
// Core-field validity is checked by Metadata and Marshal.
func (d *Document) Set(name string, value any) error {
	if d == nil || d.Frontmatter.Kind != yaml.MappingNode {
		return errors.New("frontmatter must be a YAML mapping")
	}
	var node yaml.Node
	if err := node.Encode(value); err != nil {
		return err
	}
	for i := 0; i < len(d.Frontmatter.Content); i += 2 {
		if d.Frontmatter.Content[i].Value == name {
			previous := d.Frontmatter.Content[i+1]
			node.Anchor = previous.Anchor
			node.HeadComment = previous.HeadComment
			node.LineComment = previous.LineComment
			node.FootComment = previous.FootComment
			*previous = node
			return nil
		}
	}
	d.Frontmatter.Content = append(d.Frontmatter.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name}, &node)
	return nil
}

// Marshal serializes a document, preserving its Markdown body byte for byte.
// YAML formatting may change; additional fields stay in the syntax tree.
func (d *Document) Marshal() ([]byte, error) {
	if _, err := d.Metadata(); err != nil {
		return nil, err
	}
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
