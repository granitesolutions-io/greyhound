package storage

import (
	"fmt"
	"time"
)

// ItemInfo holds metadata about a storage item.
type ItemInfo struct {
	Key     string
	Size    int64
	ModTime time.Time
	IsDir   bool
}

// Store is the interface for key-value storage backends.
type Store interface {
	Get(key string) ([]byte, error)
	Put(key string, data []byte) error
	Delete(key string) error
	Exists(key string) (bool, error)
	List(prefix string) ([]string, error)
	Stat(key string) (*ItemInfo, error)
}

// Config holds storage backend configuration.
// If AccessKey and SecretKey are set, S3 is used automatically.
// Otherwise, the filesystem backend is used.
type Config struct {
	// File backend options
	BaseDir string

	// S3 backend options
	Bucket    string
	Prefix    string
	Region    string
	Endpoint  string
	AccessKey string
	SecretKey string
}

// New creates a Store from the given configuration.
// If AccessKey and SecretKey are provided, an S3 store is created.
// Otherwise, a filesystem store is created using BaseDir.
func New(cfg Config) (Store, error) {
	if cfg.AccessKey != "" && cfg.SecretKey != "" {
		if cfg.Bucket == "" {
			return nil, fmt.Errorf("storage: bucket is required for s3 backend")
		}
		return NewS3Store(cfg)
	}

	if cfg.BaseDir == "" {
		return nil, fmt.Errorf("storage: base directory is required for file backend")
	}
	return NewFileStore(cfg.BaseDir), nil
}
