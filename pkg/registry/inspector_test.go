package registry_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"kuack-registry/pkg/registry"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/static"
	"github.com/google/go-containerregistry/pkg/v1/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	errResolutionFailed = errors.New("resolution failed")
	errFetchFailed      = errors.New("fetch failed")
	errLayerError       = errors.New("layer error")
)

type mockLayer struct {
	v1.Layer
}

func (m *mockLayer) Uncompressed() (io.ReadCloser, error) {
	return nil, errLayerError
}

func doRequest(t *testing.T, url string) *http.Response {
	t.Helper()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	require.NoError(t, err)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	return resp
}

func TestProxy_ServeResolveHTTP(t *testing.T) {
	t.Parallel()

	p := setupTestProxy(t)
	// Mock image fetcher to return empty image (defaults to WASI/c2w fallback)
	p.SetImageFetcher(func(ref name.Reference, options ...remote.Option) (v1.Image, error) {
		return empty.Image, nil
	})

	ts := httptest.NewServer(http.HandlerFunc(p.ServeResolveHTTP))
	t.Cleanup(ts.Close)

	// 1. Missing Image Param
	resp := doRequest(t, ts.URL)

	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	// 3. Resolve Error
	p.SetImageFetcher(func(ref name.Reference, options ...remote.Option) (v1.Image, error) {
		return nil, errResolutionFailed
	})

	resp = doRequest(t, ts.URL+"?image=error-image")

	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)

	// Reset fetcher for success case
	p.SetImageFetcher(func(ref name.Reference, options ...remote.Option) (v1.Image, error) {
		return empty.Image, nil
	})

	// 4. Success
	resp = doRequest(t, ts.URL+"?image=test-image")

	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var config registry.WasmConfig

	err := json.NewDecoder(resp.Body).Decode(&config)
	require.NoError(t, err)

	// Check default fallback values
	assert.Equal(t, "wasi", config.Type)
	assert.Equal(t, "wasm32/wasi", config.Variant)
	assert.Equal(t, "/output.wasm", config.Path)
}

func TestProxy_ResolveWasmConfig_Cache(t *testing.T) {
	t.Parallel()

	p := setupTestProxy(t)
	callCount := 0

	p.SetImageFetcher(func(ref name.Reference, options ...remote.Option) (v1.Image, error) {
		callCount++

		return empty.Image, nil
	})

	ctx := context.Background()
	imageRef := "cached-image"

	// First call - miss
	cfg1, err := p.ResolveWasmConfig(ctx, imageRef)
	require.NoError(t, err)
	assert.Equal(t, 1, callCount)

	// Second call - hit
	cfg2, err := p.ResolveWasmConfig(ctx, imageRef)
	require.NoError(t, err)
	assert.Equal(t, 1, callCount) // count should not increase

	assert.Equal(t, cfg1, cfg2)
}

func TestProxy_ResolveWasmConfig_Error(t *testing.T) {
	t.Parallel()

	p := setupTestProxy(t)
	p.SetImageFetcher(func(ref name.Reference, options ...remote.Option) (v1.Image, error) {
		return nil, errFetchFailed
	})

	_, err := p.ResolveWasmConfig(context.Background(), "error-image")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fetch failed")
}

func TestProxy_InspectImageContent_Bindgen(t *testing.T) {
	t.Parallel()

	// Create a layer with pkg/package.json indicating bindgen
	pkgJson := []byte(`{"main": "test_bg.js"}`)
	layer := createLayer(t, map[string][]byte{
		"pkg/package.json": pkgJson,
	})
	img, err := mutate.AppendLayers(empty.Image, layer)
	require.NoError(t, err)

	p := setupTestProxy(t)
	p.SetImageFetcher(func(ref name.Reference, options ...remote.Option) (v1.Image, error) {
		return img, nil
	})

	cfg, err := p.ResolveWasmConfig(context.Background(), "bindgen-image")
	require.NoError(t, err)

	assert.Equal(t, "bindgen", cfg.Type)
	assert.Equal(t, "pkg/test_bg_bg.wasm", cfg.Path)
}

func TestProxy_ArchNormalization(t *testing.T) {
	t.Parallel()

	p := setupTestProxy(t)
	p.SetImageFetcher(func(ref name.Reference, options ...remote.Option) (v1.Image, error) {
		// Mock returning an image with a file that matches
		layer := createLayer(t, map[string][]byte{"file": []byte("content")})
		img, _ := mutate.AppendLayers(empty.Image, layer)

		return img, nil
	})

	ts := httptest.NewServer(http.HandlerFunc(p.ServeHTTP))
	t.Cleanup(ts.Close)

	// x86_64 should resolve to amd64
	resp := doRequest(t, ts.URL+"?image=img&path=file&variant=linux/x86_64")
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// aarch64 should resolve to arm64
	resp = doRequest(t, ts.URL+"?image=img&path=file&variant=linux/aarch64")
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// riscv64 should remain riscv64 (default case)
	resp = doRequest(t, ts.URL+"?image=img&path=file&variant=linux/riscv64")
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestProxy_LayerError(t *testing.T) {
	t.Parallel()

	p := setupTestProxy(t)
	p.SetImageFetcher(func(ref name.Reference, options ...remote.Option) (v1.Image, error) {
		baseLayer := static.NewLayer([]byte("data"), types.DockerLayer)

		img, err := mutate.AppendLayers(empty.Image, &mockLayer{Layer: baseLayer})
		if err != nil {
			return nil, err
		}

		return img, nil
	})

	ts := httptest.NewServer(http.HandlerFunc(p.ServeHTTP))
	t.Cleanup(ts.Close)

	resp := doRequest(t, ts.URL+"?image=bad-layer&path=file")
	_ = resp.Body.Close()

	assert.Equal(t, http.StatusBadGateway, resp.StatusCode)
}
