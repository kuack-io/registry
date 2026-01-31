package config_test

import (
	"testing"

	"kuack-registry/pkg/config"

	"github.com/stretchr/testify/assert"
)

func TestLoadConfig_Defaults(t *testing.T) {
	t.Parallel()

	// Mock EnvGetter that returns empty string (simulating missing env vars)
	mockEnv := func(key string) string {
		return ""
	}

	cfg := config.LoadConfig(mockEnv)

	assert.Equal(t, config.DefaultPublicPort, cfg.PublicPort)
	assert.Empty(t, cfg.RegistryToken)
	assert.Equal(t, config.DefaultKlogVerbosity, cfg.Verbosity)
	assert.Equal(t, config.DefaultRedisAddr, cfg.RedisAddr)
	assert.Empty(t, cfg.RedisPassword)
	assert.Equal(t, config.DefaultRedisDB, cfg.RedisDB)
}

func TestLoadConfig_CustomValues(t *testing.T) {
	t.Parallel()

	envVars := map[string]string{
		"PORT":           "9090",
		"REGISTRY_TOKEN": "secret-token",
		"KLOG_VERBOSITY": "5",
		"REDIS_ADDR":     "redis:6379",
		"REDIS_PASSWORD": "redis-pass",
		"REDIS_DB":       "1",
	}

	mockEnv := func(key string) string {
		return envVars[key]
	}

	cfg := config.LoadConfig(mockEnv)

	assert.Equal(t, 9090, cfg.PublicPort)
	assert.Equal(t, "secret-token", cfg.RegistryToken)
	assert.Equal(t, 5, cfg.Verbosity)
	assert.Equal(t, "redis:6379", cfg.RedisAddr)
	assert.Equal(t, "redis-pass", cfg.RedisPassword)
	assert.Equal(t, 1, cfg.RedisDB)
}

func TestLoadConfig_InvalidIntFallbacks(t *testing.T) {
	t.Parallel()

	envVars := map[string]string{
		"PORT": "invalid-port",
	}

	mockEnv := func(key string) string {
		return envVars[key]
	}

	cfg := config.LoadConfig(mockEnv)

	assert.Equal(t, config.DefaultPublicPort, cfg.PublicPort)
}

func TestInitializeKlog(t *testing.T) {
	t.Parallel()
	// this is hard to verify side effects of klog being initialized,
	// but we can at least ensure it doesn't panic.
	assert.NotPanics(t, func() {
		config.InitializeKlog(2)
	})
	assert.NotPanics(t, func() {
		config.InitializeKlog(-1) // Should handle out of range gracefully
	})
}
