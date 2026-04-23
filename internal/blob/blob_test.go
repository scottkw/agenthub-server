package blob

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFileBlob_PutGetRoundTrip(t *testing.T) {
	base := t.TempDir()
	fb := NewFileBlob(FileBlobOptions{BasePath: base, BaseURL: "http://localhost/api/blobs"})

	ctx := context.Background()
	key := "test-obj-1"
	data := []byte("hello blobs")

	require.NoError(t, fb.Put(ctx, key, bytes.NewReader(data), "text/plain"))

	got, err := fb.Get(ctx, key)
	require.NoError(t, err)
	defer got.Close()
	body, err := io.ReadAll(got)
	require.NoError(t, err)
	require.Equal(t, data, body)

	exists, err := fb.Exists(ctx, key)
	require.NoError(t, err)
	require.True(t, exists)
}

func TestFileBlob_PresignURLs(t *testing.T) {
	fb := NewFileBlob(FileBlobOptions{BasePath: t.TempDir(), BaseURL: "http://localhost/api/blobs"})
	ctx := context.Background()

	putURL, err := fb.PresignPut(ctx, "obj-1", "text/plain", 100)
	require.NoError(t, err)
	require.Equal(t, "http://localhost/api/blobs/upload/obj-1", putURL)

	getURL, err := fb.PresignGet(ctx, "obj-1")
	require.NoError(t, err)
	require.Equal(t, "http://localhost/api/blobs/download/obj-1", getURL)
}

func TestFileBlob_Delete(t *testing.T) {
	base := t.TempDir()
	fb := NewFileBlob(FileBlobOptions{BasePath: base, BaseURL: "http://localhost/api/blobs"})
	ctx := context.Background()
	key := "del-me"

	require.NoError(t, fb.Put(ctx, key, bytes.NewReader([]byte("x")), "text/plain"))
	require.NoError(t, fb.Delete(ctx, key))

	exists, err := fb.Exists(ctx, key)
	require.NoError(t, err)
	require.False(t, exists)
}

func TestFileBlob_ExistsFalse(t *testing.T) {
	fb := NewFileBlob(FileBlobOptions{BasePath: t.TempDir(), BaseURL: "http://localhost/api/blobs"})
	exists, err := fb.Exists(context.Background(), "never-existed")
	require.NoError(t, err)
	require.False(t, exists)
}
