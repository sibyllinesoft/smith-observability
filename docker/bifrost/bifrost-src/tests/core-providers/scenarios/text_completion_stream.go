package scenarios

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/tests/core-providers/config"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// RunTextCompletionStreamTest executes the text completion streaming test scenario
func RunTextCompletionStreamTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig config.ComprehensiveTestConfig) {
	if !testConfig.Scenarios.TextCompletionStream {
		t.Logf("Text completion stream not supported for provider %s", testConfig.Provider)
		return
	}

	t.Run("TextCompletionStream", func(t *testing.T) {
		// Create a text completion prompt
		prompt := "Write a short story about a robot learning to paint. Keep it under 150 words."

		input := &schemas.TextCompletionInput{
			PromptStr: &prompt,
		}

		// Use TextModel if available, otherwise fall back to ChatModel
		model := testConfig.TextModel
		if model == "" {
			model = testConfig.ChatModel
		}

		request := &schemas.BifrostTextCompletionRequest{
			Provider: testConfig.Provider,
			Model:    model,
			Input:    input,
			Params: &schemas.TextCompletionParameters{
				MaxTokens: bifrost.Ptr(150),
			},
			Fallbacks: testConfig.Fallbacks,
		}

		// Use retry framework for stream requests
		retryConfig := StreamingRetryConfig()
		retryContext := TestRetryContext{
			ScenarioName: "TextCompletionStream",
			ExpectedBehavior: map[string]interface{}{
				"should_stream_content": true,
				"should_tell_story":     true,
				"topic":                 "robot painting",
			},
			TestMetadata: map[string]interface{}{
				"provider": testConfig.Provider,
				"model":    model,
			},
		}

		// Use proper streaming retry wrapper for the stream request
		responseChannel, err := WithStreamRetry(t, retryConfig, retryContext, func() (chan *schemas.BifrostStream, *schemas.BifrostError) {
			return client.TextCompletionStreamRequest(ctx, request)
		})

		// Enhanced error handling
		RequireNoError(t, err, "Text completion stream request failed")
		if responseChannel == nil {
			t.Fatal("Response channel should not be nil")
		}

		var fullContent strings.Builder
		var responseCount int
		var lastResponse *schemas.BifrostStream

		// Create a timeout context for the stream reading
		streamCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()

		t.Logf("📡 Starting to read text completion streaming response...")

		// Read streaming responses
		for {
			select {
			case response, ok := <-responseChannel:
				if !ok {
					// Channel closed, streaming completed
					t.Logf("✅ Text completion streaming completed. Total chunks received: %d", responseCount)
					goto streamComplete
				}

				if response == nil {
					t.Fatal("Streaming response should not be nil")
				}
				lastResponse = response

				// Basic validation of streaming response structure
				if response.BifrostResponse != nil {
					if response.BifrostResponse.ExtraFields.Provider != testConfig.Provider {
						t.Logf("⚠️ Warning: Provider mismatch - expected %s, got %s", testConfig.Provider, response.BifrostResponse.ExtraFields.Provider)
					}
					if response.ID == "" {
						t.Logf("⚠️ Warning: Response ID is empty")
					}

					// Validate text completion response structure
					if response.BifrostResponse.Choices == nil {
						t.Logf("⚠️ Warning: Choices should not be nil in text completion streaming")
					}

					// Process each choice in the response (similar to chat completion)
					for _, choice := range response.Choices {
						// For text completion, we expect either streaming deltas or text completion choices
						if choice.BifrostStreamResponseChoice != nil {
							// Get content from delta for text completion streaming
							delta := choice.BifrostStreamResponseChoice.Delta
							if delta != nil && delta.Content != nil {
								fullContent.WriteString(*delta.Content)
								t.Logf("✍️ Text delta: %s", *delta.Content)
							}

							// Check finish reason if present
							if choice.FinishReason != nil {
								t.Logf("🏁 Finish reason: %s", *choice.FinishReason)
							}
						} else if choice.BifrostTextCompletionResponseChoice != nil {
							// Handle direct text completion response choice (converted by providers)
							if choice.BifrostTextCompletionResponseChoice.Text != nil {
								fullContent.WriteString(*choice.BifrostTextCompletionResponseChoice.Text)
								t.Logf("✍️ Text completion: %s", *choice.BifrostTextCompletionResponseChoice.Text)
							}

							// Check finish reason if present
							if choice.FinishReason != nil {
								t.Logf("🏁 Finish reason: %s", *choice.FinishReason)
							}
						} else {
							t.Logf("⚠️ Warning: Choice %d has no text completion or stream response content", choice.Index)
						}
					}
				}

				responseCount++

				// Safety check to prevent infinite loops in case of issues
				if responseCount > 500 {
					t.Fatal("Received too many streaming chunks, something might be wrong")
				}

			case <-streamCtx.Done():
				t.Fatal("Timeout waiting for text completion streaming response")
			}
		}

	streamComplete:
		// Validate final streaming response
		finalContent := strings.TrimSpace(fullContent.String())

		// Create a consolidated response for validation
		consolidatedResponse := createConsolidatedTextCompletionResponse(finalContent, lastResponse, testConfig.Provider)

		// Enhanced validation expectations for text completion streaming
		expectations := GetExpectationsForScenario("TextCompletionStream", testConfig, map[string]interface{}{})
		expectations = ModifyExpectationsForProvider(expectations, testConfig.Provider)
		expectations.ShouldContainKeywords = append(expectations.ShouldContainKeywords, []string{"robot"}...) // Should include story elements
		expectations.MinContentLength = 30                                                                    // Should be substantial content
		expectations.MaxContentLength = 2000                                                                  // Reasonable upper bound

		// Validate the consolidated text completion streaming response
		validationResult := ValidateResponse(t, consolidatedResponse, nil, expectations, "TextCompletionStream")

		// Basic streaming validation
		if responseCount == 0 {
			t.Fatal("Should receive at least one streaming response")
		}

		if finalContent == "" {
			t.Fatal("Final content should not be empty")
		}

		if len(finalContent) < 5 {
			t.Fatal("Final content should be substantial")
		}

		if !validationResult.Passed {
			t.Logf("⚠️ Text completion streaming validation warnings: %v", validationResult.Errors)
		}

		t.Logf("📊 Text completion streaming metrics: %d chunks, %d chars", responseCount, len(finalContent))

		t.Logf("✅ Text completion streaming test completed successfully")
		t.Logf("📝 Final content (%d chars): %s", len(finalContent), finalContent)
	})

	// Test text completion streaming with different prompts
	t.Run("TextCompletionStreamVariedPrompts", func(t *testing.T) {
		// Use TextModel if available, otherwise fall back to ChatModel
		model := testConfig.TextModel
		if model == "" {
			model = testConfig.ChatModel
		}
		testPrompts := []struct {
			name   string
			prompt string
			expect string
		}{
			{
				name:   "SimpleCompletion",
				prompt: "The quick brown fox",
				expect: "completion",
			},
			{
				name:   "Question",
				prompt: "What is artificial intelligence? AI is",
				expect: "definition",
			},
			{
				name:   "CodeCompletion",
				prompt: "def fibonacci(n):\n    if n <= 1:",
				expect: "code",
			},
		}

		for _, testCase := range testPrompts {
			t.Run(testCase.name, func(t *testing.T) {
				input := &schemas.TextCompletionInput{
					PromptStr: &testCase.prompt,
				}

				request := &schemas.BifrostTextCompletionRequest{
					Provider: testConfig.Provider,
					Model:    model,
					Input:    input,
					Params: &schemas.TextCompletionParameters{
						MaxTokens:   bifrost.Ptr(50),
						Temperature: bifrost.Ptr(0.7),
					},
					Fallbacks: testConfig.Fallbacks,
				}

				responseChannel, err := client.TextCompletionStreamRequest(ctx, request)
				RequireNoError(t, err, "Text completion stream with varied prompts failed")
				if responseChannel == nil {
					t.Fatal("Response channel should not be nil")
				}

				var responseCount int
				var content strings.Builder

				streamCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
				defer cancel()

				t.Logf("🔄 Testing text completion streaming with prompt: %s", testCase.name)

				for {
					select {
					case response, ok := <-responseChannel:
						if !ok {
							goto variedPromptComplete
						}

						if response == nil {
							t.Fatal("Streaming response should not be nil")
						}
						responseCount++

						// Extract content from choices
						if response.BifrostResponse != nil {
							for _, choice := range response.Choices {
								if choice.BifrostStreamResponseChoice != nil {
									delta := choice.BifrostStreamResponseChoice.Delta
									if delta != nil && delta.Content != nil {
										content.WriteString(*delta.Content)
									}
								}
							}
						}

						if responseCount > 100 {
							goto variedPromptComplete
						}

					case <-streamCtx.Done():
						t.Fatal("Timeout waiting for text completion streaming response")
					}
				}

			variedPromptComplete:
				finalContent := strings.TrimSpace(content.String())

				if responseCount == 0 {
					t.Fatal("Should receive at least one streaming response")
				}

				if finalContent == "" {
					t.Logf("⚠️ Warning: No content generated for prompt: %s", testCase.prompt)
				} else {
					t.Logf("✅ Generated content for %s: %s", testCase.name, finalContent)
				}
			})
		}
	})

	// Test text completion streaming with different parameters
	t.Run("TextCompletionStreamParameters", func(t *testing.T) {
		// Use TextModel if available, otherwise fall back to ChatModel
		model := testConfig.TextModel
		if model == "" {
			model = testConfig.ChatModel
		}

		prompt := "Once upon a time in a distant galaxy"

		parameterTests := []struct {
			name        string
			temperature *float64
			maxTokens   *int
			topP        *float64
		}{
			{
				name:        "HighCreativity",
				temperature: bifrost.Ptr(0.9),
				maxTokens:   bifrost.Ptr(100),
				topP:        bifrost.Ptr(0.9),
			},
			{
				name:        "LowCreativity",
				temperature: bifrost.Ptr(0.1),
				maxTokens:   bifrost.Ptr(50),
				topP:        bifrost.Ptr(0.5),
			},
			{
				name:        "Balanced",
				temperature: bifrost.Ptr(0.5),
				maxTokens:   bifrost.Ptr(75),
				topP:        bifrost.Ptr(0.8),
			},
		}

		for _, paramTest := range parameterTests {
			t.Run(paramTest.name, func(t *testing.T) {
				input := &schemas.TextCompletionInput{
					PromptStr: &prompt,
				}

				request := &schemas.BifrostTextCompletionRequest{
					Provider: testConfig.Provider,
					Model:    model,
					Input:    input,
					Params: &schemas.TextCompletionParameters{
						MaxTokens:   paramTest.maxTokens,
						Temperature: paramTest.temperature,
						TopP:        paramTest.topP,
					},
					Fallbacks: testConfig.Fallbacks,
				}

				responseChannel, err := client.TextCompletionStreamRequest(ctx, request)
				RequireNoError(t, err, "Text completion stream with parameters failed")
				if responseChannel == nil {
					t.Fatal("Response channel should not be nil")
				}

				var responseCount int
				streamCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
				defer cancel()

				t.Logf("🔧 Testing text completion streaming with parameters: %s", paramTest.name)

				for {
					select {
					case response, ok := <-responseChannel:
						if !ok {
							goto parameterTestComplete
						}

						if response != nil {
							responseCount++
						}

						if responseCount > 150 {
							goto parameterTestComplete
						}

					case <-streamCtx.Done():
						t.Fatal("Timeout waiting for text completion streaming response")
					}
				}

			parameterTestComplete:
				if responseCount == 0 {
					t.Fatal("Should receive at least one streaming response")
				}

				t.Logf("✅ Parameter test %s completed with %d chunks", paramTest.name, responseCount)
			})
		}
	})
}

// createConsolidatedTextCompletionResponse creates a consolidated response for validation
func createConsolidatedTextCompletionResponse(finalContent string, lastResponse *schemas.BifrostStream, provider schemas.ModelProvider) *schemas.BifrostResponse {
	consolidatedResponse := &schemas.BifrostResponse{
		Object: "text_completion",
		Choices: []schemas.BifrostChatResponseChoice{
			{
				Index: 0,
				BifrostTextCompletionResponseChoice: &schemas.BifrostTextCompletionResponseChoice{
					Text: &finalContent,
				},
			},
		},
		ExtraFields: schemas.BifrostResponseExtraFields{
			Provider:    provider,
			RequestType: schemas.TextCompletionRequest,
		},
	}

	// Copy usage and other metadata from last response if available
	if lastResponse != nil && lastResponse.BifrostResponse != nil {
		consolidatedResponse.Usage = lastResponse.Usage
		consolidatedResponse.Model = lastResponse.Model
		consolidatedResponse.ID = lastResponse.ID
		consolidatedResponse.Created = lastResponse.Created

		// Copy finish reason from last choice if available
		if len(lastResponse.Choices) > 0 && lastResponse.Choices[0].FinishReason != nil {
			consolidatedResponse.Choices[0].FinishReason = lastResponse.Choices[0].FinishReason
		}
	}

	return consolidatedResponse
}
