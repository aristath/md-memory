package memory

import (
	"encoding"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

var errCyclicValue = errors.New("cyclic Go value cannot be encoded")

// Encode containers while tracking the active path. Custom encoders run once,
// and only fields that will actually be serialized enter the cycle check.
func encodeValue(value any) (yaml.Node, error) {
	type reference struct {
		typeOf  reflect.Type
		pointer uintptr
		length  int
		inline  bool
	}
	active := map[reference]bool{}
	var encode func(reflect.Value, bool) (yaml.Node, error)
	encode = func(v reflect.Value, inline bool) (node yaml.Node, err error) {
		if !v.IsValid() || (v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface) && v.IsNil() {
			err = node.Encode(nil)
			return
		}
		if v.Kind() == reflect.Interface {
			return encode(v.Elem(), inline)
		}
		switch v.Kind() {
		case reflect.Pointer, reflect.Map, reflect.Slice:
			if !v.IsNil() {
				key := reference{typeOf: v.Type(), pointer: uintptr(v.UnsafePointer()), inline: inline}
				if v.Kind() == reflect.Slice {
					key.length = v.Len()
				}
				if active[key] {
					return node, errCyclicValue
				}
				active[key] = true
				defer delete(active, key)
			}
		}
		if !inline && v.CanInterface() {
			switch typed := v.Interface().(type) {
			case yaml.Node:
				if nodeCycle(&typed, map[*yaml.Node]bool{}) {
					return node, errCyclicValue
				}
				err = node.Encode(typed)
				return
			case *yaml.Node:
				if nodeCycle(typed, map[*yaml.Node]bool{}) {
					return node, errCyclicValue
				}
				err = node.Encode(typed)
				return
			case time.Time, *time.Time, time.Duration:
				err = node.Encode(typed)
				return
			case yaml.Marshaler:
				output, err := typed.MarshalYAML()
				if err != nil {
					return node, err
				}
				return encode(reflect.ValueOf(output), false)
			case encoding.TextMarshaler:
				err = node.Encode(typed)
				return
			}
		}
		switch v.Kind() {
		case reflect.Pointer:
			return encode(v.Elem(), inline)
		case reflect.Map:
			node.Kind, node.Tag = yaml.MappingNode, "!!map"
			type pair struct{ key, value reflect.Value }
			entries := make([]pair, 0, v.Len())
			iterator := v.MapRange()
			for iterator.Next() {
				entries = append(entries, pair{iterator.Key(), iterator.Value()})
			}
			if v.Type().Key().Kind() == reflect.String && len(entries) > 1 {
				// Let YAML order string keys without invoking custom encoders or
				// inspecting possibly cyclic values. Integer ranks retain identity.
				ranks := make(map[string]int, len(entries))
				for i, entry := range entries {
					ranks[entry.key.String()] = i
				}
				var ordered yaml.Node
				if err := ordered.Encode(ranks); err != nil {
					return node, err
				}
				sorted := make([]pair, 0, len(entries))
				for i := 1; i < len(ordered.Content); i += 2 {
					var index int
					if err := ordered.Content[i].Decode(&index); err != nil {
						return node, err
					}
					sorted = append(sorted, entries[index])
				}
				entries = sorted
			}
			for _, entry := range entries {
				for _, item := range []reflect.Value{entry.key, entry.value} {
					child, err := encode(item, false)
					if err != nil {
						return node, err
					}
					node.Content = append(node.Content, &child)
				}
			}
		case reflect.Slice, reflect.Array:
			node.Kind, node.Tag = yaml.SequenceNode, "!!seq"
			for i := 0; i < v.Len(); i++ {
				child, err := encode(v.Index(i), false)
				if err != nil {
					return node, err
				}
				node.Content = append(node.Content, &child)
			}
		case reflect.Struct:
			node.Kind, node.Tag = yaml.MappingNode, "!!map"
			for i := 0; i < v.NumField(); i++ {
				field := v.Type().Field(i)
				if field.PkgPath != "" && !field.Anonymous {
					continue
				}
				tag := field.Tag.Get("yaml")
				if tag == "" && !strings.Contains(string(field.Tag), ":") {
					tag = string(field.Tag)
				}
				if tag == "-" {
					continue
				}
				parts := strings.Split(tag, ",")
				options := "," + strings.Join(parts[1:], ",") + ","
				flatten := strings.Contains(options, ",inline,")
				item := v.Field(i)
				if !flatten && strings.Contains(options, ",omitempty,") && emptyYAMLValue(item) {
					continue
				}
				if flatten && item.Kind() == reflect.Pointer && item.IsNil() {
					continue
				}
				child, err := encode(item, flatten)
				if err != nil {
					return node, err
				}
				if flatten {
					if child.Kind != yaml.MappingNode {
						return node, fmt.Errorf("inline field %s is not a mapping", field.Name)
					}
					node.Content = append(node.Content, child.Content...)
					continue
				}
				if strings.Contains(options, ",flow,") {
					child.Style |= yaml.FlowStyle
				}
				name := parts[0]
				if name == "" {
					name = strings.ToLower(field.Name)
				}
				node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name}, &child)
			}
		default:
			err = node.Encode(v.Interface())
		}
		return
	}
	return encode(reflect.ValueOf(value), false)
}

// Match YAML's omitempty behavior, including custom IsZero methods. Pointers
// are not followed, so checking whether a field is empty cannot recurse in a cycle.
func emptyYAMLValue(v reflect.Value) bool {
	if (v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface) && v.IsNil() {
		return true
	}
	if v.CanInterface() {
		if zero, ok := v.Interface().(yaml.IsZeroer); ok {
			return zero.IsZero()
		}
	}
	switch v.Kind() {
	case reflect.String, reflect.Map, reflect.Slice:
		return v.Len() == 0
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).PkgPath == "" && !emptyYAMLValue(v.Field(i)) {
				return false
			}
		}
		return true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64, reflect.Bool:
		return v.IsZero()
	default:
		return false
	}
}

// Alias pointers may be recursive; Content must remain a tree for YAML emission.
func nodeCycle(node *yaml.Node, active map[*yaml.Node]bool) bool {
	if node == nil {
		return false
	}
	if active[node] {
		return true
	}
	active[node] = true
	defer delete(active, node)
	for _, child := range node.Content {
		if nodeCycle(child, active) {
			return true
		}
	}
	return false
}
