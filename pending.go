package memory

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

func pendingName(name string) string {
	return filepath.Join(filepath.Dir(name), "."+filepath.Base(name)+".pending")
}

// The expected checksum is in the staging filename from the start. Recovery can
// prove that a write finished even if the process died before creating .pending.
func temporaryName(name string, data []byte) string {
	return filepath.Join(filepath.Dir(name), fmt.Sprintf(".%s.%x.tmp", filepath.Base(name), sha256.Sum256(data)))
}

func stage(root *os.Root, name string, data []byte) (bool, error) {
	temporary := temporaryName(name, data)
	file, err := root.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return false, err
	}
	_, writeErr := file.Write(data)
	if err := errors.Join(writeErr, file.Close()); err != nil {
		return false, err
	}
	return prepare(root, name, temporary)
}

func prepare(root *os.Root, name, temporary string) (bool, error) {
	data, err := readFile(root, temporary)
	if err != nil {
		return false, err
	}
	if temporary != temporaryName(name, data) {
		return false, errors.New("incomplete or changed staging data retained")
	}
	file, err := root.Open(temporary)
	if err != nil {
		return false, err
	}
	err = file.Sync()
	if err != nil {
		err = file.Sync()
	}
	if err := errors.Join(err, file.Close()); err != nil {
		return false, err
	}
	if err := root.Link(temporary, pendingName(name)); err != nil {
		if !errors.Is(err, fs.ErrExist) {
			return false, err
		}
		existing, readErr := readFile(root, pendingName(name))
		if readErr != nil || !bytes.Equal(existing, data) {
			return false, fmt.Errorf("pending data conflicts with %s: %w", name, fs.ErrExist)
		}
	}
	return true, syncDirectory(root, filepath.Dir(name))
}

// Only the final file and directory sync establish publication. Cleanup failures
// are diagnostics: they cannot turn an already durable save into a failure.
func (s *Store) complete(root *os.Root, name string) (bool, error) {
	pending := pendingName(name)
	data, err := readFile(root, pending)
	if err != nil {
		return false, err
	}
	if err := root.Link(pending, name); err != nil {
		if !errors.Is(err, fs.ErrExist) {
			return false, err
		}
		existing, readErr := readFile(root, name)
		if readErr != nil {
			return false, readErr
		}
		if !bytes.Equal(data, existing) {
			return false, fmt.Errorf("pending data conflicts with %s: %w", name, fs.ErrExist)
		}
	}
	if err := syncDirectory(root, filepath.Dir(name)); err != nil {
		return false, err
	}
	temporary := temporaryName(name, data)
	stagedInfo, _ := root.Lstat(pending)
	if info, err := root.Lstat(temporary); err == nil && stagedInfo != nil && os.SameFile(info, stagedInfo) {
		s.note(temporary, root.Remove(temporary))
	}
	s.note(pending, root.Remove(pending))
	s.note("cleanup "+name, syncDirectory(root, filepath.Dir(name)))
	return true, nil
}

func (s *Store) recoverFile(root *os.Root, directory, filename string) {
	path := filepath.Join(directory, filename)
	if _, err := root.Lstat(path); errors.Is(err, fs.ErrNotExist) {
		s.note(path, nil)
		return
	}
	id, suffix, ok := strings.Cut(strings.TrimPrefix(filename, "."), ".md.")
	prefix, _ := Destination(directory).prefix()
	if !ok || !strings.HasPrefix(filename, ".") || !ValidID(id) || id[:2] != prefix {
		return
	}
	name := filepath.Join(directory, id+".md")
	if strings.HasSuffix(suffix, ".tmp") {
		if _, err := prepare(root, name, path); err != nil {
			s.note(path, err)
			return
		}
		s.note(path, nil)
	} else if suffix != "pending" {
		return
	}
	_, err := s.complete(root, name)
	s.note(name, err)
}

func (s *Store) recoverDirectory(root *os.Root, directory string) {
	entries, err := fs.ReadDir(root.FS(), directory)
	if err != nil {
		s.note(directory, err)
		return
	}
	for _, entry := range entries {
		s.recoverFile(root, directory, entry.Name())
	}
}
