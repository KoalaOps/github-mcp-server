#!/bin/bash

# Test script for GitHub MCP server

# Default method is initialize if none provided
METHOD=${1:-initialize}
PARAMS=${2:-"{}"}

# Create a unique ID for the request
ID=$RANDOM

# Send the request to the MCP server
curl -s -X POST http://localhost:8080/mcp \
  -H "Content-Type: application/json" \
  -d "{\"jsonrpc\":\"2.0\",\"id\":$ID,\"method\":\"$METHOD\",\"params\":$PARAMS}" | jq . 