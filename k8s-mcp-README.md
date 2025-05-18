# GitHub MCP Server for Kubernetes (Single Container)

This document outlines how to deploy the GitHub MCP Server and its HTTP/SSE adapter as a single container within a Pod in a Kubernetes cluster.

## Prerequisites

1.  A Kubernetes cluster.
2.  `docker` installed locally for building the container image.
3.  A container registry (e.g., Docker Hub, GHCR) to push your image.
4.  A GitHub Personal Access Token with appropriate permissions.
5.  `kubectl` configured to interact with your cluster.

## Architecture

The deployment consists of:

*   **Unified Container**: A single container running the `mcp-adapter` as its main process. The adapter, in turn, starts and manages the `github-mcp-server` as a subprocess. The `github-mcp-server` binary is included in the same container.
*   **Kubernetes Deployment**: Manages the Pod containing the unified service.
*   **Kubernetes Service**: Exposes the adapter's port 8080 to other services within the cluster on port 80.
*   **Kubernetes Secret**: Stores the GitHub Personal Access Token. The adapter is configured to pass this token to the `github-mcp-server` subprocess.

## Setup Instructions

### 1. Build and Push the Unified Docker Image

Use the main `Dockerfile` in the project root. This Dockerfile builds both the `github-mcp-server` and `mcp-adapter` binaries and packages them into a single image, with the adapter as the entrypoint.

Build and push this image:

```bash
export YOUR_REGISTRY=<your-container-registry> # e.g., docker.io/yourusername, ghcr.io/yourusername
export IMAGE_NAME=github-mcp-service # Or your preferred image name
docker build -f Dockerfile -t $YOUR_REGISTRY/$IMAGE_NAME:latest .
docker push $YOUR_REGISTRY/$IMAGE_NAME:latest
```

### 2. Prepare Kubernetes Manifest (`github-mcp-k8s.yaml`)

Update `github-mcp-k8s.yaml` with the following content. 
**Important:**
*   Replace `${YOUR_REGISTRY}/${IMAGE_NAME}:latest` with your actual image path from step 1.
*   Generate a base64 encoded version of your GitHub Personal Access Token: `echo -n "your-actual-github-token" | base64`
*   Replace the placeholder in the `Secret` data (`github-token`) with your base64 encoded token.

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: github-mcp-secrets
type: Opaque
data:
  github-token: "YOUR_BASE64_ENCODED_GITHUB_TOKEN"
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: github-mcp-server
  labels:
    app: github-mcp-server
spec:
  replicas: 1
  selector:
    matchLabels:
      app: github-mcp-server
  template:
    metadata:
      labels:
        app: github-mcp-server
    spec:
      containers:
      - name: github-mcp-service
        image: ${YOUR_REGISTRY}/${IMAGE_NAME}:latest # Use your unified image from Dockerfile
        ports:
        - containerPort: 8080
        readinessProbe:
          httpGet:
            path: /health
            port: 8080
          initialDelaySeconds: 10
          periodSeconds: 10
        livenessProbe:
          httpGet:
            path: /health
            port: 8080
          initialDelaySeconds: 20
          periodSeconds: 20
        resources:
          requests:
            cpu: "200m"
            memory: "256Mi"
          limits:
            cpu: "1000m"
            memory: "512Mi"
        env:
        - name: GITHUB_TOKEN # This is read by the adapter and passed as MCP_ENV_GITHUB_TOKEN
          valueFrom:
            secretKeyRef:
              name: github-mcp-secrets
              key: github-token
        - name: MCP_COMMAND
          value: "/usr/local/bin/github-mcp-server"
        - name: MCP_ARGS
          value: "stdio"
        - name: MCP_ENV_GITHUB_TOKEN # Adapter will use GITHUB_TOKEN to populate this for the child process
          valueFrom:
            secretKeyRef:
              name: github-mcp-secrets
              key: github-token
# Removed VolumeMounts and Volumes for adapter-config.json as it's no longer used.
---
apiVersion: v1
kind: Service
metadata:
  name: github-mcp-server
spec:
  selector:
    app: github-mcp-server
  ports:
  - name: http
    protocol: TCP
    port: 80
    targetPort: 8080
  type: ClusterIP
```

### 3. Deploy to Kubernetes

Apply the manifest:
```bash
kubectl apply -f github-mcp-k8s.yaml
```

Verify:
```bash
kubectl get pods -l app=github-mcp-server
kubectl get svc github-mcp-server
```

## Usage

Access within the cluster at:
*   JSON-RPC: `http://github-mcp-server/mcp` (POST)
*   SSE: `http://github-mcp-server/mcp` (GET)

## Troubleshooting

*   **Check Pod Logs**:
    ```bash
    POD_NAME=$(kubectl get pods -l app=github-mcp-server -o jsonpath='{.items[0].metadata.name}')
    kubectl logs $POD_NAME
    ```
*   Ensure the image specified in the Deployment exists in your registry and is accessible by Kubernetes.
*   Verify the GitHub token in the `github-mcp-secrets` Secret is correct and base64 encoded.
*   Check the adapter logs for messages about the `MCP_COMMAND`, `MCP_ARGS`, and environment variables.

## Deprecated Dockerfiles

Previous versions might have used other Dockerfile variations. The current recommended approach for Kubernetes uses the root `Dockerfile` to build a single, self-contained image. 