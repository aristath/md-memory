package memory

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

var errUnverifiedPending = errors.New("pending checksum cannot be verified; data retained")

func pendingName(name string) string {
	return filepath.Join(filepath.Dir(name), "."+filepath.Base(name)+".pending")
}

// The directory reserves the ID; its checksum-named file records the complete
// intended write. No scan of other records is needed to claim an ID.
func temporaryName(name string, data []byte) string {
	return filepath.Join(pendingName(name), fmt.Sprintf("%x.tmp", sha256.Sum256(data)))
}

func stage(root *os.Root, name string, data []byte) (bool, error) {
	if err := root.Mkdir(pendingName(name), 0700); err != nil {
		return false, err
	}
	temporary := temporaryName(name, data)
	file, err := root.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return true, err
	}
	_, writeErr := file.Write(data)
	if err := errors.Join(writeErr, file.Close()); err != nil {
		return true, err
	}
	return true, prepare(root, name, temporary, data)
}

func prepare(root *os.Root, name, temporary string, data []byte) error {
	if temporary != temporaryName(name, data) {
		return errUnverifiedPending
	}
	for _, path := range []string{temporary, pendingName(name), filepath.Dir(name)} {
		if err := syncPath(root, path); err != nil {
			return err
		}
	}
	return nil
}

// Unknown files are retained. Multiple candidate records are ambiguous, so none
// is chosen for publication merely because it appeared first in a listing.
func stagedData(root *os.Root, name string) ([]byte, string, error) {
	directory := pendingName(name)
	info, err := root.Lstat(directory)
	if err != nil {
		return nil, "", err
	}
	if !info.IsDir() {
		return nil, "", fmt.Errorf("%s is not a staging directory", directory)
	}
	entries, err := fs.ReadDir(root.FS(), directory)
	if err != nil {
		return nil, "", err
	}
	var temporary string
	for _, entry := range entries {
		filename := entry.Name()
		if len(filename) != 68 || !strings.HasSuffix(filename, ".tmp") {
			continue
		}
		if _, err := hex.DecodeString(filename[:64]); err != nil {
			continue
		}
		if temporary != "" {
			return nil, "", fmt.Errorf("multiple staged records for %s; all retained", name)
		}
		temporary = filepath.Join(directory, filename)
	}
	if temporary == "" {
		return nil, "", fmt.Errorf("unfinished write for %s is retained without a readable record", name)
	}
	data, err := readFile(root, temporary)
	return data, temporary, err
}

func (s *Store) complete(root *os.Root, name string) (bool, error) {
	data, temporary, err := stagedData(root, name)
	if err != nil {
		// A crash during cleanup may leave an empty reservation after the final
		// record is durable. Remove only an empty directory, never unknown data.
		info, statErr := root.Lstat(pendingName(name))
		if _, readErr := readFile(root, name); readErr == nil && statErr == nil && info.IsDir() {
			if removeErr := root.Remove(pendingName(name)); removeErr == nil {
				s.note(name+".cleanup", syncPath(root, filepath.Dir(name)))
				return true, nil
			}
		}
		return false, err
	}
	if err := prepare(root, name, temporary, data); err != nil {
		return false, err
	}
	return s.publish(root, name, data)
}

// Both a new write and recovered work arrive here with verified, synced staging
// data. A new hard link reuses that synced inode. An existing final file is
// checked and synced before its staged copy is removed.
func (s *Store) publish(root *os.Root, name string, data []byte) (bool, error) {
	temporary := temporaryName(name, data)
	if err := root.Link(temporary, name); err != nil {
		if !errors.Is(err, fs.ErrExist) {
			return false, err
		}
		existing, readErr := readFile(root, name)
		if readErr != nil || !bytes.Equal(data, existing) {
			return false, fmt.Errorf("pending data conflicts with %s: %w", name, fs.ErrExist)
		}
		if err := syncPath(root, name); err != nil {
			return false, err
		}
	}
	if err := syncPath(root, filepath.Dir(name)); err != nil {
		return false, err
	}
	// Publication is durable. Cleanup problems are diagnostics, never failed saves.
	s.note(temporary, root.Remove(temporary))
	s.note(pendingName(name), root.Remove(pendingName(name)))
	s.note(name+".cleanup", syncPath(root, filepath.Dir(name)))
	return true, nil
}

func (s *Store) recoverDirectory(root *os.Root, directory string) {
	entries, err := fs.ReadDir(root.FS(), directory)
	if err == nil {
		err = syncPath(root, directory)
	}
	if err != nil {
		s.note(directory, err)
		return
	}
	s.issues.Range(func(key, _ any) bool {
		if strings.HasPrefix(key.(string), directory+string(filepath.Separator)) {
			s.issues.Delete(key)
		}
		return true
	})
	for _, entry := range entries {
		filename := entry.Name()
		id := strings.TrimSuffix(strings.TrimPrefix(filename, "."), ".md.pending")
		prefix, _ := Destination(directory).prefix()
		if filename != "."+id+".md.pending" || !ValidID(id) || id[:2] != prefix {
			continue
		}
		name := filepath.Join(directory, id+".md")
		_, err := s.complete(root, name)
		s.note(name, err)
	}
}
