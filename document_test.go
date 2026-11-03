package memory

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

const sample = `---
id: m_7k3m9x2q6v4c
title: Example
created_at: 2026-11-03T10:00:00Z
updated_at: 2026-11-03T10:00:00Z
relations:
  - type: motivated_by
    target: m_b8r5n2w9d4hx
  - type: custom_connection
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
	doc, err := Parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	before := doc.Body
	if err := doc.Set("title", "Renamed"); err != nil {
		t.Fatal(err)
	}
	encoded, err := doc.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	reparsed, err := Parse(encoded)
	if err != nil {
		t.Fatal(err)
	}
	metadata, _ := reparsed.Metadata()
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
	doc, err := Parse(append([]byte{0xef, 0xbb, 0xbf}, []byte(input)...))
	if err != nil {
		t.Fatal(err)
	}
	want := doc.Body
	encoded, err := doc.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	reparsed, err := Parse(encoded)
	if err != nil || reparsed.Body != want {
		t.Fatalf("body changed during round trip: %v", err)
	}
}

func TestEditingAnchoredFieldPreservesReferences(t *testing.T) {
	input := strings.Replace(sample, "title: Example", "title: &label Example # Keep this comment", 1)
	input = strings.Replace(input, "custom:\n", "custom:\n  label: *label\n", 1)
	doc, err := Parse([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if err := doc.Set("title", "Renamed"); err != nil {
		t.Fatal(err)
	}
	encoded, err := doc.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	reparsed, err := Parse(encoded)
	if err != nil {
		t.Fatal(err)
	}
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

func TestRejectsMalformedDocumentsWithoutChangingInput(t *testing.T) {
	for name, input := range map[string]string{
		"no header":       "# Plain Markdown",
		"unclosed header": "---\nid: m_7k3m9x2q6v4c\n",
		"invalid YAML":    "---\na: [\n---\n",
		"non-mapping":     "---\n- value\n---\n",
		"duplicate ID":    strings.Replace(sample, "title: Example", "id: m_b8r5n2w9d4hx", 1),
		"invalid ID":      strings.Replace(sample, "m_7k3m9x2q6v4c", "../bad", 1),
		"missing time":    strings.Replace(sample, "created_at: 2026-11-03T10:00:00Z\n", "", 1),
		"invalid target":  strings.Replace(sample, "m_b8r5n2w9d4hx", "missing", 1),
		"empty type":      strings.Replace(sample, "type: motivated_by", "type: ''", 1),
	} {
		t.Run(name, func(t *testing.T) {
			data := []byte(input)
			if _, err := Parse(data); err == nil {
				t.Fatal("expected a parse error")
			}
			if string(data) != input {
				t.Fatal("parser changed its input")
			}
		})
	}
}

func TestNewDocumentAndValidationBeforeMarshal(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.FixedZone("example", 7200))
	doc, err := NewDocument(Metadata{ID: "m_7k3m9x2q6v4c", Title: "Example", CreatedAt: now, UpdatedAt: now}, "Body")
	if err != nil {
		t.Fatal(err)
	}
	data, err := doc.Marshal()
	if err != nil || !bytes.Contains(data, []byte("2026-11-03T10:00:00Z")) {
		t.Fatalf("timestamps were not written in UTC: %s, %v", data, err)
	}
	if err := doc.Set("id", "invalid"); err != nil {
		t.Fatal(err)
	}
	if _, err := doc.Marshal(); err == nil {
		t.Fatal("invalid document was serialized")
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
