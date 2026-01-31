package redis

import (
	"context"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
)

// Client wraps the Redis client.
type Client struct {
	rdb *redis.Client
}

// NewClient creates a new Redis client.
func NewClient(ctx context.Context, addr, password string, db int) (*Client, error) {
	rdb := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: password,
		DB:       db,
	})

	// Test connection
	const connectTimeout = 5 * time.Second

	ctx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()

	err := rdb.Ping(ctx).Err()
	if err != nil {
		return nil, err
	}

	return &Client{rdb: rdb}, nil
}

// Get retrieves a value from Redis.
func (c *Client) Get(ctx context.Context, key string) ([]byte, error) {
	val, err := c.rdb.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, nil // Not found
	}

	return val, err
}

// Set stores a value in Redis with an optional expiration.
func (c *Client) Set(ctx context.Context, key string, value []byte, expiration time.Duration) error {
	return c.rdb.Set(ctx, key, value, expiration).Err()
}

// Close closes the Redis connection.
func (c *Client) Close() error {
	return c.rdb.Close()
}
