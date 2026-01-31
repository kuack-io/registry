# Kuack Registry Service

The **Kuack Registry Service** is a stateless microservice that acts as an intelligent gateway between the Kuack Node and standard OCI Registries (like Docker Hub, GHCR, or ECR).

Its primary responsibility is to **resolve** and **extract** WebAssembly (WASM) artifacts from standard container images, enabling `kuack-node` to run them without needing to understand OCI layers or image formats itself.

## Why it exists?

In the original design, `kuack-node` handled image pulling and parsing directly. Extracting this logic into a dedicated service offers several advantages:

1.  **Decoupling**: `kuack-node` becomes simpler. It doesn't need to know how to parse tarballs, manifest schemas, or handle OCI authentication nuances. It just asks for a "config" and a "file".
2.  **Stateless Scalability**: The registry service is stateless and can be scaled independently of the nodes.
3.  **Caching**: By sitting between Nodes and OCI registries, this service leverages **Redis** to cache resolved metadata and extracted artifacts. This dramatically speeds up cold starts (resolving an image config takes milliseconds if cached).
4.  **Security**: The Node doesn't need direct internet access to every registry, it only needs to talk to this internal service.

## Architecture

```mermaid
graph LR
    Node[Kuack Node] -->|1. /resolve| Registry[Kuack Registry]
    Agent[Kuack Agent] -->|2. /registry (Ingress)| Registry
    Registry <-->|Cache| Redis[(Redis)]
    Registry <-->|Pull| OCI[(Docker/OCI Registry)]
```

The service is designed to be **stateless**. All persistent state (cached configs and artifacts) is stored in Redis. This allows you to restart or scale the registry service without losing the "hot" cache.

## How it Works

The interaction between `kuack-node` and `kuack-registry` follows a strict **Two-Step Flow**.

### Step 1: Discovery (`/resolve`)

First, the Node asks the Registry to "inspect" an image.
> "I have this image ref `kuack-io/wasi-example:latest`. What is it, and how do I run it?"

The Registry:
1.  Checks Redis cache.
2.  If missing, pulls the image Manifest and Config from the OCI registry.
3.  Scans the layers to find the executable WASM file (e.g., `app.wasm` or `plugin.wasm`).
4.  Determines the execution type (`wasi` or `bindgen`) and architecture.
5.  Returns a **JSON Configuration** object containing the environment variables, entrypoint, and crucially, the **path** to the artifact.

#### Why separate this?

We separate discovery because we don't know *what* file to download yet. A WASM container isn't a standardized single binary, it's a filesystem. We need to find the binary and negotiate the execution environment (env vars, args) before we start streaming bytes.

### Step 2: Fetching (`/registry`)

Once the Node knows the path (e.g., `/app/main.wasm`), it passes this information to the **Agent**. The Agent then requests the file.
> "Give me the file at `/app/main.wasm` from image `kuack-io/wasi-example:latest`."

The Agent sends this request to the **Ingress Controller**, which routes `/registry` traffic directly to the `kuack-registry` service. This bypasses the Node entirely for artifact downloads.

The Registry:
1.  Checks Redis cache for this specific artifact.
2.  If missing, pulls only the specific layer containing that file.
3.  Extracts the file on-the-fly.
4.  Caches the binary in Redis.
5.  Streams the binary to the **Agent**.

## API Reference

### `GET /resolve`
Resolves the configuration for a given WASM image.

**Parameters:**
- `image`: The OCI image reference (e.g., `kuack-io/my-app:v1`).

**Response (200 OK):**
```json
{
  "type": "wasi",
  "path": "/app.wasm",
  "variant": "wasm32/wasi",
  "env": ["KEY=VALUE"],
  "entrypoint": ["/app.wasm"],
  "cmd": ["--help"]
}
```

### `GET /registry`
Downloads a specific artifact from an image.

**Parameters:**
- `image`: The OCI image reference.
- `path`: The absolute path to the file inside the container (returned by `/resolve`).
- `variant`: (Optional) Architecture variant (default: `wasm32/wasi`).

**Response:**
- Returns the binary file stream with `Content-Type: application/wasm` (or octet-stream).

## Configuration

The service is configured via environment variables:

| Variable | Description | Default |
|----------|-------------|---------|
| `PORT` | HTTP Server Port | `8080` |
| `REDIS_ADDR` | Redis address | `localhost:6379` |
| `REDIS_PASSWORD` | Redis password | *(empty)* |
| `REDIS_DB` | Redis DB index | `0` |
| `KLOG_VERBOSITY` | Log level (0-10) | `2` |
