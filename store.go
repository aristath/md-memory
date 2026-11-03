package memory

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/flock"
)

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

// Store holds no open files between calls. Diagnostics is safe across goroutines.
type Store struct {
	path   string
	issues sync.Map
}

func (s *Store) note(path string, err error) {
	if err == nil {
		s.issues.Delete(path)
	} else {
		s.issues.Store(path, err.Error())
	}
}

func (s *Store) Diagnostics() []string {
	var messages []string
	s.issues.Range(func(path, issue any) bool {
		messages = append(messages, fmt.Sprintf("%s: %s", path, issue))
		return true
	})
	sort.Strings(messages)
	return messages
}

// Open opens the root and lock. Problems in individual directories are reported
// through Diagnostics rather than disabling healthy parts of the store.
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
	s := &Store{path: path}
	// Creating .lock completes initialization. Until it exists, retries must
	// sync every ancestor, including directories left by an interrupted Open.
	if _, err := root.Lstat(".lock"); errors.Is(err, fs.ErrNotExist) {
		if err := syncParents(path); err != nil {
			s.note("initialization", err)
			s.note("close root", root.Close())
			return s, nil // Existing content remains readable; writes lack a lock.
		}
	}
	lock, err := root.OpenFile(".lock", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err == nil {
		err = lock.Close()
	} else if errors.Is(err, fs.ErrExist) {
		err = regularFile(root, ".lock")
	}
	err = errors.Join(err, root.Close())
	if err != nil {
		s.note("write coordination", err)
		return s, nil
	}
	_ = s.Recover()
	return s, nil
}

// An initialized store does not need its ancestors reopened on later calls.
func syncParents(path string) error {
	for parent := filepath.Dir(path); ; parent = filepath.Dir(parent) {
		directory, err := os.Open(parent)
		if err != nil {
			return err
		}
		if err := errors.Join(directory.Sync(), directory.Close()); err != nil {
			return err
		}
		if parent == filepath.Dir(parent) {
			return nil
		}
	}
}

func ensureDirectory(root *os.Root, name string) error {
	info, err := root.Lstat(name)
	if err == nil {
		if info.IsDir() {
			return nil
		}
		return fmt.Errorf("%s is not a directory", name)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := root.Mkdir(name, 0700); err != nil {
		return err
	}
	return syncPath(root, ".")
}

// Recover resumes completed pending writes when the write lock is available.
// Open reports deferred recovery without blocking access to readable records.
func (s *Store) Recover() error {
	err := s.locked(false, func(root *os.Root) error {
		// The cache is disposable, unlike the record directories.
		if info, err := root.Lstat(".index"); err == nil && !info.IsDir() {
			s.note(".index", root.Remove(".index"))
		}
		s.note(".index", ensureDirectory(root, ".index"))
		for _, destination := range destinations {
			name := string(destination)
			err := ensureDirectory(root, name)
			s.note(name, err)
			if err == nil {
				s.recoverDirectory(root, name)
			}
		}
		return nil
	})
	if s != nil {
		s.note("recovery", err)
	}
	return err
}

// Create assigns the filename ID and UTC timestamps. Metadata problems are
// diagnostics, not reasons to discard the body. Pending=true means the complete
// record is durably staged and readable, while final publication will be retried.
func (s *Store) Create(destination Destination, fields map[string]any, body string) (*Document, error) {
	destination = Destination(strings.ToLower(strings.TrimSpace(string(destination))))
	prefix, err := destination.prefix()
	if err != nil {
		return nil, err
	}
	d := &Document{Body: body}
	for name, value := range fields {
		if name == "id" || name == "created_at" || name == "updated_at" {
			continue
		}
		if err := d.Set(name, value); err != nil {
			d.warnings = append(d.warnings, err.Error())
			_ = d.Set(name, fmt.Sprint(value))
		}
	}
	if field(&d.Frontmatter, "title") == nil {
		_ = d.Set("title", "")
	}
	now := time.Now().UTC()
	_ = d.Set("created_at", now)
	_ = d.Set("updated_at", now)
	err = s.locked(true, func(root *os.Root) error {
		if err := ensureDirectory(root, string(destination)); err != nil {
			return err
		}
		if err := syncPath(root, "."); err != nil {
			return err
		}
		for {
			id, err := NewID(prefix)
			if err != nil {
				return err
			}
			name := filepath.Join(string(destination), id+".md")
			if _, err := root.Lstat(name); err == nil {
				continue
			} else if !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			if _, err := root.Lstat(pendingName(name)); err == nil {
				continue
			} else if !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			d.ID = id
			data, err := d.Marshal()
			if err != nil {
				return err
			}
			ready, err := stage(root, name, data)
			d.Pending = ready
			if err != nil {
				if !ready && errors.Is(err, fs.ErrExist) {
					continue
				}
				return err
			}
			published, err := s.complete(root, name)
			d.Pending = !published
			s.note(name, err)
			if err != nil {
				d.warnings = append(d.warnings, err.Error())
			}
			if errors.Is(err, fs.ErrExist) || errors.Is(err, errUnverifiedPending) {
				return err
			} // Conflicting or changed data cannot confirm this save.
			return nil // The synced pending record remains readable even if publication is blocked.
		}
	})
	return d, err
}

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
	// Published files are complete. Reading one needs no write coordination.
	if s != nil {
		if root, err := os.OpenRoot(s.path); err == nil {
			data, readErr := readFile(root, name)
			pending := regularFile(root, pendingName(name)) == nil
			_ = root.Close()
			if readErr == nil {
				d := Parse(data)
				d.ID = id
				if pending {
					d.warnings = append(d.warnings, "Pending work is retained for recovery; committed content returned")
				}
				return d, nil
			}
		}
	}
	var d *Document
	err := s.locked(false, func(root *os.Root) error {
		pending := false
		var recoveryErr error
		var staging []string
		if err := regularFile(root, name); err != nil {
			// Only missing/blocked records need a directory scan for pre-publication writes.
			matches, err := fs.Glob(root.FS(), filepath.Join(string(destination), "."+id+".md.*.tmp"))
			staging = matches
			s.note("staging "+id, err)
			for _, path := range matches {
				s.recoverFile(root, string(destination), filepath.Base(path))
			}
		}
		if err := regularFile(root, pendingName(name)); err == nil {
			published, err := s.complete(root, name)
			recoveryErr = err
			pending = !published
			s.note(name, err)
		}
		data, err := readFile(root, name)
		if err != nil {
			data, err = readFile(root, pendingName(name))
			if err != nil {
				if len(staging) > 0 {
					return fmt.Errorf("unfinished write for %s is retained; see store diagnostics", id)
				}
				return err
			}
			pending = true
		}
		d = Parse(data)
		d.ID, d.Pending = id, pending
		if recoveryErr != nil {
			d.warnings = append(d.warnings, recoveryErr.Error())
		}
		return nil
	})
	if err != nil {
		// Recovery may be busy or blocked. Recheck committed content first in
		// case the writer just published it, then return readable pending data.
		if s != nil {
			if root, openErr := os.OpenRoot(s.path); openErr == nil {
				defer root.Close()
				for _, candidate := range []string{name, pendingName(name)} {
					if data, readErr := readFile(root, candidate); readErr == nil {
						d = Parse(data)
						d.ID, d.Pending = id, candidate != name
						d.warnings = append(d.warnings, err.Error())
						return d, nil
					}
				}
			}
		}
		return d, fmt.Errorf("read %s: %w", name, err)
	}
	return d, nil
}

func (s *Store) locked(wait bool, operation func(*os.Root) error) error {
	if s == nil {
		return errors.New("store is nil")
	}
	root, err := os.OpenRoot(s.path)
	if err != nil {
		return err
	}
	defer func() { s.note("close root", root.Close()) }()
	if err := regularFile(root, ".lock"); err != nil {
		return err
	}
	lock := flock.New(filepath.Join(s.path, ".lock"))
	defer func() { s.note("close lock", lock.Close()) }()
	if wait {
		if err := lock.Lock(); err != nil {
			return err
		}
	} else {
		acquired, err := lock.TryLock()
		if err != nil {
			return err
		}
		if !acquired {
			return errors.New("recovery deferred while another writer holds the lock")
		}
	}
	s.note("initialization", nil)
	s.note("write coordination", nil)
	return operation(root)
}

func regularFile(root *os.Root, name string) error {
	info, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", name)
	}
	return nil
}

func readFile(root *os.Root, name string) ([]byte, error) {
	if err := regularFile(root, name); err != nil {
		return nil, err
	}
	return root.ReadFile(name)
}

func syncPath(root *os.Root, name string) error {
	file, err := root.Open(name)
	if err != nil {
		return err
	}
	err = file.Sync()
	if err != nil {
		err = file.Sync()
	}
	return errors.Join(err, file.Close())
}
