# Running GitHub MCP Server Locally with Docker Compose

This guide explains how to set up and run the GitHub MCP server and its HTTP/SSE adapter using Docker Compose. This setup involves two services: one for the MCP server itself (exposing its stdio over a TCP socket) and one for the adapter that bridges HTTP/SSE to this TCP socket.

## Prerequisites

1. Docker and Docker Compose installed
2. A GitHub Personal Access Token with appropriate permissions (e.g., `repo`, `user`)

## Setup and Run

1. **Set up your GitHub token:**

   Create a `.env` file in the root of the project (if it doesn't exist):
   ```bash
   # .env file content
   GITHUB_TOKEN=your_actual_github_personal_access_token
   ```
   Replace `your_actual_github_personal_access_token` with your real token. This token will be passed to the `mcp-adapter` service, which then provides it to the MCP server process.

2. **Understand the Docker Compose Configuration:**

   The `docker-compose.yml` file defines two main services:
   *   `mcp-server`:
       *   Built using `Dockerfile.mcp-server-compose`.
       *   This service compiles the `github-mcp-server` from source.
       *   It uses `socat` to expose the server's stdio interface on TCP port 9000 within the Docker network. This service is not directly exposed to your host machine.
   *   `mcp-adapter`:
       *   Built using `Dockerfile.adapter`.
       *   This service compiles the Go-based HTTP/SSE adapter.
       *   It listens on `localhost:8080`.
       *   It's configured via environment variables (set in `docker-compose.yml`) to connect to the `mcp-server` service:
           *   `MCP_COMMAND=nc` (netcat, available in the adapter's image)
           *   `MCP_ARGS=mcp-server,9000` (telling nc to connect to `mcp-server` on port 9000)
           *   `MCP_ENV_GITHUB_TOKEN=${GITHUB_TOKEN}` (passes the token from your `.env` file to the adapter, which then sets it for the `nc` process, effectively making it available to the underlying MCP server via the socat relay).

3. **Build and Start the Services using Docker Compose:**

   ```bash
   docker-compose up --build -d
   ```
   This command will:
   *   Build the `mcp-server` and `mcp-adapter` images.
   *   Start both services.

4. **Check that the services are running:**

   ```bash
   docker-compose ps
   ```
   You should see both `mcp-server` and `mcp-adapter` running.

## Using the HTTP/SSE Endpoint

The GitHub MCP service is now accessible via the `mcp-adapter` at `http://localhost:8080/mcp`.

*   **JSON-RPC (POST requests):** For sending commands to the MCP server.
*   **SSE (GET requests):** For establishing a Server-Sent Events stream (e.g., for Cursor compatibility).

### Testing the Endpoint

You can use the provided test scripts or `curl`.

**JSON-RPC with `test-mcp.sh`:**
```bash
./test-mcp.sh initialize
./test-mcp.sh tools/call '{"name": "get_me"}'
# Example: list repositories (replace with your username)
# ./test-mcp.sh tools/call '{"name": "search_repositories", "input": {"query": "user:YOUR_USERNAME"}}'
```

**JSON-RPC with `curl`:**
```bash
# Initialize
curl -X POST http://localhost:8080/mcp \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}'

# Get current user
curl -X POST http://localhost:8080/mcp \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"get_me"}}'
```

**SSE with `test-sse.sh` or `test-sse.html`:**
```bash
./test-sse.sh
# Or open test-sse.html in your browser
```

### Health Check
Check the health of the adapter (which implies the MCP server process it manages is also running and responsive):
```bash
curl http://localhost:8080/health
```

## Stopping the Services

When you're done, stop the services:
```bash
docker-compose down
```

## Troubleshooting

1.  **Check the logs:**
    *   For the adapter: `docker-compose logs mcp-adapter`
    *   For the MCP server itself (via socat): `docker-compose logs mcp-server`
2.  **Verify your GitHub token:** Ensure it's correctly set in `.env` and has the necessary permissions.
3.  **Ensure services are up:** `docker-compose ps`
4.  **Rebuild and restart:**
    ```bash
    docker-compose down
    docker-compose up --build -d
    ```

## Dockerfile Structure

The local setup uses two main Dockerfiles:
*   `Dockerfile.mcp-server-compose`: Builds the `github-mcp-server` binary from the current source code and uses `socat` to expose its `stdio` interface over TCP port 9000.
*   `Dockerfile.adapter`: Builds the Go-based HTTP/SSE adapter (`cmd/adapter/main.go`). This adapter includes `netcat-openbsd` to connect to the `mcp-server`'s TCP socket.

The `mcp-adapter` is the entrypoint for HTTP/SSE requests from your host machine. It then communicates with the `mcp-server` over the internal Docker network. 