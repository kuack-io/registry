package main

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/require"
)

const (
	envRedisAddr = "REDIS_ADDR"
	envPort      = "PORT"
)

func TestRun(t *testing.T) {
	t.Parallel()

	// Start miniredis
	s := miniredis.RunT(t)

	// Mock environment
	mockEnv := func(key string) string {
		switch key {
		case envRedisAddr:
			return s.Addr()
		case envPort:
			return "0" // Use random port to avoid conflicts
		}

		return ""
	}

	// Create context that cancels quickly to stop the server
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// Run application
	err := run(ctx, mockEnv)

	// We expect no error, just a clean shutdown after context cancellation
	require.NoError(t, err)
}

func TestRun_RedisError(t *testing.T) {
	t.Parallel()

	// Mock env pointing to invalid redis
	mockEnv := func(key string) string {
		if key == envRedisAddr {
			return "localhost:65535" // Invalid port usually
		}

		return ""
	}

	// Run application
	err := run(context.Background(), mockEnv)

	// Should fail connecting to redis
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to connect to Redis")
}

func TestRun_ServerBindError(t *testing.T) {
	t.Parallel()

	// 1. Occupy a port
	lc := net.ListenConfig{}
	l, err := lc.Listen(context.Background(), "tcp", "localhost:0")
	require.NoError(t, err)

	defer func() {
		_ = l.Close()
	}()

	addr, ok := l.Addr().(*net.TCPAddr)
	require.True(t, ok)

	port := addr.Port

	// 2. Start miniredis
	s := miniredis.RunT(t)

	// 3. Mock env to use that port
	mockEnv := func(key string) string {
		switch key {
		case envRedisAddr:
			return s.Addr()
		case envPort:
			return strconv.Itoa(port)
		}

		return ""
	}

	// 4. Run application
	// It should fail to bind to the port because we are holding it
	err = run(context.Background(), mockEnv)

	require.Error(t, err)
	require.Contains(t, err.Error(), "server failed")
	require.Contains(t, err.Error(), "bind")
}
