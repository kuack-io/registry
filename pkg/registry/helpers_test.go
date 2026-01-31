package registry_test

import (
	"archive/tar"
	"bytes"
	"context"
	"testing"

	"kuack-registry/pkg/redis"
	"kuack-registry/pkg/registry"

	"github.com/alicebob/miniredis/v2"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/static"
	"github.com/google/go-containerregistry/pkg/v1/types"
	"github.com/stretchr/testify/require"
)

//nolint:ireturn // Helper function returns interface by design
func createLayer(t *testing.T, files map[string][]byte) v1.Layer {
	t.Helper()

	var buf bytes.Buffer

	tw := tar.NewWriter(&buf)

	for name, content := range files {
		err := tw.WriteHeader(&tar.Header{
			Name: name,
			Mode: 0600,
			Size: int64(len(content)),
		})
		require.NoError(t, err)
		_, err = tw.Write(content)
		require.NoError(t, err)
	}

	require.NoError(t, tw.Close())

	return static.NewLayer(buf.Bytes(), types.DockerLayer)
}

func setupTestProxy(t *testing.T) *registry.Proxy {
	t.Helper()

	s := miniredis.RunT(t)
	client, err := redis.NewClient(context.Background(), s.Addr(), "", 0)
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	return registry.NewProxy(client)
}
