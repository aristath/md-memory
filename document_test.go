package songya

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"
)

const sample = `---
user: test
title: Example
created_at: 2026-11-03T10:00:00Z
updated_at: 2026-11-03T10:00:00Z
relations:
  - type: caused_by
    target: m_b8r5n2w9d4hx
  - type: related_to
    target: t_9za4cd6ef8gh
# User-defined metadata
aliases: &names ["α", "000123", "yes"]
custom:
  names: *names
  enabled: false
  count: 0
  unset: null
  tagged: !custom "preserved"
---

# Body

Literal text with --- inside it.
`

func TestRoundTripPreservesAdditionalMetadataAndBody(t *testing.T) {
	doc := Parse([]byte(sample))
	before := doc.Body
	if err := doc.Set("title", "Renamed"); err != nil {
		t.Fatal(err)
	}
	encoded, err := doc.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	reparsed := Parse(encoded)
	metadata := reparsed.Metadata()
	if metadata.Title != "Renamed" || len(metadata.Relations) != 2 || reparsed.Body != before {
		t.Fatalf("record changed unexpectedly: %+v", metadata)
	}
	for _, value := range []string{"# User-defined metadata", "&names", "*names", "!custom", `"000123"`, `"yes"`, "enabled: false", "count: 0", "unset: null"} {
		if !bytes.Contains(encoded, []byte(value)) {
			t.Errorf("lost metadata representation %q", value)
		}
	}
}

func TestCRLFAndBodyWithoutFinalNewline(t *testing.T) {
	input := strings.ReplaceAll(sample, "\n", "\r\n")
	input = strings.TrimSuffix(input, "\r\n")
	doc := Parse(append([]byte{0xef, 0xbb, 0xbf}, []byte(input)...))
	want := doc.Body
	encoded, err := doc.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	reparsed := Parse(encoded)
	if reparsed.Body != want {
		t.Fatal("body changed during round trip")
	}
}

func TestEditingAnchoredFieldPreservesReferences(t *testing.T) {
	input := strings.Replace(sample, "title: Example", "title: &label Example # Keep this comment", 1)
	input = strings.Replace(input, "custom:\n", "custom:\n  label: *label\n", 1)
	doc := Parse([]byte(input))
	if err := doc.Set("title", "Renamed"); err != nil {
		t.Fatal(err)
	}
	encoded, err := doc.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	reparsed := Parse(encoded)
	var fields struct {
		Custom struct{ Label string }
	}
	if err := reparsed.Frontmatter.Decode(&fields); err != nil || fields.Custom.Label != "Renamed" {
		t.Fatalf("anchor reference was lost: %+v, %v", fields, err)
	}
	if !bytes.Contains(encoded, []byte("# Keep this comment")) {
		t.Fatal("field comment was lost")
	}
}

func TestIncomingAnchorsDoNotChangeOtherFields(t *testing.T) {
	for _, edit := range []string{"middle", "added"} {
		t.Run(edit, func(t *testing.T) {
			doc := Parse([]byte("---\noriginal: &shared Keep original\nmiddle: Old\nreserved: &shared_1 Keep reserved\ncopy: *shared\nreserved_copy: *shared_1\n---\nBody"))
			var incoming yaml.Node
			if err := yaml.Unmarshal([]byte("[&shared Changed, *shared]"), &incoming); err != nil {
				t.Fatal(err)
			}
			if err := doc.Set(edit, &incoming); err != nil {
				t.Fatal(err)
			}
			var before, after map[string]any
			if err := doc.Frontmatter.Decode(&before); err != nil {
				t.Fatal(err)
			}
			encoded, err := doc.Marshal()
			if err != nil {
				t.Fatal(err)
			}
			read := Parse(encoded)
			if err := read.Frontmatter.Decode(&after); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) || after["copy"] != "Keep original" || after["reserved_copy"] != "Keep reserved" || !reflect.DeepEqual(after[edit], []any{"Changed", "Changed"}) {
				t.Fatalf("an edit changed unrelated metadata after serialization:\nbefore: %v\nafter: %v", before, after)
			}
		})
	}
}

func TestReplacingFieldKeepsIncomingRootReferences(t *testing.T) {
	for _, anchor := range []string{"", "&existing "} {
		t.Run(anchor, func(t *testing.T) {
			copy := ""
			if anchor != "" {
				copy = "copy: *existing\n"
			}
			doc := Parse([]byte("---\nmiddle: " + anchor + "Old\n" + copy + "---\nBody"))
			var incoming yaml.Node
			if err := yaml.Unmarshal([]byte("&incoming {label: Keep, self: *incoming}"), &incoming); err != nil {
				t.Fatal(err)
			}
			if err := doc.Set("middle", &incoming); err != nil {
				t.Fatal(err)
			}
			encoded, err := doc.Marshal()
			if err != nil {
				t.Fatal(err)
			}
			read := Parse(encoded)
			middle := field(&read.Frontmatter, "middle")
			if middle == nil || middle.Kind != yaml.MappingNode || field(middle, "self").Alias != middle || field(middle, "label").Value != "Keep" {
				t.Fatalf("incoming recursive reference was broken:\n%s", encoded)
			}
			if anchor != "" && field(&read.Frontmatter, "copy").Alias != middle {
				t.Fatal("existing reference no longer follows the edited field")
			}
		})
	}
}

func TestMalformedDocumentsStayUsable(t *testing.T) {
	for name, input := range map[string]string{
		"no header":       "# Plain Markdown",
		"unclosed header": "---\ntitle: Example\n",
		"invalid YAML":    "---\na: [\n---\nOriginal body",
		"non-mapping":     "---\n- value\n---\nOriginal body",
		"duplicate title": strings.Replace(sample, "title: Example", "title: Example\ntitle: Duplicate", 1),
		"missing time":    strings.Replace(sample, "created_at: 2026-11-03T10:00:00Z\n", "", 1),
		"invalid target":  strings.Replace(sample, "m_b8r5n2w9d4hx", "missing", 1),
		"empty type":      strings.Replace(sample, "type: caused_by", "type: ''", 1),
	} {
		t.Run(name, func(t *testing.T) {
			data := []byte(input)
			doc := Parse(data)
			if len(doc.Diagnostics()) == 0 {
				t.Fatal("damaged data had no diagnostics")
			}
			if string(data) != input {
				t.Fatal("parser changed its input")
			}
			encoded, err := doc.Marshal()
			if err != nil {
				t.Fatal(err)
			}
			if Parse(encoded).Body != doc.Body {
				t.Fatal("body did not survive serialization")
			}
			if strings.Contains(name, "header") && doc.Body != input {
				t.Fatal("original source was not retained")
			}
			if name == "invalid YAML" || name == "non-mapping" {
				var raw string
				if err := field(&doc.Frontmatter, "unparsed_frontmatter").Decode(&raw); err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(input, raw) || doc.Body != "Original body" {
					t.Fatal("unparsed data was lost")
				}
			}
		})
	}
}

func TestNewDocumentKeepsDamagedMetadata(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.FixedZone("example", 7200))
	doc, err := NewDocument(Metadata{Title: "Example", CreatedAt: now, UpdatedAt: now}, "Body")
	if err != nil {
		t.Fatal(err)
	}
	data, err := doc.Marshal()
	if err != nil || !bytes.Contains(data, []byte("2026-11-03T10:00:00Z")) {
		t.Fatalf("timestamps were not written in UTC: %s, %v", data, err)
	}
	if err := doc.Set("created_at", "invalid"); err != nil {
		t.Fatal(err)
	}
	if _, err := doc.Marshal(); err != nil {
		t.Fatal(err)
	}
	if len(doc.Diagnostics()) == 0 {
		t.Fatal("invalid timestamp was not reported")
	}
}

func TestDocumentIDIsNotSerializedOrInferredFromContent(t *testing.T) {
	doc := Parse([]byte(sample))
	if doc.ID != "" {
		t.Fatalf("standalone parsing invented an ID: %q", doc.ID)
	}
	doc.ID = "m_7k3m9x2q6v4c"
	encoded, err := doc.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	reparsed := Parse(encoded)
	var fields map[string]any
	if err := reparsed.Frontmatter.Decode(&fields); err != nil {
		t.Fatal(err)
	}
	if _, exists := fields["id"]; exists || reparsed.ID != "" {
		t.Fatal("document identity was serialized into the file")
	}
}

func TestIDs(t *testing.T) {
	for _, id := range []string{"", "7k3m9x2q6v4c", "m_7K3M9X2Q6V4C", "m_7k3m9x2q6v4!", "x_7k3m9x2q6v4c", "m-7k3m9x2q6v4c", "m_7k3m9x2q6v4cc", "m_../badpath!"} {
		if ValidID(id) {
			t.Errorf("accepted invalid ID %q", id)
		}
	}
	for _, prefix := range []string{"m_", "e_", "t_", "s_"} {
		if !ValidID(prefix + "7k3m9x2q6v4c") {
			t.Errorf("rejected valid ID with prefix %q", prefix)
		}
		for range 25 {
			id, err := NewID(prefix)
			if err != nil || !ValidID(id) || !strings.HasPrefix(id, prefix) {
				t.Fatalf("invalid generated ID %q: %v", id, err)
			}
		}
	}
	for _, prefix := range []string{"", "m", "x_", "memory_"} {
		if _, err := NewID(prefix); err == nil {
			t.Errorf("generated ID with invalid prefix %q", prefix)
		}
	}
}
