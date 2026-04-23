// Package blob defines the object storage abstraction. Implementations:
//   - FileBlob (solo mode) — stores objects on the local filesystem.
//
// Future Plans add an S3 implementation that returns signed S3 URLs from
// PresignPut/PresignGet and no-ops Put/Get.
package blob

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Blob is the storage abstraction used by the API layer.
type Blob interface {
	// PresignPut returns a URL the client can PUT to upload an object.
	PresignPut(ctx context.Context, key string, contentType string, size int64) (url string, err error)

	// PresignGet returns a URL the client can GET to download an object.
	PresignGet(ctx context.Context, key string) (url string, err error)

	// Put stores the object bytes. Used by the in-process upload handler
	// (file backend) or by the server when it needs to write on behalf of the client.
	Put(ctx context.Context, key string, r io.Reader, contentType string) error

	// Get returns a ReadCloser for the object bytes. Caller must close.
	Get(ctx context.Context, key string) (io.ReadCloser, error)

	// Exists reports whether the object is present in storage.
	Exists(ctx context.Context, key string) (bool, error)

	// Delete removes the object from storage.
	Delete(ctx context.Context, key string) error
}

// FileBlobOptions tunes the file backend.
type FileBlobOptions struct {
	BasePath string // directory where objects are stored (e.g. /var/lib/agenthub-server/blobs)
	BaseURL  string // public URL prefix for in-process endpoints (e.g. http://host/api/blobs)
}

// FileBlob stores objects as flat files under BasePath.
type FileBlob struct {
	opts FileBlobOptions
}

// NewFileBlob returns a file-backed Blob.
func NewFileBlob(opts FileBlobOptions) *FileBlob {
	return &FileBlob{opts: opts}
}

func (f *FileBlob) path(key string) string {
	return filepath.Join(f.opts.BasePath, key)
}

func (f *FileBlob) PresignPut(_ context.Context, key string, _ string, _ int64) (string, error) {
	return f.opts.BaseURL + "/upload/" + key, nil
}

func (f *FileBlob) PresignGet(_ context.Context, key string) (string, error) {
	return f.opts.BaseURL + "/download/" + key, nil
}

func (f *FileBlob) Put(_ context.Context, key string, r io.Reader, _ string) error {
	path := f.path(key)
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		return fmt.Errorf("blob.Put: mkdir: %w", err)
	}
	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("blob.Put: create: %w", err)
	}
	defer file.Close()
	if _, err := io.Copy(file, r); err != nil {
		return fmt.Errorf("blob.Put: copy: %w", err)
	}
	return nil
}

func (f *FileBlob) Get(_ context.Context, key string) (io.ReadCloser, error) {
	file, err := os.Open(f.path(key))
	if err != nil {
		return nil, fmt.Errorf("blob.Get: %w", err)
	}
	return file, nil
}

func (f *FileBlob) Exists(_ context.Context, key string) (bool, error) {
	_, err := os.Stat(f.path(key))
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("blob.Exists: %w", err)
	}
	return true, nil
}

func (f *FileBlob) Delete(_ context.Context, key string) error {
	if err := os.Remove(f.path(key)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("blob.Delete: %w", err)
	}
	return nil
}
