package memory

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// BatchData exposes retained work without changing it. Both original and proposed
// record bytes remain available after commit or abandonment. Receipt data can be
// byte-paged by a client even when a batch contains thousands of records.
func (s *Store) BatchData(ctx context.Context, key, id, view string) ([]byte, error) {
	if s == nil {
		return nil, errors.New("store is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(s.path)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	journal, err := loadJournal(root, batchDirectory(key))
	if err != nil {
		return nil, err
	}
	switch view {
	case "account":
		return journal.Account, nil
	case "before", "proposed":
		for _, entry := range journal.Entries {
			if entry.ID == id {
				if view == "before" {
					return entry.Before, nil
				}
				return entry.After, nil
			}
		}
		return nil, fmt.Errorf("record %s is not in this batch", id)
	default:
		return nil, fmt.Errorf("choose account, before or proposed")
	}
}

// ReadArchive returns the checksum-verified original retained by an import.
func (s *Store) ReadArchive(revision string) ([]byte, error) {
	if s == nil {
		return nil, errors.New("store is nil")
	}
	if !validHash(revision) {
		return nil, errors.New("an archive checksum is required")
	}
	root, err := os.OpenRoot(s.path)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	data, err := readFile(root, filepath.Join(".imports", revision+".md"))
	if err != nil {
		return nil, err
	}
	if dataRevision(data) != revision {
		return nil, errors.New("preserved archive checksum mismatch; bytes retained")
	}
	return data, nil
}

func batchReadIssue(root *os.Root, name string) error {
	owner, err := readFile(root, filepath.Join(pendingName(name), "batch"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err == nil {
		if !validHash(string(owner)) {
			err = errors.New("batch ownership marker is damaged")
		} else {
			_, err = loadJournal(root, filepath.Join(".batches", string(owner)))
		}
	}
	if err != nil {
		return fmt.Errorf("unconfirmed batch snapshot; readable bytes retained: %w", err)
	}
	return nil
}
