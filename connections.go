package songya

import (
	"errors"
	"strings"
)

// EditConnection edits the single relations list. Other metadata and prose remain intact.
func (d *Document) EditConnection(relation Relation, remove bool) error {
	if !remove && strings.TrimSpace(relation.Type) == "" {
		relation.Type = "related_to"
	}
	if normalized := relationType(relation.Type); normalized != "" {
		relation.Type = normalized
	} else if !remove {
		return errors.New("choose after, caused_by, part_of, related_to or possibly_same_as")
	}
	if !remove && !ValidID(relation.Target) {
		return errors.New("choose a target record")
	}
	node := sequenceNode(metadataField(&d.Frontmatter, "relations"))
	kept := node.Content[:0]
	found := false
	for _, child := range node.Content {
		var typ, target string
		if n := metadataField(child, "type"); n != nil {
			_ = n.Decode(&typ)
		}
		if n := metadataField(child, "target"); n != nil {
			_ = n.Decode(&target)
		}
		if normalized := relationType(typ); normalized != "" {
			typ = normalized
		}
		if strings.TrimSpace(typ) == strings.TrimSpace(relation.Type) && strings.TrimSpace(target) == strings.TrimSpace(relation.Target) {
			found = true
			if remove {
				continue
			}
		}
		kept = append(kept, child)
	}
	node.Content = kept
	if !remove && !found {
		encoded := yamlValue(relation)
		node.Content = append(node.Content, encoded)
	}
	if len(node.Content) > 0 || remove {
		return d.Set("relations", node)
	}
	return nil
}

// MoveConnection changes narrative reading order without changing the connection.
func (d *Document) MoveConnection(relation Relation, step int) error {
	node := sequenceNode(metadataField(&d.Frontmatter, "relations"))
	for i, child := range node.Content {
		var current Relation
		if child.Decode(&current) != nil {
			continue
		}
		if normalized := relationType(current.Type); normalized != "" {
			current.Type = normalized
		}
		current.Target = strings.TrimSpace(current.Target)
		if current == relation {
			next := i + step
			if next >= 0 && next < len(node.Content) {
				node.Content[i], node.Content[next] = node.Content[next], node.Content[i]
			}
			return d.Set("relations", node)
		}
	}
	return nil
}
