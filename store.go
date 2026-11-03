package memory

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
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
	path      string
	issues    sync.Map
	initMu    sync.Mutex
	needsInit bool
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
	_, err = root.Lstat(".lock")
	s.needsInit = errors.Is(err, fs.ErrNotExist)
	s.note("close root", root.Close())
	_ = s.Recover()
	return s, nil
}

// Only an initialization known to be unfinished may create the lock. A missing
// lock on an already initialized handle must not replace another writer's lock.
func (s *Store) initialize(root *os.Root) (err error) {
	s.initMu.Lock()
	defer s.initMu.Unlock()
	if !s.needsInit {
		return regularFile(root, ".lock")
	}
	defer func() { s.note("initialization", err) }()
	if _, err = root.Lstat(".lock"); errors.Is(err, fs.ErrNotExist) {
		if err = syncParents(s.path); err != nil {
			return err
		}
		var file *os.File
		file, err = root.OpenFile(".lock", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
		if err == nil {
			err = file.Close()
		}
	}
	if err == nil || errors.Is(err, fs.ErrExist) {
		err = regularFile(root, ".lock")
	}
	if err == nil {
		s.needsInit = false
	}
	return err
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
			_ = d.Set(name, issueValue(err.Error()))
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
			d.ID = id
			data, err := d.Marshal()
			if err != nil {
				return err
			}
			retained, err := stage(root, name, data)
			d.Pending = retained
			if err != nil {
				if !retained && errors.Is(err, fs.ErrExist) {
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
	if s == nil {
		return nil, errors.New("store is nil")
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
	root, err := os.OpenRoot(s.path)
	if err != nil {
		return nil, err
	}
	defer func() { s.note("close root", root.Close()) }()
	doc, readErr := readCopy(root, id, name)
	if doc != nil && !doc.Pending {
		return doc, nil
	}
	if _, err := root.Lstat(pendingName(name)); errors.Is(err, fs.ErrNotExist) {
		return doc, readErr
	}
	recoveryErr := s.locked(false, func(root *os.Root) error {
		_, err := s.complete(root, name)
		s.note(name, err)
		return err
	})
	if latest, err := readCopy(root, id, name); err == nil {
		doc, readErr = latest, nil
	} else if doc != nil {
		doc.warnings = append(doc.warnings, "Readable snapshot retained: "+err.Error())
	} else {
		readErr = err
	}
	if doc == nil {
		return nil, fmt.Errorf("read %s: %w", name, errors.Join(readErr, recoveryErr))
	}
	if recoveryErr != nil && doc.Pending && !slices.Contains(doc.warnings, recoveryErr.Error()) {
		doc.warnings = append(doc.warnings, recoveryErr.Error())
	}
	return doc, nil
}

// One policy for every read: committed content wins; staged bytes stay useful
// even when incomplete. Recheck the committed name after concurrent cleanup.
func readCopy(root *os.Root, id, name string) (*Document, error) {
	data, err := readFile(root, name)
	pending := false
	var warning string
	if err != nil {
		committedErr := err
		var temporary string
		data, temporary, err = stagedData(root, name)
		if err == nil {
			pending = true
			if temporary != temporaryName(name, data) {
				warning = errUnverifiedPending.Error()
			}
		} else {
			stagedErr := err
			data, err = readFile(root, name)
			if err != nil {
				if errors.Is(stagedErr, fs.ErrNotExist) {
					return nil, committedErr
				}
				return nil, stagedErr
			}
		}
	}
	doc := Parse(data)
	doc.ID, doc.Pending = id, pending
	if warning != "" {
		doc.warnings = append(doc.warnings, warning)
	}
	if !pending {
		if _, err := root.Lstat(pendingName(name)); err == nil {
			doc.warnings = append(doc.warnings, "Pending work is retained for recovery; committed content returned")
		}
	}
	return doc, nil
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
	if err := s.initialize(root); err != nil {
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
	s.note("recovery", nil)
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
