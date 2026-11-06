package songya

import (
	"reflect"
	"sort"

	"go.yaml.in/yaml/v3"
)

// Compare user-visible data, excluding engine timestamps and source bookkeeping.
// Receipt outcomes are derived from durable snapshots, so retries report the
// original operation even if the record has since changed again.
func changedRecordFields(before, after []byte) []string {
	left, right := Parse(before), Parse(after)
	changed := []string{}
	if left.Body != right.Body {
		changed = append(changed, "text")
	}
	keys := map[string]bool{}
	for _, doc := range []*Document{left, right} {
		for i := 0; i+1 < len(doc.Frontmatter.Content); i += 2 {
			if name, ok := fieldName(doc.Frontmatter.Content[i]); ok {
				keys[name] = true
			}
		}
	}
	for name := range keys {
		if name == "created_at" || name == "updated_at" || name == "original_accounts" {
			continue
		}
		a, b := metadataField(&left.Frontmatter, name), metadataField(&right.Frontmatter, name)
		if !sameFieldValue(a, b) {
			changed = append(changed, name)
		}
	}
	sort.Strings(changed)
	return changed
}

func sameFieldValue(a, b *yaml.Node) bool {
	var left, right any
	if a != nil {
		if err := a.Decode(&left); err != nil {
			return false
		}
	}
	if b != nil {
		if err := b.Decode(&right); err != nil {
			return false
		}
	}
	return reflect.DeepEqual(left, right)
}
