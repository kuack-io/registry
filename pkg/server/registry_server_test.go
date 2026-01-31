package server_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"kuack-registry/pkg/config"
	"kuack-registry/pkg/redis"
	"kuack-registry/pkg/registry"
	"kuack-registry/pkg/server"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupTestServer(t *testing.T, cfg *config.Config) (*httptest.Server, *registry.Proxy) {
	t.Helper()

	// Mock Redis
	s := miniredis.RunT(t)
	rdb, err := redis.NewClient(context.Background(), s.Addr(), "", 0)
	require.NoError(t, err)

	// Create Proxy
	proxy := registry.NewProxy(rdb)

	// Create Server
	srv := server.NewRegistryServer(cfg, proxy)

	// Return test server
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	return ts, proxy
}

func doRequest(t *testing.T, url string) *http.Response {
	t.Helper()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	require.NoError(t, err)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	return resp
}

func TestRegistryServer_Healthz(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{PublicPort: 0}
	ts, _ := setupTestServer(t, cfg)

	resp := doRequest(t, ts.URL+"/healthz")

	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestRegistryServer_Auth(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		PublicPort:    0,
		RegistryToken: "secret-token",
	}
	ts, _ := setupTestServer(t, cfg)

	// 1. Missing Token
	resp := doRequest(t, ts.URL+"/healthz?token=") // healthz is protected too in current middleware
	_ = resp.Body.Close()
	// Middleware runs before route matching, so even /healthz requires token if configured
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	// 2. Wrong Token
	resp = doRequest(t, ts.URL+"/healthz?token=wrong")
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	// 3. Correct Token
	resp = doRequest(t, ts.URL+"/healthz?token=secret-token")
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestRegistryServer_Routes(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{PublicPort: 0}
	ts, _ := setupTestServer(t, cfg)

	// 1. /registry -> Proxy (should fail with Bad Request as query params missing, handled by Proxy)
	resp := doRequest(t, ts.URL+"/registry")
	_ = resp.Body.Close()
	// Proxy.ServeHTTP checks for image param, etc. If missing likely 400 or 404 depending on implementation.
	// We assume Proxy is integrated correctly.
	assert.NotEqual(t, http.StatusNotFound, resp.StatusCode)

	// 2. /resolve -> Proxy
	resp = doRequest(t, ts.URL+"/resolve")
	_ = resp.Body.Close()
	// Proxy.ServeResolveHTTP checks for image param.
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	// 3. Unknown -> 404
	resp = doRequest(t, ts.URL+"/unknown")
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestRegistryServer_Resolve_Success(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{PublicPort: 0}
	ts, proxy := setupTestServer(t, cfg)

	// Mock Image Fetcher
	proxy.SetImageFetcher(func(ref name.Reference, options ...remote.Option) (v1.Image, error) {
		return empty.Image, nil // Return empty image, resolver will guess WASI
	})

	// Request /resolve with valid image
	resp := doRequest(t, ts.URL+"/resolve?image=test-image")

	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))
}
