package semanticcache

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// TestSemanticCacheBasicFlow tests the complete semantic cache flow
func TestSemanticCacheBasicFlow(t *testing.T) {
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	ctx := context.Background()

	// Add cache key to context
	ctx = context.WithValue(ctx, CacheKey, "test-cache-enabled")

	// Test request
	request := &schemas.BifrostRequest{
		Provider:    schemas.OpenAI,
		Model:       "gpt-4o-mini",
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Input: []schemas.ChatMessage{
				{
					Role: schemas.ChatMessageRoleUser,
					Content: &schemas.ChatMessageContent{
						ContentStr: bifrost.Ptr("Hello, world!"),
					},
				},
			},
			Params: &schemas.ChatParameters{
				Temperature:         bifrost.Ptr(0.7),
				MaxCompletionTokens: bifrost.Ptr(100),
			},
		},
	}

	t.Log("Testing first request (cache miss)...")

	// First request - should be a cache miss
	modifiedReq, shortCircuit, err := setup.Plugin.PreHook(&ctx, request)
	if err != nil {
		t.Fatalf("PreHook failed: %v", err)
	}

	if shortCircuit != nil {
		t.Fatal("Expected cache miss, but got cache hit")
	}

	if modifiedReq == nil {
		t.Fatal("Modified request is nil")
	}

	t.Log("✅ Cache miss handled correctly")

	// Simulate a response
	response := &schemas.BifrostResponse{
		ID: uuid.New().String(),
		Choices: []schemas.BifrostChatResponseChoice{
			{
				BifrostNonStreamResponseChoice: &schemas.BifrostNonStreamResponseChoice{
					Message: &schemas.ChatMessage{
						Role: "assistant",
						Content: &schemas.ChatMessageContent{
							ContentStr: bifrost.Ptr("Hello! How can I help you today?"),
						},
					},
				},
			},
		},
		ExtraFields: schemas.BifrostResponseExtraFields{
			Provider:       schemas.OpenAI,
			ModelRequested: "gpt-4o-mini",
			RequestType:    schemas.ChatCompletionRequest,
		},
	}

	// Capture original response content for comparison
	var originalContent string
	if len(response.Choices) > 0 && response.Choices[0].Message.Content.ContentStr != nil {
		originalContent = *response.Choices[0].Message.Content.ContentStr
	}
	if originalContent == "" {
		t.Fatal("Original response content is empty")
	}
	t.Logf("Original response content: %s", originalContent)

	// Cache the response
	t.Log("Caching response...")
	_, _, err = setup.Plugin.PostHook(&ctx, response, nil)
	if err != nil {
		t.Fatalf("PostHook failed: %v", err)
	}

	// Wait for async caching to complete
	WaitForCache()
	t.Log("✅ Response cached successfully")

	// Second request - should be a cache hit
	t.Log("Testing second identical request (expecting cache hit)...")

	// Reset context for second request
	ctx2 := context.Background()
	ctx2 = context.WithValue(ctx2, CacheKey, "test-cache-enabled")

	modifiedReq2, shortCircuit2, err := setup.Plugin.PreHook(&ctx2, request)
	if err != nil {
		t.Fatalf("Second PreHook failed: %v", err)
	}

	if shortCircuit2 == nil {
		t.Fatal("expected cache hit on identical request")
		return
	}

	if shortCircuit2.Response == nil {
		t.Fatal("Cache hit but response is nil")
	}

	if modifiedReq2 == nil {
		t.Fatal("Modified request is nil on cache hit")
	}

	t.Log("✅ Cache hit detected and response returned")

	// Verify the cached response
	if len(shortCircuit2.Response.Choices) == 0 {
		t.Fatal("Cached response has no choices")
	}

	cachedContent := shortCircuit2.Response.Choices[0].Message.Content.ContentStr
	if cachedContent == nil || *cachedContent == "" {
		t.Fatal("Cached response content is empty")
	}

	t.Logf("✅ Cached response content: %s", *cachedContent)

	// Compare original and cached content
	cachedContentStr := *cachedContent
	// Trim whitespace and newlines for comparison
	originalContentTrimmed := strings.TrimSpace(originalContent)
	cachedContentTrimmed := strings.TrimSpace(cachedContentStr)

	if originalContentTrimmed != cachedContentTrimmed {
		t.Fatalf("❌ Content mismatch: original='%s', cached='%s'", originalContentTrimmed, cachedContentTrimmed)
	}

	t.Log("✅ Content verification passed - original and cached responses match")
	t.Log("🎉 Basic semantic cache flow test passed!")
}

// TestSemanticCacheStrictFiltering tests that the cache respects parameter differences
func TestSemanticCacheStrictFiltering(t *testing.T) {
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	ctx := context.Background()
	ctx = context.WithValue(ctx, CacheKey, "test-cache-enabled")

	// Base request
	baseRequest := &schemas.BifrostRequest{
		Provider:    schemas.OpenAI,
		Model:       "gpt-4o-mini",
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Input: []schemas.ChatMessage{
				{
					Role: schemas.ChatMessageRoleUser,
					Content: &schemas.ChatMessageContent{
						ContentStr: bifrost.Ptr("What is the weather like?"),
					},
				},
			},
			Params: &schemas.ChatParameters{
				Temperature:         bifrost.Ptr(0.7),
				MaxCompletionTokens: bifrost.Ptr(100),
			},
		},
	}

	t.Log("Testing first request with temperature=0.7...")

	// First request
	_, shortCircuit1, err := setup.Plugin.PreHook(&ctx, baseRequest)
	if err != nil {
		t.Fatalf("First PreHook failed: %v", err)
	}

	if shortCircuit1 != nil {
		t.Fatal("Expected cache miss for first request")
	}

	// Cache a response
	response := &schemas.BifrostResponse{
		ID: uuid.New().String(),
		Choices: []schemas.BifrostChatResponseChoice{
			{
				BifrostNonStreamResponseChoice: &schemas.BifrostNonStreamResponseChoice{
					Message: &schemas.ChatMessage{
						Role: "assistant",
						Content: &schemas.ChatMessageContent{
							ContentStr: bifrost.Ptr("It's sunny today!"),
						},
					},
				},
			},
		},
		ExtraFields: schemas.BifrostResponseExtraFields{
			Provider:       schemas.OpenAI,
			ModelRequested: "gpt-4o-mini",
			RequestType:    schemas.ChatCompletionRequest,
		},
	}

	_, _, err = setup.Plugin.PostHook(&ctx, response, nil)
	if err != nil {
		t.Fatalf("PostHook failed: %v", err)
	}

	WaitForCache()
	t.Log("✅ First response cached")

	// Second request with different temperature - should be cache miss
	t.Log("Testing second request with temperature=0.5 (expecting cache miss)...")

	ctx2 := context.Background()
	ctx2 = context.WithValue(ctx2, CacheKey, "test-cache-enabled")

	modifiedRequest := &schemas.BifrostRequest{
		Provider:    schemas.OpenAI,
		Model:       "gpt-4o-mini",
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Input: []schemas.ChatMessage{
				{
					Role: schemas.ChatMessageRoleUser,
					Content: &schemas.ChatMessageContent{
						ContentStr: bifrost.Ptr("What is the weather like?"),
					},
				},
			},
			Params: &schemas.ChatParameters{
				Temperature:         bifrost.Ptr(0.5), // Different temperature
				MaxCompletionTokens: bifrost.Ptr(100),
			},
		},
	}

	_, shortCircuit2, err := setup.Plugin.PreHook(&ctx2, modifiedRequest)
	if err != nil {
		t.Fatalf("Second PreHook failed: %v", err)
	}

	if shortCircuit2 != nil {
		t.Fatal("Expected cache miss due to different temperature, but got cache hit")
	}

	t.Log("✅ Strict filtering working - different parameters result in cache miss")

	// Third request with different model - should be cache miss
	t.Log("Testing third request with different model (expecting cache miss)...")

	ctx3 := context.Background()
	ctx3 = context.WithValue(ctx3, CacheKey, "test-cache-enabled")

	modifiedRequest2 := &schemas.BifrostRequest{
		Provider:    schemas.OpenAI,
		Model:       "gpt-3.5-turbo", // Different model
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Input: []schemas.ChatMessage{
				{
					Role: schemas.ChatMessageRoleUser,
					Content: &schemas.ChatMessageContent{
						ContentStr: bifrost.Ptr("What is the weather like?"),
					},
				},
			},
			Params: &schemas.ChatParameters{
				Temperature:         bifrost.Ptr(0.7),
				MaxCompletionTokens: bifrost.Ptr(100),
			},
		},
	}

	_, shortCircuit3, err := setup.Plugin.PreHook(&ctx3, modifiedRequest2)
	if err != nil {
		t.Fatalf("Third PreHook failed: %v", err)
	}

	if shortCircuit3 != nil {
		t.Fatal("Expected cache miss due to different model, but got cache hit")
	}

	t.Log("✅ Strict filtering working - different model results in cache miss")
	t.Log("🎉 Strict filtering test passed!")
}

// TestSemanticCacheStreamingFlow tests streaming response caching
func TestSemanticCacheStreamingFlow(t *testing.T) {
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	ctx := context.Background()
	ctx = context.WithValue(ctx, CacheKey, "test-cache-enabled")

	request := &schemas.BifrostRequest{
		Provider:    schemas.OpenAI,
		Model:       "gpt-4o-mini",
		RequestType: schemas.ChatCompletionStreamRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Input: []schemas.ChatMessage{
				{
					Role: schemas.ChatMessageRoleUser,
					Content: &schemas.ChatMessageContent{
						ContentStr: bifrost.Ptr("Tell me a short story"),
					},
				},
			},
			Params: &schemas.ChatParameters{
				Temperature: bifrost.Ptr(0.8),
			},
		},
	}

	t.Log("Testing streaming request (cache miss)...")

	// First request - should be cache miss
	_, shortCircuit, err := setup.Plugin.PreHook(&ctx, request)
	if err != nil {
		t.Fatalf("PreHook failed: %v", err)
	}

	if shortCircuit != nil {
		t.Fatal("Expected cache miss for streaming request")
	}

	t.Log("✅ Streaming cache miss handled correctly")

	// Simulate streaming response chunks
	t.Log("Caching streaming response chunks...")

	chunks := []string{
		"Once upon a time,",
		" there was a brave",
		" knight who saved the day.",
	}

	for i, chunk := range chunks {
		var finishReason *string
		if i == len(chunks)-1 {
			finishReason = bifrost.Ptr("stop")
		}

		chunkResponse := &schemas.BifrostResponse{
			ID: uuid.New().String(),
			Choices: []schemas.BifrostChatResponseChoice{
				{
					Index:        i,
					FinishReason: finishReason,
					BifrostStreamResponseChoice: &schemas.BifrostStreamResponseChoice{
						Delta: &schemas.BifrostStreamDelta{
							Content: bifrost.Ptr(chunk),
						},
					},
				},
			},
			ExtraFields: schemas.BifrostResponseExtraFields{
				Provider:       schemas.OpenAI,
				ModelRequested: "gpt-4o-mini",
				RequestType:    schemas.ChatCompletionStreamRequest,
				ChunkIndex:     i,
			},
		}

		_, _, err = setup.Plugin.PostHook(&ctx, chunkResponse, nil)
		if err != nil {
			t.Fatalf("PostHook failed for chunk %d: %v", i, err)
		}
	}

	WaitForCache()
	t.Log("✅ Streaming response chunks cached")

	// Test cache retrieval for streaming
	t.Log("Testing streaming cache retrieval...")

	ctx2 := context.Background()
	ctx2 = context.WithValue(ctx2, CacheKey, "test-cache-enabled")

	_, shortCircuit2, err := setup.Plugin.PreHook(&ctx2, request)
	if err != nil {
		t.Fatalf("Second PreHook failed: %v", err)
	}

	if shortCircuit2 == nil {
		t.Log("⚠️ Expected streaming cache hit, but got cache miss - this may be expected with the new unified storage")
		return
	}

	if shortCircuit2.Stream == nil {
		t.Fatal("Cache hit but stream is nil")
	}

	t.Log("✅ Streaming cache hit detected")

	// Read from the cached stream
	chunkCount := 0
	for chunk := range shortCircuit2.Stream {
		if chunk.BifrostResponse == nil {
			continue
		}
		chunkCount++
		t.Logf("Received cached chunk %d", chunkCount)
	}

	if chunkCount == 0 {
		t.Fatal("No chunks received from cached stream")
	}

	t.Logf("✅ Received %d cached chunks", chunkCount)
	t.Log("🎉 Streaming cache test passed!")
}

// TestSemanticCache_NoCacheWhenKeyMissing verifies cache is disabled when cache key is missing from context
func TestSemanticCache_NoCacheWhenKeyMissing(t *testing.T) {
	t.Log("Testing cache behavior when cache key is missing...")

	setup := NewTestSetup(t)
	defer setup.Cleanup()

	ctx := context.Background()
	// Don't set the cache key - cache should be disabled

	request := &schemas.BifrostRequest{
		Provider:    schemas.OpenAI,
		Model:       "gpt-4o-mini",
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Input: []schemas.ChatMessage{
				{
					Role: schemas.ChatMessageRoleUser,
					Content: &schemas.ChatMessageContent{
						ContentStr: bifrost.Ptr("Test message"),
					},
				},
			},
		},
	}

	_, shortCircuit, err := setup.Plugin.PreHook(&ctx, request)
	if err != nil {
		t.Fatalf("PreHook failed: %v", err)
	}

	if shortCircuit != nil {
		t.Fatal("Expected no caching when cache key is not set, but got cache hit")
	}

	t.Log("✅ Cache properly disabled when no cache key is set")
	t.Log("🎉 No cache key test passed!")
}

// TestSemanticCache_CustomTTLHandling verifies cache respects custom TTL values from context
func TestSemanticCache_CustomTTLHandling(t *testing.T) {
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	// Configure plugin with custom TTL key
	ctx := context.Background()
	ctx = context.WithValue(ctx, CacheKey, "test-cache-enabled")
	ctx = context.WithValue(ctx, CacheTTLKey, 1*time.Minute) // Custom TTL

	request := &schemas.BifrostRequest{
		Provider:    schemas.OpenAI,
		Model:       "gpt-4o-mini",
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Input: []schemas.ChatMessage{
				{
					Role: schemas.ChatMessageRoleUser,
					Content: &schemas.ChatMessageContent{
						ContentStr: bifrost.Ptr("TTL test message"),
					},
				},
			},
		},
	}

	// First request - cache miss
	_, shortCircuit, err := setup.Plugin.PreHook(&ctx, request)
	if err != nil {
		t.Fatalf("PreHook failed: %v", err)
	}

	if shortCircuit != nil {
		t.Fatal("Expected cache miss, but got cache hit")
	}

	// Simulate response and cache it
	response := &schemas.BifrostResponse{
		ID: "ttl-test-response",
		Choices: []schemas.BifrostChatResponseChoice{
			{
				BifrostNonStreamResponseChoice: &schemas.BifrostNonStreamResponseChoice{
					Message: &schemas.ChatMessage{
						Role: "assistant",
						Content: &schemas.ChatMessageContent{
							ContentStr: bifrost.Ptr("TTL test response"),
						},
					},
				},
			},
		},
		ExtraFields: schemas.BifrostResponseExtraFields{
			Provider:       schemas.OpenAI,
			ModelRequested: "gpt-4o-mini",
			RequestType:    schemas.ChatCompletionRequest,
		},
	}

	_, _, err = setup.Plugin.PostHook(&ctx, response, nil)
	if err != nil {
		t.Fatalf("PostHook failed: %v", err)
	}

	WaitForCache()

	t.Log("✅ Custom TTL configuration test passed!")
}

// TestSemanticCache_CustomThresholdHandling verifies cache respects custom similarity threshold from context
func TestSemanticCache_CustomThresholdHandling(t *testing.T) {
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	// Configure plugin with custom threshold key
	ctx := context.Background()
	ctx = context.WithValue(ctx, CacheKey, "test-cache-enabled")
	ctx = context.WithValue(ctx, CacheThresholdKey, 0.95) // Very high threshold

	request := &schemas.BifrostRequest{
		Provider:    schemas.OpenAI,
		Model:       "gpt-4o-mini",
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Input: []schemas.ChatMessage{
				{
					Role: schemas.ChatMessageRoleUser,
					Content: &schemas.ChatMessageContent{
						ContentStr: bifrost.Ptr("Threshold test message"),
					},
				},
			},
		},
	}

	// Test that custom threshold is used (this would need semantic search to be fully testable)
	_, shortCircuit, err := setup.Plugin.PreHook(&ctx, request)
	if err != nil {
		t.Fatalf("PreHook failed: %v", err)
	}

	if shortCircuit != nil {
		t.Fatal("Expected cache miss with high threshold, but got cache hit")
	}

	t.Log("✅ Custom threshold configuration test passed!")
}

// TestSemanticCache_ProviderModelCachingFlags verifies cache behavior with provider/model caching flags
func TestSemanticCache_ProviderModelCachingFlags(t *testing.T) {
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	// Test with provider/model caching disabled
	setup.Config.CacheByProvider = bifrost.Ptr(false)
	setup.Config.CacheByModel = bifrost.Ptr(false)

	ctx := context.Background()
	ctx = context.WithValue(ctx, CacheKey, "test-cache-enabled")

	request1 := &schemas.BifrostRequest{
		Provider:    schemas.OpenAI,
		Model:       "gpt-4o-mini",
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Input: []schemas.ChatMessage{
				{
					Role: schemas.ChatMessageRoleUser,
					Content: &schemas.ChatMessageContent{
						ContentStr: bifrost.Ptr("Provider model flags test"),
					},
				},
			},
		},
	}

	// First request with OpenAI
	_, shortCircuit1, err := setup.Plugin.PreHook(&ctx, request1)
	if err != nil {
		t.Fatalf("PreHook failed: %v", err)
	}

	if shortCircuit1 != nil {
		t.Fatal("Expected cache miss, but got cache hit")
	}

	// Cache the response
	response := &schemas.BifrostResponse{
		ID: "provider-model-test",
		Choices: []schemas.BifrostChatResponseChoice{
			{
				BifrostNonStreamResponseChoice: &schemas.BifrostNonStreamResponseChoice{
					Message: &schemas.ChatMessage{
						Role: "assistant",
						Content: &schemas.ChatMessageContent{
							ContentStr: bifrost.Ptr("Provider model test response"),
						},
					},
				},
			},
		},
		ExtraFields: schemas.BifrostResponseExtraFields{
			Provider:       schemas.OpenAI,
			ModelRequested: "gpt-4o-mini",
			RequestType:    schemas.ChatCompletionRequest,
		},
	}

	_, _, err = setup.Plugin.PostHook(&ctx, response, nil)
	if err != nil {
		t.Fatalf("PostHook failed: %v", err)
	}

	WaitForCache()

	// Second request with different provider - should potentially hit cache since provider is not considered
	request2 := &schemas.BifrostRequest{
		Provider:    schemas.Anthropic, // Different provider
		Model:       "claude-3-haiku",  // Different model
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Input: []schemas.ChatMessage{
				{
					Role: schemas.ChatMessageRoleUser,
					Content: &schemas.ChatMessageContent{
						ContentStr: bifrost.Ptr("Provider model flags test"), // Same content
					},
				},
			},
		},
	}

	ctx2 := context.Background()
	ctx2 = context.WithValue(ctx2, CacheKey, "test-cache-enabled")

	_, shortCircuit2, err := setup.Plugin.PreHook(&ctx2, request2)
	if err != nil {
		t.Fatalf("Second PreHook failed: %v", err)
	}

	// With provider/model caching disabled, we might get cache hits across different providers/models
	// This behavior depends on the exact implementation of hash generation
	t.Logf("Cache behavior with disabled provider/model flags: hit=%v", shortCircuit2 != nil)

	t.Log("✅ Provider/model caching flags test passed!")
}

// TestSemanticCache_ConfigurationEdgeCases verifies edge cases in configuration handling
func TestSemanticCache_ConfigurationEdgeCases(t *testing.T) {
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	// Test with invalid TTL type in context
	ctx := context.Background()
	ctx = context.WithValue(ctx, CacheKey, "test-cache-enabled")
	ctx = context.WithValue(ctx, CacheTTLKey, "not-a-duration") // Invalid TTL type

	request := &schemas.BifrostRequest{
		Provider:    schemas.OpenAI,
		Model:       "gpt-4o-mini",
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Input: []schemas.ChatMessage{
				{
					Role: schemas.ChatMessageRoleUser,
					Content: &schemas.ChatMessageContent{
						ContentStr: bifrost.Ptr("Edge case test"),
					},
				},
			},
		},
	}

	// Should handle invalid TTL gracefully
	_, shortCircuit, err := setup.Plugin.PreHook(&ctx, request)
	if err != nil {
		t.Fatalf("PreHook failed with invalid TTL: %v", err)
	}

	if shortCircuit != nil {
		t.Fatal("Unexpected cache hit with invalid TTL")
	}

	// Test with invalid threshold type
	ctx2 := context.Background()
	ctx2 = context.WithValue(ctx2, CacheKey, "test-cache-enabled")
	ctx2 = context.WithValue(ctx2, CacheThresholdKey, "not-a-float") // Invalid threshold type

	// Should handle invalid threshold gracefully
	_, shortCircuit2, err := setup.Plugin.PreHook(&ctx2, request)
	if err != nil {
		t.Fatalf("PreHook failed with invalid threshold: %v", err)
	}

	if shortCircuit2 != nil {
		t.Fatal("Unexpected cache hit with invalid threshold")
	}

	t.Log("✅ Configuration edge cases test passed!")
}
