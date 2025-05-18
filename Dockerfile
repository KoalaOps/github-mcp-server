# Unified Dockerfile for GitHub MCP Server and Adapter

# Stage 1: Build the github-mcp-server
FROM golang:1.24-alpine AS server-builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/github-mcp-server/main.go ./cmd/github-mcp-server/
COPY internal ./internal
COPY pkg ./pkg
# Ensure all necessary source code for the server is copied above
RUN CGO_ENABLED=0 GOOS=linux go build -o /github-mcp-server ./cmd/github-mcp-server

# Stage 2: Build the adapter
FROM golang:1.24-alpine AS adapter-builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/adapter/main.go ./cmd/adapter/
# Ensure all necessary source code for the adapter is copied above
RUN CGO_ENABLED=0 GOOS=linux go build -o /adapter ./cmd/adapter/main.go

# Stage 3: Create the runtime image
FROM alpine:latest

# Add any necessary runtime dependencies. 
# For now, none are added beyond what alpine base provides, 
# as the adapter runs the server directly and doesn't need nc/socat for this setup.
# RUN apk add --no-cache <any-runtime-packages>

# Copy the github-mcp-server binary from the server-builder stage
COPY --from=server-builder /github-mcp-server /usr/local/bin/github-mcp-server

# Copy the adapter binary from the adapter-builder stage
COPY --from=adapter-builder /adapter /adapter

# Set the adapter as the entrypoint
ENTRYPOINT ["/adapter"]

# The adapter will be configured via environment variables by the Kubernetes manifest:
# MCP_COMMAND="/usr/local/bin/github-mcp-server"
# MCP_ARGS="stdio"
# MCP_ENV_GITHUB_TOKEN will be set from a K8s secret via GITHUB_TOKEN env var in the pod spec. 