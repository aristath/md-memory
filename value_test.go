package memory

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"
)

func TestValueEncodingPreservesYAMLSemantics(t *testing.T) {
	var source yaml.Node
	if err := yaml.Unmarshal([]byte("tagged: !custom preserved\nvalue: &v [Keep]\ncopy: *v\n"), &source); err != nil {
		t.Fatal(err)
	}
	type inlineFields struct{ Label string }
	type tagged struct {
		inlineFields `yaml:",inline"`
		Renamed      string            `yaml:"custom_name"`
		Ignored      string            `yaml:"-"`
		Empty        string            `yaml:",omitempty"`
		Array        [1]int            `yaml:",omitempty"`
		Extras       map[string]string `yaml:",inline"`
		Flow         []int             `yaml:",flow"`
	}
	now := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	for name, value := range map[string]any{
		"nil":         nil,
		"nil map":     map[string]any(nil),
		"nil list":    []string(nil),
		"scalars":     []any{"000123", "yes", true, 3, 1.5, nil},
		"map keys":    map[any]any{3: "Three", "other": []string{"Keep"}},
		"string keys": map[string]any{"key10": "Ten", "key2": "Two", "other": []int{1, 2}},
		"binary keys": map[string]string{"\xff": "First", "\xfe": "Second"},
		"times": struct {
			Time     time.Time
			Pointer  *time.Time
			Duration time.Duration
		}{now, &now, time.Second},
		"tags": tagged{inlineFields: inlineFields{Label: "Keep"}, Renamed: "Keep", Ignored: "Drop",
			Extras: map[string]string{"extra": "Keep"}, Flow: []int{1, 2}},
		"inline nil pointer": struct {
			*inlineFields `yaml:",inline"`
			Name          string
		}{Name: "Keep"},
		"YAML node": *source.Content[0],
	} {
		t.Run(name, func(t *testing.T) {
			expected, err := yaml.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := encodeValue(value)
			if err != nil {
				t.Fatal(err)
			}
			actual, err := yaml.Marshal(&encoded)
			if err != nil {
				t.Fatal(err)
			}
			var want, got any
			if err := yaml.Unmarshal(expected, &want); err != nil {
				t.Fatal(err)
			}
			if err := yaml.Unmarshal(actual, &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(want, got) {
				t.Fatalf("YAML values changed:\nwant:\n%sgot:\n%s", expected, actual)
			}
			if name == "string keys" && !bytes.Equal(expected, actual) {
				t.Fatalf("map ordering changed:\nwant:\n%sgot:\n%s", expected, actual)
			}
			if name == "tags" && !strings.Contains(string(actual), "flow: [1, 2]") {
				t.Fatalf("flow style lost: %s", actual)
			}
			if name == "YAML node" && !strings.Contains(string(actual), "!custom") {
				t.Fatalf("custom tag lost: %s", actual)
			}
		})
	}
}
