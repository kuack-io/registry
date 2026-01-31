package registry

import (
	"archive/tar"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"k8s.io/klog/v2"
)

const configCacheTTL = 10 * time.Minute

// WasmConfig describes how to execute a WASM image.
type WasmConfig struct {
	Type       string   `json:"type"` // "bindgen" or "wasi"
	Path       string   `json:"path"`
	Variant    string   `json:"variant"` // defaults to "wasm32/wasi"
	Env        []string `json:"env,omitempty"`
	Entrypoint []string `json:"entrypoint,omitempty"` // Image's default entrypoint
	Cmd        []string `json:"cmd,omitempty"`        // Image's default command
}

// ServeResolveHTTP serves the resolve endpoint.
func (p *Proxy) ServeResolveHTTP(w http.ResponseWriter, r *http.Request) {
	imageRef := r.URL.Query().Get("image")
	if imageRef == "" {
		http.Error(w, "missing image query parameter", http.StatusBadRequest)

		return
	}

	config, err := p.ResolveWasmConfig(r.Context(), imageRef)
	if err != nil {
		klog.Errorf("Failed to resolve WASM config for %s: %v", imageRef, err)
		http.Error(w, fmt.Sprintf("failed to resolve config: %v", err), http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", "application/json")

	err = json.NewEncoder(w).Encode(config)
	if err != nil {
		klog.Errorf("Failed to encode config response: %v", err)
	}
}

// ResolveWasmConfig inspects the image to determine the WASM configuration.
func (p *Proxy) ResolveWasmConfig(ctx context.Context, imageRef string) (*WasmConfig, error) {
	// Check Cache
	key := "config::" + imageRef
	if p.cache != nil {
		data, err := p.cache.Get(ctx, key)
		if err == nil && data != nil {
			var config WasmConfig

			err := json.Unmarshal(data, &config)
			if err == nil {
				klog.V(logVerboseLevel).Infof("[Registry] Config cache hit for: %s", key)

				return &config, nil
			}
		}
	}

	klog.Infof("[Registry] Resolving WASM config for: %s", imageRef)

	ref, err := name.ParseReference(imageRef, name.WithDefaultTag("latest"))
	if err != nil {
		return nil, fmt.Errorf("failed to parse reference: %w", err)
	}

	// Fetch image metadata (manifest)
	opts := []remote.Option{
		remote.WithContext(ctx),
		remote.WithUserAgent(registryUserAgent),
		remote.WithPlatform(v1.Platform{OS: "wasi", Architecture: "wasm32"}), // Prefer WASM variant
		remote.WithTransport(p.transport),
	}

	img, err := p.imageFetcher(ref, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch image metadata: %w", err)
	}

	// Fetch image config (env vars, entrypoint, etc.)
	configFile, err := img.ConfigFile()
	if err != nil {
		klog.Warningf("[Registry] Failed to fetch config file for %s: %v", imageRef, err)
	}

	var (
		imageEnv        []string
		imageEntrypoint []string
		imageCmd        []string
	)

	if configFile != nil {
		imageEnv = configFile.Config.Env
		imageEntrypoint = configFile.Config.Entrypoint
		imageCmd = configFile.Config.Cmd

		klog.V(logVerboseLevel).Infof("[Registry] Image config for %s: Entrypoint=%v, Cmd=%v", imageRef, imageEntrypoint, imageCmd)
	}

	// Check for common container2wasm / standalone WASM files
	config, err := p.inspectImageContent(img, imageRef, imageEnv, imageEntrypoint, imageCmd)
	if err != nil {
		return nil, err
	}

	// Cache result
	if p.cache != nil {
		data, err := json.Marshal(config)
		if err == nil {
			// Cache for 10 minutes (configs might change more often than blobs, or maybe not)
			_ = p.cache.Set(ctx, key, data, configCacheTTL)
		}
	}

	return config, nil
}

func (p *Proxy) inspectImageContent(img v1.Image, imageRef string, env, entrypoint, cmd []string) (*WasmConfig, error) {
	layers, err := img.Layers()
	if err != nil {
		return nil, err
	}

	// Default fallback
	bestGuess := &WasmConfig{
		Type:       "wasi",
		Path:       "/output.wasm", // Standard c2w output
		Variant:    "wasm32/wasi",
		Env:        env,
		Entrypoint: entrypoint,
		Cmd:        cmd,
	}

	// Derive name from image ref for fallback detection (e.g. "checker" from "kuack/checker")
	imageName := "unknown"
	if parts := strings.Split(imageRef, "/"); len(parts) > 0 {
		imageName = parts[len(parts)-1]
		if tagIdx := strings.Index(imageName, ":"); tagIdx != -1 {
			imageName = imageName[:tagIdx]
		}
	}

	derivedWasm := fmt.Sprintf("/%s.wasm", imageName)

	// Iterate layers from top to bottom
	for i := len(layers) - 1; i >= 0; i-- {
		layer := layers[i]

		rc, err := layer.Uncompressed()
		if err != nil {
			return nil, err
		}

		// Wrap in closure to defer close inside loop
		stop, err := func() (bool, error) {
			defer func() { _ = rc.Close() }()

			tr := tar.NewReader(rc)
			for {
				header, err := tr.Next()
				if err == io.EOF {
					break
				}

				if err != nil {
					return false, err
				}

				// Normalize path
				cleanPath := normalizeArtifactPath(header.Name)

				// Check for pkg/package.json
				if cleanPath == "pkg/package.json" {
					// Parse it to find the wasm path
					var pkg struct {
						Main string `json:"main"`
					}

					err := json.NewDecoder(tr).Decode(&pkg)
					if err == nil && pkg.Main != "" {
						// Found bindgen!
						wasmFile := strings.Replace(pkg.Main, ".js", "_bg.wasm", 1)

						*bestGuess = WasmConfig{
							Type:    "bindgen",
							Path:    path.Join("pkg", wasmFile),
							Variant: "wasm32/wasi",
							Env:     env,
						}

						return true, nil // Found definitive match
					}
				}

				// Check for WASI candidates
				if strings.HasSuffix(cleanPath, ".wasm") {
					if cleanPath == "output.wasm" || cleanPath == "out.wasm" || cleanPath == "main.wasm" || "/"+cleanPath == derivedWasm {
						bestGuess.Path = "/" + cleanPath
						bestGuess.Type = "wasi"
						// Keep scanning
					}
				}
			}

			return false, nil
		}()
		if err != nil {
			return nil, err
		}

		if stop {
			break
		}
	}

	klog.Infof("[Registry] Fallback detection for %s: %+v", imageRef, bestGuess)

	return bestGuess, nil
}
