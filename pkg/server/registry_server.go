package server

import (
	"net/http"
	"time"

	"kuack-registry/pkg/config"
	"kuack-registry/pkg/registry"

	"k8s.io/klog/v2"
)

// RegistryServer is the HTTP server for the registry.
type RegistryServer struct {
	*BaseHTTPServer
}

const (
	readTimeout  = 60 * time.Second // Long timeout for large downloads
	writeTimeout = 60 * time.Second
)

// NewRegistryServer creates a new registry server.
func NewRegistryServer(cfg *config.Config, proxy *registry.Proxy) *RegistryServer {
	// Middleware for logging and token check
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// Auth check if configured
		if cfg.RegistryToken != "" {
			token := r.URL.Query().Get("token")
			if token != cfg.RegistryToken {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)

				return
			}
		}

		// Route
		switch r.URL.Path {
		case "/registry":
			proxy.ServeHTTP(w, r)
		case "/resolve":
			proxy.ServeResolveHTTP(w, r)
		case "/healthz":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		default:
			http.NotFound(w, r)
		}

		duration := time.Since(start)
		klog.Infof("%s %s %s - %v", r.Method, r.URL.Path, r.RemoteAddr, duration)
	})

	baseServer := NewBaseHTTPServer("Registry Server", cfg.PublicPort, handler, readTimeout, writeTimeout)

	return &RegistryServer{
		BaseHTTPServer: baseServer,
	}
}
