// Package lib provides core functionality for the Bifrost HTTP service,
// including context propagation, header management, and integration with monitoring systems.
//
// This package handles the conversion of FastHTTP request contexts to Bifrost contexts,
// ensuring that important metadata and tracking information is preserved across the system.
// It supports propagation of both Prometheus metrics and Maxim tracing data through HTTP headers.
package lib

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/plugins/governance"
	"github.com/maximhq/bifrost/plugins/maxim"
	"github.com/maximhq/bifrost/plugins/semanticcache"
	"github.com/maximhq/bifrost/plugins/telemetry"
	"github.com/valyala/fasthttp"
)

// ConvertToBifrostContext converts a FastHTTP RequestCtx to a Bifrost context,
// preserving important header values for monitoring and tracing purposes.
//
// The function processes several types of special headers:
// 1. Prometheus Headers (x-bf-prom-*):
//   - All headers prefixed with 'x-bf-prom-' are copied to the context
//   - The prefix is stripped and the remainder becomes the context key
//   - Example: 'x-bf-prom-latency' becomes 'latency' in the context
//
// 2. Maxim Tracing Headers (x-bf-maxim-*):
//   - Specifically handles 'x-bf-maxim-traceID' and 'x-bf-maxim-generationID'
//   - These headers enable trace correlation across service boundaries
//   - Values are stored using Maxim's context keys for consistency
//
// 3. MCP Headers (x-bf-mcp-*):
//   - Specifically handles 'x-bf-mcp-include-clients', 'x-bf-mcp-exclude-clients', 'x-bf-mcp-include-tools', and 'x-bf-mcp-exclude-tools'
//   - These headers enable MCP client and tool filtering
//   - Values are stored using MCP context keys for consistency
//
// 4. Governance Headers:
//   - x-bf-vk: Virtual key for governance (required for governance to work)
//   - x-bf-team: Team identifier for team-based governance rules
//   - x-bf-user: User identifier for user-based governance rules
//   - x-bf-customer: Customer identifier for customer-based governance rules
//
// 5. API Key Headers:
//   - Authorization: Bearer token format only (e.g., "Bearer sk-...") - OpenAI style
//   - x-api-key: Direct API key value - Anthropic style
//   - Keys are extracted and stored in the context using schemas.BifrostContextKey
//   - This enables explicit key usage for requests via headers
//

// Parameters:
//   - ctx: The FastHTTP request context containing the original headers
//
// Returns:
//   - *context.Context: A new context.Context containing the propagated values
//
// Example Usage:
//
//	fastCtx := &fasthttp.RequestCtx{...}
//	bifrostCtx := ConvertToBifrostContext(fastCtx)
//	// bifrostCtx now contains any prometheus and maxim header values

type ContextKey string

func ConvertToBifrostContext(ctx *fasthttp.RequestCtx, allowDirectKeys bool) *context.Context {
	bifrostCtx := context.Background()

	// First, check if x-request-id header exists
	requestID := string(ctx.Request.Header.Peek("x-request-id"))
	if requestID == "" {
		requestID = uuid.New().String()
	}
	bifrostCtx = context.WithValue(bifrostCtx, schemas.BifrostContextKeyRequestID, requestID)

	// Initialize tags map for collecting maxim tags
	maximTags := make(map[string]string)

	// Then process other headers
	ctx.Request.Header.All()(func(key, value []byte) bool {
		keyStr := strings.ToLower(string(key))
		if labelName, ok := strings.CutPrefix(keyStr, "x-bf-prom-"); ok {
			bifrostCtx = context.WithValue(bifrostCtx, telemetry.ContextKey(labelName), string(value))
			return true
		}
		// Checking for maxim headers
		if labelName, ok := strings.CutPrefix(keyStr, "x-bf-maxim-"); ok {
			switch labelName {
			case string(maxim.GenerationIDKey):
				bifrostCtx = context.WithValue(bifrostCtx, maxim.ContextKey(labelName), string(value))
			case string(maxim.TraceIDKey):
				bifrostCtx = context.WithValue(bifrostCtx, maxim.ContextKey(labelName), string(value))
			case string(maxim.SessionIDKey):
				bifrostCtx = context.WithValue(bifrostCtx, maxim.ContextKey(labelName), string(value))
			case string(maxim.TraceNameKey):
				bifrostCtx = context.WithValue(bifrostCtx, maxim.ContextKey(labelName), string(value))
			case string(maxim.GenerationNameKey):
				bifrostCtx = context.WithValue(bifrostCtx, maxim.ContextKey(labelName), string(value))
			case string(maxim.LogRepoIDKey):
				bifrostCtx = context.WithValue(bifrostCtx, maxim.ContextKey(labelName), string(value))
			default:
				// apart from these all headers starting with x-bf-maxim- are keys for tags
				// collect them in the maximTags map
				maximTags[labelName] = string(value)
			}
			return true
		}
		// MCP control headers
		if labelName, ok := strings.CutPrefix(keyStr, "x-bf-mcp-"); ok {
			switch labelName {
			case "include-clients":
				fallthrough
			case "exclude-clients":
				fallthrough
			case "include-tools":
				fallthrough
			case "exclude-tools":
				bifrostCtx = context.WithValue(bifrostCtx, ContextKey("mcp-"+labelName), string(value))
				return true
			}
		}
		// Handle governance headers (x-bf-team, x-bf-user, x-bf-customer)
		if keyStr == "x-bf-team" || keyStr == "x-bf-user" || keyStr == "x-bf-customer" {
			bifrostCtx = context.WithValue(bifrostCtx, governance.ContextKey(keyStr), string(value))
			return true
		}
		// Handle virtual key header (x-bf-vk)
		if keyStr == "x-bf-vk" {
			bifrostCtx = context.WithValue(bifrostCtx, governance.ContextKey(keyStr), string(value))
			return true
		}
		// Handle cache key header (x-bf-cache-key)
		if keyStr == "x-bf-cache-key" {
			bifrostCtx = context.WithValue(bifrostCtx, semanticcache.CacheKey, string(value))
			return true
		}
		// Handle cache TTL header (x-bf-cache-ttl)
		if keyStr == "x-bf-cache-ttl" {
			valueStr := string(value)
			var ttlDuration time.Duration
			var err error

			// First try to parse as duration (e.g., "30s", "5m", "1h")
			if ttlDuration, err = time.ParseDuration(valueStr); err != nil {
				// If that fails, try to parse as plain number and treat as seconds
				if seconds, parseErr := strconv.Atoi(valueStr); parseErr == nil && seconds > 0 {
					ttlDuration = time.Duration(seconds) * time.Second
					err = nil // Reset error since we successfully parsed as seconds
				}
			}

			if err == nil {
				bifrostCtx = context.WithValue(bifrostCtx, semanticcache.CacheTTLKey, ttlDuration)
			}
			// If both parsing attempts fail, we silently ignore the header and use default TTL
			return true
		}
		// Cache threshold header
		if keyStr == "x-bf-cache-threshold" {
			threshold, err := strconv.ParseFloat(string(value), 64)
			if err == nil {
				// Clamp threshold to the inclusive range [0.0, 1.0]
				if threshold < 0.0 {
					threshold = 0.0
				} else if threshold > 1.0 {
					threshold = 1.0
				}
				bifrostCtx = context.WithValue(bifrostCtx, semanticcache.CacheThresholdKey, threshold)
			}
			// If parsing fails, silently ignore the header (no context value set)
			return true
		}
		// Cache type header
		if keyStr == "x-bf-cache-type" {
			bifrostCtx = context.WithValue(bifrostCtx, semanticcache.CacheTypeKey, semanticcache.CacheType(string(value)))
			return true
		}
		// Cache no store header
		if keyStr == "x-bf-cache-no-store" {
			if valueStr := string(value); valueStr == "true" {
				bifrostCtx = context.WithValue(bifrostCtx, semanticcache.CacheNoStoreKey, true)
			}
			return true
		}
		return true
	})

	// Store the collected maxim tags in the context
	if len(maximTags) > 0 {
		bifrostCtx = context.WithValue(bifrostCtx, maxim.ContextKey(maxim.TagsKey), maximTags)
	}

	if allowDirectKeys {
		// Extract API key from Authorization header (Bearer format) or x-api-key header
		var apiKey string

		// TODO: fix plugin data leak
		// Check Authorization header (Bearer format only - OpenAI style)
		authHeader := string(ctx.Request.Header.Peek("Authorization"))
		if authHeader != "" {
			// Only accept Bearer token format: "Bearer ..."
			if strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
				authHeaderValue := strings.TrimSpace(authHeader[7:]) // Remove "Bearer " prefix
				if authHeaderValue != "" {
					apiKey = authHeaderValue
				}
			} else {
				apiKey = authHeader
			}
		}

		// Check x-api-key header if no valid Authorization header found (Anthropic style)
		if apiKey == "" {
			xAPIKey := string(ctx.Request.Header.Peek("x-api-key"))
			if xAPIKey != "" {
				apiKey = strings.TrimSpace(xAPIKey)
			}
		}

		// If we found an API key, create a Key object and store it in context
		if apiKey != "" {
			key := schemas.Key{
				ID:     "header-provided", // Identifier for header-provided keys
				Value:  apiKey,
				Models: []string{}, // Empty models list - will be validated by provider
				Weight: 1.0,        // Default weight
			}
			bifrostCtx = context.WithValue(bifrostCtx, schemas.BifrostContextKeyDirectKey, key)
		}		
	}
	// Adding fallback context
	if ctx.UserValue(schemas.BifrostContextKey("x-litellm-fallback")) != nil {
		bifrostCtx = context.WithValue(bifrostCtx, schemas.BifrostContextKey("x-litellm-fallback"), "true")
	}

	return &bifrostCtx
}
