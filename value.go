package memory

import (
	"fmt"
	"reflect"
	"sort"
	"time"

	"go.yaml.in/yaml/v3"
)

const issueTag = "!md-memory/issue"

func issueValue(message string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: issueTag, Value: message}
}

// Normalize data, never arbitrary Go encoding callbacks. An unsupported leaf is
// kept as a tagged diagnostic while its understandable neighbours survive.
func encodeValue(value any) *yaml.Node {
	type reference struct {
		typeOf  reflect.Type
		pointer uintptr
		length  int
	}
	active := map[reference]bool{}
	var walk func(reflect.Value) *yaml.Node
	leaf := func(value any) *yaml.Node {
		var node yaml.Node
		if err := node.Encode(value); err != nil {
			return issueValue(err.Error())
		}
		return &node
	}
	walk = func(v reflect.Value) *yaml.Node {
		if !v.IsValid() || (v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface) && v.IsNil() {
			return leaf(nil)
		}
		if v.Kind() == reflect.Interface {
			return walk(v.Elem())
		}
		switch v.Kind() {
		case reflect.Pointer, reflect.Map, reflect.Slice:
			if !v.IsNil() {
				key := reference{typeOf: v.Type(), pointer: uintptr(v.UnsafePointer())}
				if v.Kind() == reflect.Slice {
					key.length = v.Len()
				}
				if active[key] {
					return issueValue("cyclic Go reference: " + v.Type().String())
				}
				active[key] = true
				defer delete(active, key)
			}
		}
		if v.CanInterface() {
			var source *yaml.Node
			switch typed := v.Interface().(type) {
			case yaml.Node:
				source = &typed
			case *yaml.Node:
				source = typed
			case time.Time, time.Duration, Relation, Metadata:
				return leaf(typed)
			}
			if source != nil {
				if source.Kind == 0 {
					return leaf(nil)
				}
				if source.Kind == yaml.DocumentNode && len(source.Content) == 1 {
					return walk(reflect.ValueOf(source.Content[0]))
				}
				if source.Kind != yaml.ScalarNode && source.Kind != yaml.AliasNode && source.Kind != yaml.MappingNode && source.Kind != yaml.SequenceNode {
					return issueValue(fmt.Sprintf("unrecognized YAML node %d: %q", source.Kind, source.Value))
				}
				copy := *source
				copy.Content = nil
				copy.Alias = nil
				for _, child := range source.Content {
					copy.Content = append(copy.Content, walk(reflect.ValueOf(child)))
				}
				if copy.Kind == yaml.MappingNode && len(copy.Content)%2 != 0 {
					copy.Content = append(copy.Content, issueValue("missing YAML mapping value"))
				}
				return &copy
			}
		}
		switch v.Kind() {
		case reflect.Pointer:
			return walk(v.Elem())
		case reflect.Map:
			type pair struct{ key, value *yaml.Node }
			entries := make([]pair, 0, v.Len())
			iterator := v.MapRange()
			for iterator.Next() {
				entries = append(entries, pair{walk(iterator.Key()), walk(iterator.Value())})
			}
			sort.SliceStable(entries, func(i, j int) bool {
				a, b := entries[i].key, entries[j].key
				if a.Value != b.Value {
					return a.Value < b.Value
				}
				return a.Tag < b.Tag
			})
			node := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			for _, entry := range entries {
				node.Content = append(node.Content, entry.key, entry.value)
			}
			return node
		case reflect.Slice, reflect.Array:
			node := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
			for i := 0; i < v.Len(); i++ {
				node.Content = append(node.Content, walk(v.Index(i)))
			}
			return node
		case reflect.String:
			return leaf(v.String())
		case reflect.Bool:
			return leaf(v.Bool())
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			return leaf(v.Int())
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
			return leaf(v.Uint())
		case reflect.Float32, reflect.Float64:
			return leaf(v.Float())
		default:
			return issueValue("unsupported Go value: " + v.Type().String())
		}
	}
	node := walk(reflect.ValueOf(value))
	anchors := map[string]*yaml.Node{}
	var bind func(*yaml.Node)
	bind = func(node *yaml.Node) {
		if node.Anchor != "" {
			anchors[node.Anchor] = node
		}
		if node.Kind == yaml.AliasNode {
			if target := anchors[node.Value]; target != nil {
				node.Alias = target
			} else {
				*node = *issueValue("unresolved YAML alias: " + node.Value)
			}
		}
		for _, child := range node.Content {
			bind(child)
		}
	}
	bind(node)
	return node
}

func valueIssues(node *yaml.Node, path string) []string {
	var issues []string
	if node.Tag == issueTag {
		issues = append(issues, path+": "+node.Value)
	}
	for i, child := range node.Content {
		name := fmt.Sprintf("%s[%d]", path, i)
		if node.Kind == yaml.MappingNode {
			if i%2 == 0 {
				name = path + "[key]"
			} else {
				name = path + "." + node.Content[i-1].Value
			}
		}
		issues = append(issues, valueIssues(child, name)...)
	}
	return issues
}
