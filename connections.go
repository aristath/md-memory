package songya

import (
	"errors"
	"strings"
)

// EditConnection changes a draft, using the same metadata and Markdown references
// as Record/Graph. Unrelated and malformed metadata stays in the YAML tree.
func (d *Document) EditConnection(kind Destination, relation Relation, remove bool) error {
	relation.Type = strings.TrimSpace(relation.Type)
	if relation.Type == "" {
		relation.Type = "about"
	}
	if !ValidID(relation.Target) {
		return errors.New("choose a target record")
	}
	reference := "[[" + relation.Target + "]]"
	bodyLink := (kind == Stories && relation.Type == "contains") || (kind != Stories && relation.Type == "mentions")
	if bodyLink {
		if remove {
			d.Body = strings.ReplaceAll(d.Body, reference, "")
		} else if !strings.Contains(d.Body, reference) {
			d.Body += "\n\n" + reference
		}
	}
	fieldLink := relation.Type == "entities" || relation.Type == "topics" || relation.Type == "broader" || relation.Type == "redirects"
	if fieldLink {
		node := sequenceNode(metadataField(&d.Frontmatter, relation.Type))
		kept := node.Content[:0]
		found := false
		for _, child := range node.Content {
			var target string
			_ = child.Decode(&target)
			if target == relation.Target {
				found = true
				if remove {
					continue
				}
			}
			kept = append(kept, child)
		}
		node.Content = kept
		if !remove && !found {
			node.Content = append(node.Content, yamlValue(relation.Target))
		}
		if err := d.Set(relation.Type, node); err != nil {
			return err
		}
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
		if typ == relation.Type && target == relation.Target {
			found = true
			if remove {
				continue
			}
		}
		kept = append(kept, child)
	}
	node.Content = kept
	if !remove && !found && !bodyLink && !fieldLink {
		encoded := yamlValue(relation)
		node.Content = append(node.Content, encoded)
	}
	if len(node.Content) > 0 || remove {
		return d.Set("relations", node)
	}
	return nil
}
