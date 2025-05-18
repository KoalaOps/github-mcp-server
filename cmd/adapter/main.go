package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// JSONRPC represents a JSON-RPC message
type JSONRPC struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      interface{} `json:"id"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params"`
}

func main() {
	// Load configuration from environment variables
	mcpCommand := os.Getenv("MCP_COMMAND")
	if mcpCommand == "" {
		log.Fatalf("MCP_COMMAND environment variable not set")
	}

	mcpArgsString := os.Getenv("MCP_ARGS")
	var mcpArgs []string
	if mcpArgsString != "" {
		mcpArgs = strings.Split(mcpArgsString, ",")
	}

	mcpEnv := make(map[string]string)
	for _, e := range os.Environ() {
		pair := strings.SplitN(e, "=", 2)
		if strings.HasPrefix(pair[0], "MCP_ENV_") {
			envKey := strings.TrimPrefix(pair[0], "MCP_ENV_")
			if len(pair) > 1 {
				mcpEnv[envKey] = pair[1]
			} else {
				mcpEnv[envKey] = "" // Handle case where env var is set but has no value
			}
		}
	}
	log.Printf("Loaded MCP configuration from environment variables: command='%s', args='%v', env_keys_to_pass='%v'", mcpCommand, mcpArgs, mapsKeys(mcpEnv))

	// Start the MCP server process
	cmd := exec.Command(mcpCommand, mcpArgs...)

	// Set environment variables
	cmdEnv := os.Environ() // Start with current environment
	// Filter out MCP_COMMAND, MCP_ARGS, and MCP_ENV_XXX from being directly passed to child
	// (unless they are also in mcpEnv, in which case they'll be set correctly below)
	var filteredCmdEnv []string
	for _, e := range cmdEnv {
		if !strings.HasPrefix(e, "MCP_COMMAND=") && !strings.HasPrefix(e, "MCP_ARGS=") && !strings.HasPrefix(e, "MCP_ENV_") {
			filteredCmdEnv = append(filteredCmdEnv, e)
		}
	}
	cmdEnv = filteredCmdEnv

	for key, value := range mcpEnv {
		// If value was an env var itself (e.g., MCP_ENV_MYVAR=${OTHER_VAR}), resolve it
		if strings.HasPrefix(value, "${") && strings.HasSuffix(value, "}") {
			envVarName := strings.TrimSuffix(strings.TrimPrefix(value, "${"), "}")
			value = os.Getenv(envVarName) // Resolve from adapter's environment
		}
		cmdEnv = append(cmdEnv, key+"="+value)
	}
	cmd.Env = cmdEnv

	// Set up stdin/stdout pipes
	stdin, err := cmd.StdinPipe()
	if err != nil {
		log.Fatalf("Failed to create stdin pipe: %v", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		log.Fatalf("Failed to create stdout pipe: %v", err)
	}

	// Start the MCP server
	if err := cmd.Start(); err != nil {
		log.Fatalf("Failed to start MCP server: %v", err)
	}

	// Track if the MCP server is healthy
	var (
		healthMutex  sync.RWMutex
		lastActivity time.Time = time.Now()
		isRunning    bool      = true
	)

	// Channel for responses from MCP server
	responses := make(chan []byte, 10) // Buffer to avoid blocking

	// Read responses from MCP server using a buffered reader for line-delimited JSON
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		reader := bufio.NewReader(stdout)
		for {
			line, err := reader.ReadBytes('\n')
			if err != nil {
				if err != io.EOF {
					log.Printf("Error reading from MCP server stdout: %v", err)
				} else {
					log.Printf("MCP server stdout closed (EOF)")
				}

				// Update health status
				healthMutex.Lock()
				isRunning = false
				healthMutex.Unlock()

				close(responses)
				// If the MCP server exits, we should exit too so Kubernetes can restart the pod
				log.Printf("MCP server process ended, exiting adapter")
				os.Exit(1)
				break
			}

			// Update last activity timestamp
			healthMutex.Lock()
			lastActivity = time.Now()
			healthMutex.Unlock()

			// Trim any whitespace (including the newline)
			trimmedLine := bytes.TrimSpace(line)
			if len(trimmedLine) > 0 {
				log.Printf("[MCP STDOUT READER] Received line from MCP: %s", string(trimmedLine))
				// Send the complete JSON message
				responseCopy := make([]byte, len(trimmedLine))
				copy(responseCopy, trimmedLine)

				// Non-blocking send to allow MCP server to continue processing if channel is full
				// This might happen if handleJSONRPC is slow or if MCP sends multiple rapid responses.
				select {
				case responses <- responseCopy:
					log.Printf("[MCP STDOUT READER] Sent response to responses channel.")
				default:
					log.Printf("[MCP STDOUT READER] Responses channel full or no receiver. Discarding MCP message: %s", string(trimmedLine))
				}
			}
		}
	}()

	// Health check endpoint
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		healthMutex.RLock()
		defer healthMutex.RUnlock()

		if !isRunning {
			http.Error(w, "MCP server not running", http.StatusServiceUnavailable)
			return
		}

		// Check if we have seen activity within the last minute
		// This helps detect if the MCP server is still responsive
		if time.Since(lastActivity) > time.Minute {
			http.Error(w, "MCP server inactive", http.StatusServiceUnavailable)
			return
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	// HTTP server to handle JSON-RPC POST requests
	http.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		// Support both POST (JSON-RPC) and GET (SSE) methods
		if r.Method == http.MethodPost {
			// Handle JSON-RPC over HTTP POST
			handleJSONRPC(w, r, stdin, responses, &lastActivity, &healthMutex)
		} else if r.Method == http.MethodGet {
			// Handle Server-Sent Events (SSE)
			handleSSE(w, r, stdin, responses, &lastActivity, &healthMutex)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// Start HTTP server
	log.Println("Starting HTTP server on :8080")
	if err := http.ListenAndServe(":8080", nil); err != nil {
		log.Fatalf("HTTP server failed: %v", err)
	}

	// Wait for reader goroutine to finish
	wg.Wait()

	// Clean up
	if err := cmd.Wait(); err != nil {
		log.Printf("MCP server exited with error: %v", err)
	}
}

// Helper function to get keys from a map for logging
func mapsKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// handleJSONRPC processes JSON-RPC requests over HTTP POST
func handleJSONRPC(w http.ResponseWriter, r *http.Request, stdin io.Writer, responses <-chan []byte, lastActivity *time.Time, healthMutex *sync.RWMutex) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}

	log.Printf("[JSON-RPC HANDLER] Received request: %s", string(body))

	// Attempt to parse the request to check if it's a notification
	var rpcMessage JSONRPC
	isNotification := false
	if err := json.Unmarshal(body, &rpcMessage); err == nil {
		if rpcMessage.ID == nil { // Common way to identify notifications (no ID or null ID)
			isNotification = true
		}
	} else {
		log.Printf("[JSON-RPC HANDLER] Warning: Could not parse JSON-RPC request to check for ID: %v", err)
		// Proceed assuming it might expect a response, to be safe, or handle as error?
		// For now, let's assume it might expect a response if parsing fails.
	}

	// Update activity time when receiving a request
	healthMutex.Lock()
	*lastActivity = time.Now()
	healthMutex.Unlock()

	// Forward the request to the MCP server
	// Ensure we append a newline to maintain the line-delimited protocol
	if _, err := stdin.Write(append(body, '\n')); err != nil {
		log.Printf("[JSON-RPC HANDLER] Error writing to MCP stdin: %v", err)
		http.Error(w, "Failed to write to MCP server", http.StatusInternalServerError)
		return
	}
	log.Printf("[JSON-RPC HANDLER] Forwarded request to MCP server.")

	// If it's a notification, we don't wait for a response from the MCP server.
	// We can send an immediate HTTP 204 No Content.
	if isNotification {
		log.Printf("[JSON-RPC HANDLER] Request is a notification. Responding with HTTP 204 No Content.")
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// Wait for response from MCP server for non-notification requests
	// Adding a timeout to prevent indefinite blocking
	select {
	case response, ok := <-responses:
		if !ok {
			log.Printf("[JSON-RPC HANDLER] MCP server closed the connection (responses channel closed).")
			http.Error(w, "MCP server closed the connection", http.StatusInternalServerError)
			return
		}
		log.Printf("[JSON-RPC HANDLER] Received response from MCP via channel: %s", string(response))
		w.Header().Set("Content-Type", "application/json")
		w.Write(response)
		log.Printf("[JSON-RPC HANDLER] Sent response to client.")
	case <-time.After(10 * time.Second): // 10-second timeout
		log.Printf("[JSON-RPC HANDLER] Timeout waiting for response from MCP server for request: %s", string(body))
		http.Error(w, "Request to MCP server timed out", http.StatusGatewayTimeout)
	}
}

// handleSSE processes Server-Sent Events (SSE) requests
func handleSSE(w http.ResponseWriter, r *http.Request, stdin io.Writer, responses <-chan []byte, lastActivity *time.Time, healthMutex *sync.RWMutex) {
	// Set headers for SSE
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported!", http.StatusInternalServerError)
		log.Println("[SSE] Streaming not supported by the response writer")
		return
	}
	flusher.Flush() // Send headers immediately

	log.Println("[SSE] Connection established. Sending initial messages.")

	// Goroutine to periodically update lastActivity for health checks while SSE is active.
	activityCtx, cancelActivityUpdate := context.WithCancel(r.Context())
	defer cancelActivityUpdate()

	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-activityCtx.Done():
				return
			case <-ticker.C:
				healthMutex.Lock()
				*lastActivity = time.Now()
				healthMutex.Unlock()
			}
		}
	}()

	// Helper function to send a request to MCP and forward its response via SSE
	sendSseRequestAndForwardResponse := func(requestId string, method string, params interface{}) bool {
		request := JSONRPC{
			JSONRPC: "2.0",
			ID:      requestId, // Use a specific ID for SSE context
			Method:  method,
			Params:  params,
		}
		requestBytes, err := json.Marshal(request)
		if err != nil {
			log.Printf("[SSE] Error marshalling %s request: %v", method, err)
			// Optionally send an error event to the client if desired
			// fmt.Fprintf(w, "event: error\\ndata: {\\"error\\": \\"failed to marshal %s request\\"}\\n\\n", method)
			// flusher.Flush()
			return false
		}

		log.Printf("[SSE] Sending %s request to MCP: %s", method, string(requestBytes))
		if _, err := stdin.Write(append(requestBytes, '\n')); err != nil {
			log.Printf("[SSE] Error writing %s request to MCP stdin: %v", method, err)
			return false
		}

		// Wait for response from MCP server (with timeout)
		select {
		case response, ok := <-responses:
			if !ok {
				log.Printf("[SSE] Responses channel closed while waiting for %s response.", method)
				return false
			}
			log.Printf("[SSE] Received response for %s from MCP via channel: %s", method, string(response))
			// Send response as an SSE event, including the ID
			if _, err := fmt.Fprintf(w, "id: %s\\ndata: %s\\n\\n", requestId, string(response)); err != nil {
				log.Printf("[SSE] Error writing %s response to SSE stream: %v", method, err)
				return false
			}
			flusher.Flush()
			healthMutex.Lock() // Update activity on successful interaction
			*lastActivity = time.Now()
			healthMutex.Unlock()
			return true
		case <-time.After(15 * time.Second): // Increased timeout slightly
			log.Printf("[SSE] Timeout waiting for %s response from MCP server.", method)
			return false
		case <-r.Context().Done(): // Client disconnected
			log.Printf("[SSE] Client disconnected while waiting for %s response.", method)
			return false
		}
	}

	// Send "initialize" request
	if !sendSseRequestAndForwardResponse("sse_init", "initialize", struct{}{}) {
		log.Printf("[SSE] Failed to process 'initialize' sequence. Closing SSE stream for client.")
		// No explicit error sent to client, connection will just not have the expected initial data or may close.
		return
	}

	// Send "tools/list" request
	if !sendSseRequestAndForwardResponse("sse_tools_list", "tools/list", struct{}{}) {
		log.Printf("[SSE] Failed to process 'tools/list' sequence. Closing SSE stream for client.")
		return
	}

	log.Println("[SSE] Initial messages (initialize, tools/list) sent successfully.")

	// Keep alive loop
	keepAliveTicker := time.NewTicker(20 * time.Second)
	defer keepAliveTicker.Stop()

	clientCtx := r.Context() // Use this for the main select loop

	for {
		select {
		case <-clientCtx.Done():
			log.Printf("[SSE] Client disconnected: %v", clientCtx.Err())
			return
		case <-keepAliveTicker.C:
			if _, err := fmt.Fprintf(w, ": keepalive\\n\\n"); err != nil {
				log.Printf("[SSE] Error writing SSE keepalive: %v", err)
				return // Connection likely broken
			}
			flusher.Flush()
			// Note: We are deliberately NOT reading from the 'responses' channel here for keepalives.
			// That channel was used for the initial request/response pairs.
			// For ongoing server-initiated pushes, a different mechanism/channel would be needed.
		}
	}
}
