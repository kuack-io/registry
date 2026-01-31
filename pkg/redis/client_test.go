package redis_test

import (
	"context"
	"testing"
	"time"

	"kuack-registry/pkg/redis"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClient_Connection(t *testing.T) {
	t.Parallel()

	s := miniredis.RunT(t)

	// Test successful connection
	client, err := redis.NewClient(context.Background(), s.Addr(), "", 0)
	require.NoError(t, err)

	defer func() { _ = client.Close() }()

	// Test failed connection (wrong port)
	_, err = redis.NewClient(context.Background(), "localhost:12345", "", 0)
	require.Error(t, err)
}

func TestClient_SetAndGet(t *testing.T) {
	t.Parallel()

	s := miniredis.RunT(t)
	client, err := redis.NewClient(context.Background(), s.Addr(), "", 0)
	require.NoError(t, err)

	defer func() { _ = client.Close() }()

	ctx := context.Background()
	key := "test-key"
	value := []byte("test-value")

	// Set
	err = client.Set(ctx, key, value, time.Minute)
	require.NoError(t, err)

	// Get
	got, err := client.Get(ctx, key)
	require.NoError(t, err)
	assert.Equal(t, value, got)
}

func TestClient_Get_NotFound(t *testing.T) {
	t.Parallel()

	s := miniredis.RunT(t)
	client, err := redis.NewClient(context.Background(), s.Addr(), "", 0)
	require.NoError(t, err)

	defer func() { _ = client.Close() }()

	ctx := context.Background()

	// Get non-existent key
	got, err := client.Get(ctx, "non-existent")
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestClient_Set_Expiration(t *testing.T) {
	t.Parallel()

	s := miniredis.RunT(t)
	client, err := redis.NewClient(context.Background(), s.Addr(), "", 0)
	require.NoError(t, err)

	defer func() { _ = client.Close() }()

	ctx := context.Background()
	key := "test-expire"
	value := []byte("val")

	// Set with short expiration
	err = client.Set(ctx, key, value, time.Millisecond)
	require.NoError(t, err)

	// Verify exists
	got, err := client.Get(ctx, key)
	require.NoError(t, err)
	assert.Equal(t, value, got)

	s.FastForward(time.Second)

	// Verify expired
	got, err = client.Get(ctx, key)
	require.NoError(t, err)
	assert.Nil(t, got)
}
