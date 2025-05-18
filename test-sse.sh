#!/bin/bash
# Test SSE connection to MCP server
curl -N -H "Accept: text/event-stream" http://localhost:8080/mcp 