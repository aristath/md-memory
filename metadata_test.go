package memory

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestInvalidAnchorsPreserveValuesAndReferences(t *testing.T) {
	for _, kind := range []yaml.Kind{yaml.ScalarNode, yaml.MappingNode} {
		path := t.TempDir()
		store, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		definition := &yaml.Node{Kind: kind, Tag: "!!str", Anchor: "bad anchor", Value: "Keep value"}
		var want any = "Keep value"
		if kind == yaml.MappingNode {
			definition.Tag, definition.Value = "!!map", ""
			definition.Content = []*yaml.Node{
				{Kind: yaml.ScalarNode, Tag: "!!str", Value: "fact"},
				{Kind: yaml.ScalarNode, Tag: "!!str", Value: "Keep value"},
			}
			want = map[string]any{"fact": "Keep value"}
		}
		reference := &yaml.Node{Kind: yaml.AliasNode, Value: "bad anchor", Alias: definition}
		reserved := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Anchor: "bad_anchor", Value: "Keep reserved"}
		doc, err := store.Create(Memories, map[string]any{
			"title": "Healthy title",
			"details": map[string]any{
				"fact":   "Healthy fact",
				"values": []*yaml.Node{definition, reference, reserved},
			},
		}, "Healthy body")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(strings.Join(doc.Diagnostics(), "\n"), "repaired") {
			t.Fatal("anchor repair was not reported")
		}
		if definition.Anchor != "bad anchor" || reference.Value != "bad anchor" {
			t.Fatal("repair modified the caller's nodes")
		}
		store, err = Open(path)
		if err != nil {
			t.Fatal(err)
		}
		read, err := store.Read(doc.ID)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]any
		if err := read.Frontmatter.Decode(&fields); err != nil {
			t.Fatal(err)
		}
		details := fields["details"].(map[string]any)
		if details["fact"] != "Healthy fact" || !reflect.DeepEqual(details["values"], []any{want, want, "Keep reserved"}) || read.Body != "Healthy body" || read.Metadata().Title != "Healthy title" || len(read.Diagnostics()) != 0 {
			t.Fatalf("repair lost content or left broken references: %v, %v", fields, read.Diagnostics())
		}
	}
}

const mergeDefaults = `base: &base
  title: Inherited title
  created_at: 2026-11-03T10:00:00Z
  updated_at: 2026-11-03T10:00:00Z
  relations:
    - type: motivated_by
      target: m_b8r5n2w9d4hx
other: &other
  title: Other title
  created_at: 2020-01-01T00:00:00Z
`

func TestMetadataUsesYAMLMergePrecedence(t *testing.T) {
	for name, fields := range map[string]string{
		"mapping":  "<<: *base\n",
		"sequence": "<<: [*other, *base]\n",
		"nested":   "nested: &nested\n  <<: *base\n  title: Nested title\n<<: *nested\n",
		"explicit": "title: Explicit title\n<<: [*base, *other]\n",
		"null":     "<<: *base\ntitle: null\n",
		"literal":  "'<<': *base\n",
	} {
		t.Run(name, func(t *testing.T) {
			doc := Parse([]byte("---\n" + mergeDefaults + fields + "---\nBody"))
			var want Metadata
			if err := doc.Frontmatter.Decode(&want); err != nil {
				t.Fatal(err)
			}
			before, err := doc.Marshal()
			if err != nil {
				t.Fatal(err)
			}
			if got := doc.Metadata(); !reflect.DeepEqual(got, want) {
				t.Fatalf("merge semantics changed: got %+v, want %+v", got, want)
			}
			unchanged, err := doc.Marshal()
			if err != nil || !bytes.Equal(before, unchanged) {
				t.Fatal("reading metadata changed the source tree")
			}
			if err := doc.Set("title", "Edited title"); err != nil {
				t.Fatal(err)
			}
			after, err := doc.Marshal()
			if err != nil {
				t.Fatal(err)
			}
			read := Parse(after)
			base := field(&read.Frontmatter, "base")
			if read.Metadata().Title != "Edited title" || field(base, "title").Value != "Inherited title" || read.Body != "Body" {
				t.Fatal("reading or editing inherited metadata changed its source")
			}
		})
	}
}

func TestDamagedMergeSourcesKeepHealthyMetadataAvailable(t *testing.T) {
	for _, fields := range []string{
		"<<: [broken, *base]\n",
		"loop: &loop\n  <<: *loop\n<<: [*loop, *base]\n",
		"<<: *base\ncreated_at: unknown\ntitle: First title\ntitle: Second title\n",
	} {
		doc := Parse([]byte("---\n" + mergeDefaults + fields + "---\nBody"))
		m := doc.Metadata()
		if m.Title == "" || m.UpdatedAt.IsZero() || len(m.Relations) != 1 || doc.Body != "Body" {
			t.Fatalf("one damaged source hid healthy fields: %+v", m)
		}
		if strings.Contains(fields, "unknown") && (m.Title != "First title" || !m.CreatedAt.IsZero() || len(doc.Diagnostics()) == 0) {
			t.Fatal("damaged explicit fields were hidden by inherited defaults")
		}
		encoded, err := doc.Marshal()
		if err != nil || Parse(encoded).Body != "Body" {
			t.Fatalf("damaged merge prevented serialization: %v", err)
		}
	}
}
