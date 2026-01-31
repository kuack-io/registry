package registry_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var errPullMock = errors.New("pull error")

func TestProxy(t *testing.T) {
	t.Parallel()

	proxy := setupTestProxy(t)
	proxy.SetFetchArtifact(func(ctx context.Context, ref, artifactPath string, platform *v1.Platform) ([]byte, error) {
		if ref == "nginx:latest" && artifactPath == "index.html" {
			return []byte("<html></html>"), nil
		}

		return nil, assert.AnError
	})

	ts := httptest.NewServer(proxy)
	defer ts.Close()

	// Test Fetch
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, ts.URL+"/?image=nginx:latest&path=index.html", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, "<html></html>", string(body))
}

func TestProxy_RegistryFetch(t *testing.T) {
	t.Parallel()

	// Create a layer with a file
	fileContent := []byte("hello world")
	layer := createLayer(t, map[string][]byte{"test.txt": fileContent})
	img, err := mutate.AppendLayers(empty.Image, layer)
	require.NoError(t, err)

	p := setupTestProxy(t)
	p.SetImageFetcher(func(ref name.Reference, options ...remote.Option) (v1.Image, error) {
		return img, nil
	})

	ts := httptest.NewServer(p)
	t.Cleanup(ts.Close)

	// Test Fetch with Platform
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, ts.URL+"/?image=test-image&path=test.txt&variant=wasm32-wasi", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// Verify content
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, fileContent, body)
}

func TestProxy_RegistryFetch_Whiteout(t *testing.T) {
	t.Parallel()

	layer1 := createLayer(t, map[string][]byte{"test.txt": []byte("v1")})
	layer2 := createLayer(t, map[string][]byte{".wh.test.txt": []byte("")})

	img, err := mutate.AppendLayers(empty.Image, layer1, layer2)
	require.NoError(t, err)

	p := setupTestProxy(t)
	p.SetImageFetcher(func(ref name.Reference, options ...remote.Option) (v1.Image, error) {
		return img, nil
	})

	ts := httptest.NewServer(p)
	t.Cleanup(ts.Close)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, ts.URL+"/?image=test-image&path=test.txt", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestProxy_RegistryFetch_ImagePullError(t *testing.T) {
	t.Parallel()

	p := setupTestProxy(t)
	p.SetImageFetcher(func(ref name.Reference, options ...remote.Option) (v1.Image, error) {
		return nil, errPullMock
	})

	ts := httptest.NewServer(p)
	t.Cleanup(ts.Close)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, ts.URL+"/?image=test-image&path=test.txt", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusBadGateway, resp.StatusCode)
}

func TestProxy_Variants(t *testing.T) {
	t.Parallel()

	p := setupTestProxy(t)
	p.SetImageFetcher(func(ref name.Reference, options ...remote.Option) (v1.Image, error) {
		return empty.Image, nil
	})

	ts := httptest.NewServer(p)
	t.Cleanup(ts.Close)

	tests := []struct {
		name           string
		variant        string
		expectedStatus int
	}{
		{"Valid Alias", "wasm32-wasi", http.StatusNotFound},
		{"Valid OS/Arch", "linux/amd64", http.StatusNotFound},
		{"Invalid Format", "invalid", http.StatusBadRequest},
		{"Extra Slashes", "os/arch/extra", http.StatusNotFound},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, ts.URL+"/?image=img&path=file&variant="+tc.variant, nil)
			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)

			defer func() { _ = resp.Body.Close() }()

			assert.Equal(t, tc.expectedStatus, resp.StatusCode)
		})
	}
}

func TestProxy_ContentType(t *testing.T) {
	t.Parallel()

	files := map[string][]byte{
		"test.wasm": []byte("wasm"),
		"test.js":   []byte("js"),
		"test.txt":  []byte("txt"),
	}
	layer := createLayer(t, files)
	img, err := mutate.AppendLayers(empty.Image, layer)
	require.NoError(t, err)

	p := setupTestProxy(t)
	p.SetImageFetcher(func(ref name.Reference, options ...remote.Option) (v1.Image, error) {
		return img, nil
	})

	ts := httptest.NewServer(p)
	t.Cleanup(ts.Close)

	tests := []struct {
		filename     string
		expectedType string
	}{
		{"test.wasm", "application/wasm"},
		{"test.js", "application/javascript"},
		{"test.txt", "text/plain; charset=utf-8"},
	}

	for _, tc := range tests {
		t.Run(tc.filename, func(t *testing.T) {
			t.Parallel()

			req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, ts.URL+"/?image=img&path="+tc.filename, nil)
			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)

			defer func() { _ = resp.Body.Close() }()

			assert.Equal(t, http.StatusOK, resp.StatusCode)
			assert.Equal(t, tc.expectedType, resp.Header.Get("Content-Type"))
		})
	}
}
