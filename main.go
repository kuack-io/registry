package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"kuack-registry/pkg/config"
	"kuack-registry/pkg/redis"
	"kuack-registry/pkg/registry"
	"kuack-registry/pkg/server"

	"k8s.io/klog/v2"
)

func main() {
	err := run(context.Background(), os.Getenv)
	if err != nil {
		klog.Fatal(err)
	}
}

func run(ctx context.Context, getEnv config.EnvGetter) error {
	// Load config
	cfg := config.LoadConfig(getEnv)
	config.InitializeKlog(cfg.Verbosity)

	klog.Info("Starting Kuack Registry Service")

	// Initialize Redis
	klog.Infof("Connecting to Redis at %s", cfg.RedisAddr)

	redisClient, err := redis.NewClient(ctx, cfg.RedisAddr, cfg.RedisPassword, cfg.RedisDB)
	if err != nil {
		return fmt.Errorf("failed to connect to Redis: %w", err)
	}

	defer func() {
		_ = redisClient.Close()
	}()

	klog.Info("Connected to Redis")

	// Initialize Registry Proxy
	proxy := registry.NewProxy(redisClient)

	// Initialize Server
	srv := server.NewRegistryServer(cfg, proxy)

	// Start Server
	srvErrChan := srv.Start(ctx)

	// Wait for shutdown signal or server error
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-srvErrChan:
		if err != nil {
			return fmt.Errorf("server failed: %w", err)
		}
	case <-sigChan:
		klog.Info("Shutdown signal received")
	case <-ctx.Done():
		klog.Info("Context cancelled")
	}

	// Graceful shutdown
	shutdownCtx, cancel := context.WithTimeout(context.Background(), config.ShutdownTimeout)
	defer cancel()

	err = srv.Shutdown(shutdownCtx) //nolint:contextcheck // Shutdown requires separate timeout context
	if err != nil {
		return fmt.Errorf("error during shutdown: %w", err)
	}

	klog.Info("Registry service stopped")

	return nil
}
