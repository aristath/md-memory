package memory

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
)

// Destination selects a record directory within a store.
type Destination string

const (
	Memories Destination = "memories"
	Entities Destination = "entities"
	Topics   Destination = "topics"
	Stories  Destination = "stories"
)

var destinations = []Destination{Memories, Entities, Topics, Stories}

func (d Destination) prefix() (string, error) {
	switch d {
	case Memories:
		return "m_", nil
	case Entities:
		return "e_", nil
	case Topics:
		return "t_", nil
	case Stories:
		return "s_", nil
	}
	return "", fmt.Errorf("unknown destination %q", d)
}

// Store provides coordinated filesystem access. It holds no open files between
// calls and can be shared between goroutines. Separate processes use the same lock.
type Store struct {
	path string
}

// Open initializes missing directories and opens an existing store without
// rewriting records. The persistent .lock file coordinates engine processes;
// unlike .index, it must not be removed while the store is in use.
func Open(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("store path must not be empty")
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		return nil, err
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	// Exclusive creation avoids following a pre-existing symlink or special file.
	lock, err := root.OpenFile(".lock", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err == nil {
		err = errors.Join(lock.Sync(), lock.Close())
	} else if errors.Is(err, fs.ErrExist) {
		err = regularFile(root, ".lock")
	}
	err = errors.Join(err, root.Close())
	if err != nil {
		return nil, err
	}
	store := &Store{path: path}
	err = store.locked(false, func(root *os.Root) error {
		for _, directory := range append([]Destination{".index"}, destinations...) {
			if err := root.MkdirAll(string(directory), 0700); err != nil {
				return err
			}
			info, err := root.Lstat(string(directory))
			if err != nil {
				return err
			}
			if !info.IsDir() {
				return fmt.Errorf("%s must be a directory", directory)
			}
			if err := syncDirectory(root, string(directory)); err != nil {
				return err
			}
		}
		return syncDirectory(root, ".")
	})
	if err != nil {
		return nil, err
	}
	// MkdirAll may have created several ancestors. Sync each parent so a newly
	// initialized nested store does not depend on unsynced directory entries.
	for parent := filepath.Dir(path); ; parent = filepath.Dir(parent) {
		directory, err := os.Open(parent)
		if err != nil {
			return nil, err
		}
		if err := errors.Join(directory.Sync(), directory.Close()); err != nil {
			return nil, err
		}
		if parent == filepath.Dir(parent) {
			break
		}
	}
	return store, nil
}

// Create saves a new record in an explicit destination. The engine overrides
// supplied timestamps and ignores a supplied id field. The generated ID is only
// stored in the filename and returned as Document.ID. Other metadata is preserved.
// A non-nil document alongside an error means publication succeeded but final
// syncing, staging cleanup, or lock cleanup failed; its ID identifies that record.
func (s *Store) Create(destination Destination, fields map[string]any, body string) (*Document, error) {
	prefix, err := destination.prefix()
	if err != nil {
		return nil, err
	}
	metadataFields := make(map[string]any, len(fields))
	for name, value := range fields {
		if name != "id" {
			metadataFields[name] = value
		}
	}
	document := &Document{Body: body}
	if err := document.Frontmatter.Encode(metadataFields); err != nil {
		return nil, fmt.Errorf("metadata: %w", err)
	}
	if _, exists := fields["title"]; !exists {
		if err := document.Set("title", ""); err != nil {
			return nil, err
		}
	}
	var saved *Document
	err = s.locked(false, func(root *os.Root) error {
		now := time.Now().UTC()
		for _, name := range []string{"created_at", "updated_at"} {
			if err := document.Set(name, now); err != nil {
				return err
			}
		}
		for {
			id, err := NewID(prefix)
			if err != nil {
				return err
			}
			document.ID = id
			data, err := document.Marshal()
			if err != nil {
				return err
			}
			published, err := publish(root, filepath.Join(string(destination), id+".md"), data)
			if published {
				saved = document
				return err
			}
			if !errors.Is(err, fs.ErrExist) {
				return err
			}
			// A collision (including an interrupted staging file) gets a fresh ID.
		}
	})
	return saved, err
}

// Read resolves a prefixed ID directly to its directory and derives Document.ID
// from the filename. Errors retain the affected path; files are never repaired
// or rewritten implicitly by this operation.
func (s *Store) Read(id string) (*Document, error) {
	if !ValidID(id) {
		return nil, fmt.Errorf("invalid record ID %q", id)
	}
	var destination Destination
	for _, candidate := range destinations {
		prefix, _ := candidate.prefix()
		if prefix == id[:2] {
			destination = candidate
			break
		}
	}
	name := filepath.Join(string(destination), id+".md")
	var document *Document
	err := s.locked(true, func(root *os.Root) error {
		if err := regularFile(root, name); err != nil {
			return err
		}
		data, err := root.ReadFile(name)
		if err != nil {
			return err
		}
		document, err = Parse(data)
		if err != nil {
			return err
		}
		document.ID = id
		return nil
	})
	if err != nil {
		return document, fmt.Errorf("read %s: %w", name, err)
	}
	return document, nil
}

func (s *Store) locked(shared bool, operation func(*os.Root) error) (err error) {
	if s == nil {
		return errors.New("store is nil")
	}
	root, err := os.OpenRoot(s.path)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	if err := regularFile(root, ".lock"); err != nil {
		return err
	}
	lock := flock.New(filepath.Join(s.path, ".lock"))
	defer func() { err = errors.Join(err, lock.Close()) }()
	if shared {
		err = lock.RLock()
	} else {
		err = lock.Lock()
	}
	if err != nil {
		return err
	}
	return operation(root)
}

func regularFile(root *os.Root, name string) error {
	info, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s must be a regular file", name)
	}
	return nil
}

func syncDirectory(root *os.Root, name string) error {
	directory, err := root.Open(name)
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}

// publish exposes only a complete, synced record and never replaces an existing
// name. A hard link provides atomic no-replace publication on the same filesystem.
func publish(root *os.Root, name string, data []byte) (published bool, err error) {
	temporary := filepath.Join(filepath.Dir(name), "."+filepath.Base(name)+".tmp")
	file, err := root.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return false, err
	}
	defer func() {
		if !published {
			err = errors.Join(err, root.Remove(temporary))
		}
	}()
	_, writeErr := file.Write(data)
	var syncErr error
	if writeErr == nil {
		syncErr = file.Sync()
	}
	if err := errors.Join(writeErr, syncErr, file.Close()); err != nil {
		return false, err
	}
	if err := root.Link(temporary, name); err != nil {
		return false, err
	}
	published = true
	removeErr := root.Remove(temporary)
	syncErr = syncDirectory(root, filepath.Dir(name))
	return true, errors.Join(removeErr, syncErr)
}
