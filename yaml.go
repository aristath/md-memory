package memory

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

func yamlValue(value any) *yaml.Node {
	var node yaml.Node
	if err := node.Encode(value); err != nil {
		return issueValue(err.Error())
	}
	return &node
}

func mergeComments(destination, source *yaml.Node) {
	for i, comment := range []string{source.HeadComment, source.LineComment, source.FootComment} {
		target := []*string{&destination.HeadComment, &destination.LineComment, &destination.FootComment}[i]
		if comment == *target {
			continue
		}
		if *target != "" && comment != "" {
			*target += "\n"
		}
		*target += comment
	}
}

func safeComment(comment string) string {
	unsafe := func(r rune) bool {
		return r < 32 && r != '\n' && r != '\t' || r >= 0x7f && r < 0xa0 || r == 0x2028 || r == 0x2029 || r == 0xfffe || r == 0xffff
	}
	if !utf8.ValidString(comment) || strings.IndexFunc(comment, unsafe) >= 0 {
		return strconv.Quote(comment)
	}
	return comment
}

// Normalize a supplied graph into an independent YAML tree. Definitions move to
// their first reference, so sorting a map or removing a field cannot rebind an
// alias or leave a forward reference. Damaged structure is retained as data.
func normalizeNode(root *yaml.Node) (*yaml.Node, []string) {
	contained := map[*yaml.Node]bool{}
	definitions := map[string][]*yaml.Node{}
	var collect func(*yaml.Node)
	collect = func(node *yaml.Node) {
		if node == nil || contained[node] {
			return
		}
		contained[node] = true
		if node.Anchor != "" && node.Kind != yaml.AliasNode {
			definitions[node.Anchor] = append(definitions[node.Anchor], node)
		}
		for _, child := range node.Content {
			collect(child)
		}
	}
	collect(root)
	used := map[string]bool{}
	unique := func(base string) string {
		name := base
		for suffix := 1; len(definitions[name]) != 0 || used[name]; suffix++ {
			name = fmt.Sprintf("%s_%d", base, suffix)
		}
		used[name] = true
		return name
	}
	alias := func(target *yaml.Node) *yaml.Node {
		if target.Anchor == "" {
			target.Anchor = unique("ref")
		}
		return &yaml.Node{Kind: yaml.AliasNode, Value: target.Anchor, Alias: target}
	}
	copies := map[*yaml.Node]*yaml.Node{}
	wrappers := map[*yaml.Node]bool{}
	var warnings []string
	var emit func(*yaml.Node) *yaml.Node
	emit = func(source *yaml.Node) *yaml.Node {
		if source == nil {
			return yamlValue(nil)
		}
		if source.Kind == yaml.AliasNode && len(source.Content) == 0 {
			target := dereference(source)
			if source.Alias == nil && len(definitions[source.Value]) == 1 {
				target = definitions[source.Value][0]
			}
			if target == nil {
				node := issueValue("unresolved YAML alias: " + strconv.Quote(source.Value))
				mergeComments(node, source)
				return node
			}
			node := emit(target)
			mergeComments(node, source)
			return node
		}
		if source.Kind == yaml.DocumentNode && len(source.Content) == 1 && source.Value == "" && source.Tag == "" && source.Anchor == "" {
			if wrappers[source] {
				return issueValue("cyclic YAML document retained without a value")
			}
			wrappers[source] = true
			node := emit(source.Content[0])
			delete(wrappers, source)
			mergeComments(node, source)
			return node
		}
		if node := copies[source]; node != nil {
			return alias(node)
		}
		// yaml.Node values are copies. An alias may still point to the original
		// definition; reconnect only an unambiguous, identical copy in this value.
		if !contained[source] && source.Anchor != "" {
			var match *yaml.Node
			for _, candidate := range definitions[source.Anchor] {
				if reflect.DeepEqual(source, candidate) {
					if match != nil {
						match = nil
						break
					}
					match = candidate
				}
			}
			if match != nil {
				node := emit(match)
				copies[source] = dereference(node)
				return node
			}
		}
		node := *source
		node.Content, node.Alias = nil, nil
		copies[source] = &node
		if node.Anchor != "" {
			original := node.Anchor
			base := strings.Map(func(r rune) rune {
				if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
					return r
				}
				return '_'
			}, original)
			if base != original || used[base] {
				node.Anchor = unique(base)
			}
			used[node.Anchor] = true
			if base != original {
				warnings = append(warnings, fmt.Sprintf("YAML anchor %q repaired as %q", original, node.Anchor))
			}
		}
		children := source.Content
		reason := ""
		switch source.Kind {
		case 0:
			if len(children) == 0 && source.Alias == nil {
				if source.IsZero() {
					node = *yamlValue(nil)
				} else {
					node.Kind = yaml.ScalarNode
					if node.Tag == "" {
						node.Tag = "!!str"
					}
					warnings = append(warnings, "missing YAML node kind recovered as a scalar")
				}
			} else {
				reason = "missing YAML node kind"
			}
		case yaml.ScalarNode:
			if len(children) != 0 || source.Alias != nil {
				reason = "unexpected scalar structure"
			}
		case yaml.MappingNode, yaml.SequenceNode:
			if source.Value != "" || source.Alias != nil {
				reason = "unexpected collection value"
			}
		default:
			reason = fmt.Sprintf("unrecognized YAML node kind %d", source.Kind)
		}
		if !utf8.ValidString(source.Tag) {
			reason = "invalid UTF-8 YAML tag"
		}
		if reason != "" {
			node.Kind, node.Tag, node.Value = yaml.MappingNode, "!!map", ""
			node.Content = []*yaml.Node{yamlValue("issue"), issueValue(reason), yamlValue("kind"), yamlValue(int(source.Kind))}
			if source.Value != "" {
				node.Content = append(node.Content, yamlValue("value"), yamlValue(source.Value))
			}
			if source.Tag != "" {
				node.Content = append(node.Content, yamlValue("tag"), yamlValue(source.Tag))
			}
			if len(children) != 0 {
				list := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
				node.Content = append(node.Content, yamlValue("content"), list)
				for _, child := range children {
					list.Content = append(list.Content, emit(child))
				}
			}
			if source.Alias != nil {
				node.Content = append(node.Content, yamlValue("target"), emit(source.Alias))
			}
		} else {
			if node.Kind == yaml.ScalarNode && !utf8.ValidString(node.Value) {
				encoded := yamlValue(node.Value)
				node.Tag, node.Value, node.Style = encoded.Tag, encoded.Value, encoded.Style
				warnings = append(warnings, "invalid UTF-8 scalar retained as YAML binary data")
			}
			for _, child := range children {
				node.Content = append(node.Content, emit(child))
			}
			if node.Kind == yaml.MappingNode && len(node.Content)%2 != 0 {
				node.Content = append(node.Content, issueValue("missing YAML mapping value"))
			}
		}

		return &node
	}
	node := emit(root)
	walkNodes(node, func(current *yaml.Node) {
		if current.Kind == yaml.AliasNode {
			current.Value = current.Alias.Anchor
		}
		for _, comment := range []*string{&current.HeadComment, &current.LineComment, &current.FootComment} {
			*comment = safeComment(*comment)
		}
	})
	return node, warnings
}

// sequenceNode keeps unusual values as list entries instead of flattening them
// away when organization or capture needs to add another value.
func sequenceNode(source *yaml.Node) *yaml.Node {
	node, _ := normalizeNode(source)
	if node.ShortTag() == "!!null" {
		return &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	}
	if node.Kind != yaml.SequenceNode {
		return &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: []*yaml.Node{node}}
	}
	return node
}
