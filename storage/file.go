package storage

import (
	"os"
	"path/filepath"
	"strings"
)

// FileStore implements Store using the local filesystem.
type FileStore struct {
	baseDir string
}

// NewFileStore creates a filesystem-backed store rooted at baseDir.
func NewFileStore(baseDir string) *FileStore {
	return &FileStore{baseDir: baseDir}
}

func (f *FileStore) path(key string) string {
	return filepath.Join(f.baseDir, key)
}

func (f *FileStore) Get(key string) ([]byte, error) {
	return os.ReadFile(f.path(key))
}

func (f *FileStore) Put(key string, data []byte) error {
	p := f.path(key)
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		return err
	}

	// Atomic write: tmp + rename
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func (f *FileStore) Delete(key string) error {
	return os.RemoveAll(f.path(key))
}

func (f *FileStore) Exists(key string) (bool, error) {
	_, err := os.Stat(f.path(key))
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func (f *FileStore) List(prefix string) ([]string, error) {
	dir := f.path(prefix)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var keys []string
	for _, e := range entries {
		key := prefix + "/" + e.Name()
		keys = append(keys, strings.TrimPrefix(key, "/"))
	}
	return keys, nil
}
