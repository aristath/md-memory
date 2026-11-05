package songya

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// DeleteBlocked retains the complete list so a UI can navigate to each source.
type DeleteBlocked struct {
	Records     []Record     `json:"records"`
	Diagnostics []Diagnostic `json:"diagnostics,omitempty"`
}

func (e *DeleteBlocked) Error() string {
	return "Deletion blocked; remove incoming links or repair the listed sources first"
}

// Delete permanently removes one unreferenced, unchanged record. Checks and
// removal hold the same cross-process lock used by all engine writers.
func (s *Store) Delete(ctx context.Context, id, revision string) error {
	name, err := recordName(id)
	if err != nil {
		return err
	}
	return s.lockedContext(ctx, func(root *os.Root) error {
		blocked := &DeleteBlocked{Records: []Record{}}
		catalog, err := s.scanRoot(ctx, root, false, nil)
		if err != nil {
			return err
		}
		for _, issue := range catalog.Diagnostics {
			if !strings.HasPrefix(issue.Path, ".index") {
				blocked.Diagnostics = append(blocked.Diagnostics, issue)
			}
		}
		seen := map[string]bool{}
		check := func(r Record) {
			if r.ID == id {
				return
			}
			links := append([]Relation{}, r.Relations...)
			for _, target := range r.Redirects {
				links = append(links, Relation{Type: "redirect", Target: target})
			}
			for _, link := range links {
				if link.Target == id && !seen[r.Source] {
					blocked.Records = append(blocked.Records, r)
					if r.Pending {
						blocked.Diagnostics = append(blocked.Diagnostics, Diagnostic{r.Source, "An unfinished write still links here; resolve or recover that write first"})
					}
					seen[r.Source] = true
				}
			}
			for _, issue := range r.Diagnostics {
				if strings.Contains(issue, "unparsed frontmatter") {
					blocked.Diagnostics = append(blocked.Diagnostics, Diagnostic{r.Source, "Metadata needs repair before incoming links can be checked"})
				}
			}
		}
		for _, r := range catalog.Records {
			check(r)
		}
		// A staged update may introduce a link or republish the target after restart.
		for _, kind := range destinations {
			entries, err := fs.ReadDir(root.FS(), string(kind))
			if err != nil {
				continue
			} // already diagnosed by scanRoot
			for _, entry := range entries {
				pendingID := strings.TrimSuffix(strings.TrimPrefix(entry.Name(), "."), ".md.pending")
				if entry.Name() != "."+pendingID+".md.pending" || !ValidID(pendingID) {
					continue
				}
				source := filepath.Join(string(kind), pendingID+".md")
				if pendingID == id {
					blocked.Diagnostics = append(blocked.Diagnostics, Diagnostic{source, "Unfinished write must recover before deletion"})
				}
				data, _, _, err := stagedData(root, source)
				if err != nil {
					blocked.Diagnostics = append(blocked.Diagnostics, Diagnostic{source, err.Error()})
					continue
				}
				doc := Parse(data)
				doc.ID = pendingID
				doc.Pending = true
				check(recordFromDocument(source, doc, kind))
			}
		}
		entries, listErr := fs.ReadDir(root.FS(), ".batches")
		if listErr != nil && !errors.Is(listErr, fs.ErrNotExist) {
			blocked.Diagnostics = append(blocked.Diagnostics, Diagnostic{".batches", listErr.Error()})
		}
		for _, entry := range entries {
			if !validHash(entry.Name()) || !entry.IsDir() {
				continue
			}
			directory := filepath.Join(".batches", entry.Name())
			committed, err := committedBatch(root, directory)
			if err == nil && committed {
				continue
			}
			withdrawn, withdrawErr := abandonment(root, directory)
			if withdrawErr == nil && withdrawn == "abandoned" {
				continue
			}
			journal, readErr := loadJournal(root, directory)
			if err != nil || withdrawErr != nil || readErr != nil {
				blocked.Diagnostics = append(blocked.Diagnostics, Diagnostic{directory, "Unfinished write cannot be inspected"})
				continue
			}
			for _, item := range journal.Entries {
				if item.ID == id {
					blocked.Diagnostics = append(blocked.Diagnostics, Diagnostic{item.Source, "An unfinished batch still changes this record"})
				}
				doc := Parse(item.After)
				doc.ID = item.ID
				doc.Pending = true
				check(recordFromDocument(item.Source, doc, kindOf(item.ID)))
			}
		}
		if len(blocked.Records) > 0 || len(blocked.Diagnostics) > 0 {
			return blocked
		}
		data, err := readFile(root, name)
		if errors.Is(err, fs.ErrNotExist) {
			return syncPath(root, filepath.Dir(name))
		} // retry after successful unlink
		if err != nil {
			return err
		}
		if revision == "" || dataRevision(data) != revision {
			return fmt.Errorf("delete %s: %w", id, ErrConflict)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := root.Remove(name); err != nil {
			return err
		}
		return syncPath(root, filepath.Dir(name))
	})
}
