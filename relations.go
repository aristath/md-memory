package songya

import "strings"

var RelationTypes = [...]string{"after", "caused_by", "part_of", "related_to", "possibly_same_as"}

func relationType(value string) string {
	value = strings.ToLower(strings.Join(strings.Fields(strings.ReplaceAll(value, "-", " ")), "_"))
	for _, name := range RelationTypes {
		if value == name {
			return name
		}
	}
	return ""
}

func reverseRelation(value string) string {
	switch value {
	case "after":
		return "before"
	case "caused_by":
		return "causes"
	case "part_of":
		return "contains"
	default:
		return value
	}
}

// Story relations are the sole membership and reading-order representation.
func storyMembers(record Record) []string {
	if record.Kind != Stories {
		return nil
	}
	var members []string
	for _, relation := range record.Relations {
		kind := kindOf(relation.Target)
		if relation.Type == "related_to" && (kind == Memories || kind == Stories) {
			members = append(members, relation.Target)
		}
	}
	return members
}
