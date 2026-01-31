package registry

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"path"
	"path/filepath"
	"strings"
	"time"

	"kuack-registry/pkg/redis"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"k8s.io/klog/v2"
)

const (
	registryUserAgent   = "kuack-registry/proxy"
	cacheHeaderMaxAge   = 3600 // 1 hour
	allowedCORSHeaders  = "Content-Type, Authorization"
	allowedCORSMethods  = "GET, OPTIONS"
	corsAllowOriginName = "Access-Control-Allow-Origin"
	variantSplitParts   = 2
	// logVerboseLevel is the klog verbosity level for verbose debug logs.
	logVerboseLevel = 4

	// Transport timeout configuration for large WASM artifacts.
	transportDialTimeout           = 60 * time.Second
	transportKeepAlive             = 30 * time.Second
	transportResponseHeaderTimeout = 60 * time.Second
	transportIdleConnTimeout       = 60 * time.Second
	transportTLSHandshakeTimeout   = 60 * time.Second

	// Cache expiration.
	cacheExpiration = 24 * time.Hour
)

var (
	errUnsupportedVariant   = errors.New("unsupported variant")
	errInvalidVariantFormat = errors.New("invalid variant format")
	errInvalidTargetPath    = errors.New("invalid target path")
	errArtifactNotFound     = errors.New("artifact not found")
	errFileNotFound         = errors.New("file-not-found")
	errFileTooLarge         = errors.New("file too large")
)

// Proxy streams files from OCI images to the agent via HTTP with CORS.
type Proxy struct {
	cache         *redis.Client
	fetchArtifact func(ctx context.Context, ref, artifactPath string, platform *v1.Platform) ([]byte, error)
	imageFetcher  func(ref name.Reference, options ...remote.Option) (v1.Image, error)
	transport     http.RoundTripper
}

// NewProxy creates a new registry proxy instance.
func NewProxy(redisClient *redis.Client) *Proxy {
	// Clone the default transport to customize timeouts for large WASM artifacts
	defaultTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		// Fallback if not standard transport
		defaultTransport = &http.Transport{}
	}

	transport := defaultTransport.Clone()
	transport.DialContext = (&net.Dialer{
		Timeout:   transportDialTimeout,
		KeepAlive: transportKeepAlive,
	}).DialContext
	transport.ResponseHeaderTimeout = transportResponseHeaderTimeout
	transport.IdleConnTimeout = transportIdleConnTimeout
	transport.TLSHandshakeTimeout = transportTLSHandshakeTimeout

	p := &Proxy{
		cache:        redisClient,
		imageFetcher: remote.Image,
		transport:    transport,
	}
	p.fetchArtifact = p.fetchArtifactFromRegistry

	return p
}

// SetImageFetcher sets the image fetcher for testing.
func (p *Proxy) SetImageFetcher(f func(ref name.Reference, options ...remote.Option) (v1.Image, error)) {
	p.imageFetcher = f
}

// SetFetchArtifact sets the fetch artifact function for testing.
func (p *Proxy) SetFetchArtifact(f func(ctx context.Context, ref, artifactPath string, platform *v1.Platform) ([]byte, error)) {
	p.fetchArtifact = f
}

// ServeHTTP serves the registry proxy endpoint.
func (rp *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Recover from panics to ensure we don't crash
	defer func() {
		if rec := recover(); rec != nil {
			klog.Errorf("Panic in Proxy: %v", rec)
			addCORSHeaders(w)
			http.Error(w, fmt.Sprintf("internal server error: %v", rec), http.StatusInternalServerError)
		}
	}()

	addCORSHeaders(w)

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)

		return
	}

	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)

		return
	}

	imageRef := strings.TrimSpace(r.URL.Query().Get("image"))
	rawPath := r.URL.Query().Get("path")
	artifactPath := normalizeArtifactPath(rawPath)
	variantParam := strings.TrimSpace(r.URL.Query().Get("variant"))

	klog.V(logVerboseLevel).Infof("[Registry] Request: image=%s, path=%s (normalized=%s), variant=%s", imageRef, rawPath, artifactPath, variantParam)

	if imageRef == "" {
		klog.Warningf("[Registry] Missing image query parameter")
		http.Error(w, "missing image query parameter", http.StatusBadRequest)

		return
	}

	if artifactPath == "" {
		klog.Warningf("[Registry] Missing path query parameter. Raw: %q, Normalized: %q", rawPath, artifactPath)
		http.Error(w, "missing path query parameter", http.StatusBadRequest)

		return
	}

	platform, platformKey, err := resolveVariantPlatform(variantParam)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)

		return
	}

	// Cache Key: image::path::platform
	key := fmt.Sprintf("%s::%s::%s", imageRef, artifactPath, platformKey)

	// Check Redis Cache
	if rp.cache != nil {
		data, err := rp.cache.Get(r.Context(), key)
		if err == nil && data != nil {
			klog.V(logVerboseLevel).Infof("[Registry] Cache hit for: %s", key)
			rp.writeArtifact(w, data, detectContentType(artifactPath))

			return
		} else if err != nil {
			klog.Warningf("[Registry] Redis error on get: %v", err)
		}
	} else {
		// Log warning if cache is missing (should not happen in prod)
		klog.Warningf("[Registry] No cache configured")
	}

	klog.V(logVerboseLevel).Infof("[Registry] Cache miss for: %s", key)

	data, err := rp.downloadArtifact(r.Context(), imageRef, artifactPath, platform)
	if err != nil {
		klog.Errorf("Registry proxy error for %s (%s): %v", imageRef, artifactPath, err)

		addCORSHeaders(w)

		statusCode := http.StatusBadGateway

		switch {
		case errors.Is(err, errArtifactNotFound), errors.Is(err, errFileNotFound):
			statusCode = http.StatusNotFound
		case errors.Is(err, errInvalidTargetPath), errors.Is(err, errInvalidVariantFormat), errors.Is(err, errUnsupportedVariant):
			statusCode = http.StatusBadRequest
		}

		http.Error(w, fmt.Sprintf("failed to fetch artifact: %v", err), statusCode)

		return
	}

	// Save to Redis
	if rp.cache != nil {
		err := rp.cache.Set(r.Context(), key, data, cacheExpiration)
		if err != nil {
			klog.Errorf("[Registry] Failed to save to cache: %v", err)
		}
	}

	rp.writeArtifact(w, data, detectContentType(artifactPath))
}

func (rp *Proxy) downloadArtifact(ctx context.Context, imageRef, artifactPath string, platform *v1.Platform) ([]byte, error) {
	return rp.fetchArtifact(ctx, imageRef, artifactPath, platform)
}

func (rp *Proxy) writeArtifact(w http.ResponseWriter, data []byte, contentType string) {
	addCORSHeaders(w)

	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}

	w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", cacheHeaderMaxAge))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func addCORSHeaders(w http.ResponseWriter) {
	w.Header().Set(corsAllowOriginName, "*")
	w.Header().Set("Access-Control-Allow-Methods", allowedCORSMethods)
	w.Header().Set("Access-Control-Allow-Headers", allowedCORSHeaders)
}

func detectContentType(p string) string {
	ext := strings.ToLower(filepath.Ext(p))
	switch ext {
	case ".wasm":
		return "application/wasm"
	case ".js":
		return "application/javascript"
	default:
		if mimeType := mime.TypeByExtension(ext); mimeType != "" {
			return mimeType
		}

		return "application/octet-stream"
	}
}

var variantPlatformAliases = map[string]v1.Platform{ //nolint:gochecknoglobals // canonical alias table is read-only data shared by every request handler
	"browser":      {OS: "wasi", Architecture: "wasm32"},
	"wasm32":       {OS: "wasi", Architecture: "wasm32"},
	"wasm32-web":   {OS: "wasi", Architecture: "wasm32"},
	"wasm32/wasi":  {OS: "wasi", Architecture: "wasm32"},
	"wasm32-wasi":  {OS: "wasi", Architecture: "wasm32"},
	"wasi":         {OS: "wasi", Architecture: "wasm32"},
	"wasi/wasm32":  {OS: "wasi", Architecture: "wasm32"},
	"wasi-wasm32":  {OS: "wasi", Architecture: "wasm32"},
	"wasm-browser": {OS: "wasi", Architecture: "wasm32"},
}

func resolveVariantPlatform(rawVariant string) (*v1.Platform, string, error) {
	clean := strings.TrimSpace(rawVariant)
	if clean == "" {
		return nil, "", nil
	}

	normalized := strings.ToLower(clean)
	if platform, ok := variantPlatformAliases[normalized]; ok {
		p := platform

		return &p, platformCacheKey(&p), nil
	}

	if !strings.Contains(normalized, "/") {
		return nil, "", fmt.Errorf("%w: %s", errUnsupportedVariant, rawVariant)
	}

	parts := strings.SplitN(normalized, "/", variantSplitParts)
	if len(parts) != variantSplitParts {
		return nil, "", fmt.Errorf("%w: %s", errInvalidVariantFormat, rawVariant)
	}

	archPart := strings.TrimSpace(parts[1])

	osPart := strings.TrimSpace(parts[0])
	if osPart == "" || archPart == "" {
		return nil, "", fmt.Errorf("%w: %s", errInvalidVariantFormat, rawVariant)
	}

	platform := v1.Platform{
		OS:           osPart,
		Architecture: normalizeArchitecture(archPart),
	}

	return &platform, platformCacheKey(&platform), nil
}

func normalizeArchitecture(arch string) string {
	switch arch {
	case "x86_64":
		return "amd64"
	case "aarch64":
		return "arm64"
	case "armv7":
		return "arm"
	default:
		return arch
	}
}

func platformCacheKey(p *v1.Platform) string {
	if p == nil {
		return ""
	}

	return fmt.Sprintf("%s/%s", p.OS, p.Architecture)
}

func normalizeArtifactPath(p string) string {
	// Use path.Clean to normalize the path (handles .. and .)
	cleaned := path.Clean(strings.TrimSpace(p))

	// If the path was absolute (started with /), Clean preserves it.
	// We want relative paths for tar extraction.
	cleaned = strings.TrimPrefix(cleaned, "/")

	// Also handle ./ prefix if it remains (though Clean usually handles it)
	cleaned = strings.TrimPrefix(cleaned, "./")

	if cleaned == "." || cleaned == "" {
		return ""
	}

	if strings.HasPrefix(cleaned, "..") {
		return ""
	}

	return cleaned
}

func extractFileFromImage(img v1.Image, filePath string) ([]byte, error) {
	target := normalizeArtifactPath(filePath)
	if target == "" {
		return nil, fmt.Errorf("%w: %s", errInvalidTargetPath, filePath)
	}

	klog.V(logVerboseLevel).Infof("[Registry] Extracting file from image: target=%s", target)

	layers, err := img.Layers()
	if err != nil {
		klog.Errorf("[Registry] Failed to get image layers: %v", err)

		return nil, err
	}

	klog.V(logVerboseLevel).Infof("[Registry] Image has %d layers", len(layers))

	deleted := make(map[string]struct{})

	// Iterate layers from top to bottom
	for i := len(layers) - 1; i >= 0; i-- {
		layer := layers[i]
		klog.V(logVerboseLevel).Infof("[Registry] Processing layer %d/%d", len(layers)-i, len(layers))

		rc, err := layer.Uncompressed()
		if err != nil {
			klog.Errorf("[Registry] Failed to uncompress layer %d: %v", i, err)

			return nil, err
		}

		data, err := extractFromLayer(rc, target, deleted)
		_ = rc.Close()

		if err == nil && data != nil {
			return data, nil
		}

		if err != nil && !errors.Is(err, errFileNotFound) {
			return nil, err
		}
	}

	return nil, fmt.Errorf("%w: %s", errArtifactNotFound, filePath)
}

func extractFromLayer(reader io.Reader, target string, deleted map[string]struct{}) ([]byte, error) {
	tr := tar.NewReader(reader)
	fileCount := 0

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}

		if err != nil {
			return nil, err
		}

		fileCount++
		headerPath := normalizeArtifactPath(header.Name)

		// Log first 10 files and any files that might be close to what we're looking for
		if fileCount <= 10 || strings.Contains(headerPath, "pkg") || strings.Contains(headerPath, "wasm") || strings.Contains(headerPath, ".js") {
			klog.V(logVerboseLevel).Infof("[Registry] Found file in tar: %q (normalized: %q, target: %q, match: %v)", header.Name, headerPath, target, headerPath == target)
		}

		if headerPath == "" {
			continue
		}

		if isWhiteout(headerPath) {
			deleted[whiteoutTarget(headerPath)] = struct{}{}

			continue
		}

		if _, ok := deleted[headerPath]; ok {
			continue
		}

		// Check for exact match
		if header.FileInfo().Mode().IsRegular() && headerPath == target {
			// Limit file size to avoid OOM
			const maxFileSize = 512 * 1024 * 1024 // 512MB
			if header.FileInfo().Size() > maxFileSize {
				return nil, fmt.Errorf("%w: %d bytes", errFileTooLarge, header.FileInfo().Size())
			}

			data, err := io.ReadAll(tr)
			if err != nil {
				return nil, err
			}

			return data, nil
		}
	}

	return nil, errFileNotFound
}

func isWhiteout(p string) bool {
	return strings.HasPrefix(path.Base(p), ".wh.")
}

func whiteoutTarget(p string) string {
	dir := path.Dir(p)
	base := path.Base(p)
	cleaned := strings.TrimPrefix(base, ".wh.")

	return normalizeArtifactPath(path.Join(dir, cleaned))
}

func (p *Proxy) fetchArtifactFromRegistry(
	ctx context.Context,
	ref string,
	artifactPath string,
	platform *v1.Platform,
) ([]byte, error) {
	klog.Infof("[Registry] Fetching artifact: ref=%s, path=%s, platform=%v", ref, artifactPath, platform)

	parsed, err := name.ParseReference(ref, name.WithDefaultTag("latest"))
	if err != nil {
		klog.Errorf("[Registry] Failed to parse reference %s: %v", ref, err)

		return nil, err
	}

	klog.Infof("[Registry] Parsed reference: %s", parsed.String())

	opts := []remote.Option{
		remote.WithContext(ctx),
		remote.WithUserAgent(registryUserAgent),
		remote.WithTransport(p.transport),
	}

	if platform != nil {
		klog.Infof("[Registry] Using platform: %s/%s", platform.OS, platform.Architecture)
		opts = append(opts, remote.WithPlatform(*platform))
	} else {
		klog.Infof("[Registry] No platform specified, using default")
	}

	klog.Infof("[Registry] Pulling image from registry: %s", parsed.String())

	image, err := p.imageFetcher(parsed, opts...)
	if err != nil {
		klog.Errorf("[Registry] Failed to pull image %s: %v", parsed.String(), err)

		return nil, err
	}

	klog.Infof("[Registry] Successfully pulled image, extracting file: %s", artifactPath)

	data, err := extractFileFromImage(image, artifactPath)
	if err != nil {
		klog.Errorf("[Registry] Failed to extract file %s from image: %v", artifactPath, err)

		return nil, err
	}

	klog.Infof("[Registry] Successfully extracted file %s (%d bytes)", artifactPath, len(data))

	return data, nil
}
