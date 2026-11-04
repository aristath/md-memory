package songya

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

func userOwned(kind Destination) bool {
	kind = Destination(strings.ToLower(strings.TrimSpace(string(kind))))
	return kind == Memories || kind == Stories
}

func documentUser(doc *Document) string {
	node := dereference(metadataField(&doc.Frontmatter, "user"))
	if node == nil || node.Kind != yaml.ScalarNode || node.ShortTag() == "!!null" || node.Tag == issueTag {
		return ""
	}
	var user string
	if node.Decode(&user) != nil || !utf8.ValidString(user) {
		return ""
	}
	return strings.TrimSpace(user)
}

func requireUser(doc *Document, kind Destination) error {
	if !userOwned(kind) {
		return nil
	}
	user := documentUser(doc)
	if user == "" || user == "all" {
		return fmt.Errorf("%s require a user field identifying one user; all is a query selector", kind)
	}
	return doc.Set("user", user)
}

// User is a selection filter, never an authorization check. Shared record kinds
// remain visible, and an omitted filter or "all" selects the whole catalogue.
func userCatalog(catalog Catalog, user string) Catalog {
	user = strings.TrimSpace(user)
	if user == "" || user == "all" {
		return catalog
	}
	records := []Record{}
	for _, record := range catalog.Records {
		if !userOwned(record.Kind) || record.User == user {
			records = append(records, record)
		}
	}
	catalog.Records = records
	return catalog
}
