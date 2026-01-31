package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"k8s.io/klog/v2"
)

// HTTPServerInterface defines the common interface for all HTTP servers.
type HTTPServerInterface interface {
	Start(ctx context.Context) <-chan error
	Shutdown(ctx context.Context) error
}

// BaseHTTPServer provides common functionality for HTTP servers.
type BaseHTTPServer struct {
	server      *http.Server
	name        string
	port        int
	tlsCertFile string
	tlsKeyFile  string
}

const serverReadyDelay = 100 * time.Millisecond

// NewBaseHTTPServer creates a new BaseHTTPServer.
func NewBaseHTTPServer(name string, port int, handler http.Handler, readTimeout, writeTimeout time.Duration) *BaseHTTPServer {
	return &BaseHTTPServer{
		server: &http.Server{
			Addr:         fmt.Sprintf("0.0.0.0:%d", port),
			Handler:      handler,
			ReadTimeout:  readTimeout,
			WriteTimeout: writeTimeout,
		},
		name: name,
		port: port,
	}
}

// WithTLS configures TLS for the server.
func (s *BaseHTTPServer) WithTLS(certFile, keyFile string) *BaseHTTPServer {
	s.tlsCertFile = certFile
	s.tlsKeyFile = keyFile

	return s
}

// Handler returns the HTTP handler.
func (s *BaseHTTPServer) Handler() http.Handler {
	return s.server.Handler
}

// Start starts the server in a goroutine and returns an error channel.
func (s *BaseHTTPServer) Start(ctx context.Context) <-chan error {
	errChan := make(chan error, 1)

	go func() {
		klog.Infof("Starting %s on port %d", s.name, s.port)

		var listenCfg net.ListenConfig

		listener, err := listenCfg.Listen(ctx, "tcp4", fmt.Sprintf(":%d", s.port))
		if err != nil {
			klog.Errorf("Failed to bind %s on IPv4: %v", s.name, err)

			select {
			case errChan <- fmt.Errorf("%s failed: %w", s.name, err):
			default:
			}

			return
		}

		var serveErr error

		if s.tlsCertFile != "" && s.tlsKeyFile != "" {
			klog.Infof("Enabling TLS for %s", s.name)
			serveErr = s.server.ServeTLS(listener, s.tlsCertFile, s.tlsKeyFile)
		} else {
			serveErr = s.server.Serve(listener)
		}

		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			select {
			case errChan <- fmt.Errorf("%s failed: %w", s.name, serveErr):
			default:
				// Channel full, error already queued
			}
		}
	}()

	// Wait for server to start (simple delay for now, could be improved with readiness check)
	go func() {
		time.Sleep(serverReadyDelay)

		select {
		case <-errChan:
			// Server failed to start
		default:
			klog.Infof("%s is ready on port %d", s.name, s.port)
		}
	}()

	return errChan
}

// Shutdown gracefully shuts down the server.
func (s *BaseHTTPServer) Shutdown(ctx context.Context) error {
	klog.Infof("Shutting down %s...", s.name)

	return s.server.Shutdown(ctx)
}
