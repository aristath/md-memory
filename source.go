package songya

import (
	"context"
	"errors"
)

// SaveSource edits a complete document through the same durable receipts as MCP
// writes. A stable key makes retries safe; updates require the bytes' revision.
func (s *Store) SaveSource(ctx context.Context, key, id string, kind Destination, revision, source string) (Receipt, error) {
	if id != "" && revision == "" {
		return Receipt{}, errors.New("open the record before saving changes")
	}
	return s.ApplyBatch(ctx, BatchRequest{Key: key, Changes: []Change{{Ref: "record", ID: id, Kind: kind, Revision: revision, source: &source}}})
}
