package scenarios

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/tests/core-providers/config"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// RunToolCallsTest executes the tool calls test scenario using dual API testing framework
func RunToolCallsTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig config.ComprehensiveTestConfig) {
	if !testConfig.Scenarios.ToolCalls {
		t.Logf("Tool calls not supported for provider %s", testConfig.Provider)
		return
	}

	t.Run("ToolCalls", func(t *testing.T) {
		chatMessages := []schemas.ChatMessage{
			CreateBasicChatMessage("What's the weather like in New York? answer in celsius"),
		}
		responsesMessages := []schemas.ResponsesMessage{
			CreateBasicResponsesMessage("What's the weather like in New York? answer in celsius"),
		}

		// Get tools for both APIs using the new GetSampleTool function
		chatTool := GetSampleChatTool(SampleToolTypeWeather)           // Chat Completions API
		responsesTool := GetSampleResponsesTool(SampleToolTypeWeather) // Responses API

		// Use specialized tool call retry configuration
		retryConfig := ToolCallRetryConfig(string(SampleToolTypeWeather))
		retryContext := TestRetryContext{
			ScenarioName: "ToolCalls",
			ExpectedBehavior: map[string]interface{}{
				"expected_tool_name": string(SampleToolTypeWeather),
				"required_location":  "new york",
			},
			TestMetadata: map[string]interface{}{
				"provider": testConfig.Provider,
				"model":    testConfig.ChatModel,
			},
		}

		// Enhanced tool call validation (same for both APIs)
		expectations := ToolCallExpectations(string(SampleToolTypeWeather), []string{"location"})
		expectations = ModifyExpectationsForProvider(expectations, testConfig.Provider)

		// Add additional tool-specific validations
		expectations.ExpectedToolCalls[0].ArgumentTypes = map[string]string{
			"location": "string",
		}

		// Create operations for both Chat Completions and Responses API
		chatOperation := func() (*schemas.BifrostResponse, *schemas.BifrostError) {
			chatReq := &schemas.BifrostChatRequest{
				Provider: testConfig.Provider,
				Model:    testConfig.ChatModel,
				Input:    chatMessages,
				Params: &schemas.ChatParameters{
					MaxCompletionTokens: bifrost.Ptr(150),
					Tools:               []schemas.ChatTool{*chatTool},
				},
				Fallbacks: testConfig.Fallbacks,
			}
			return client.ChatCompletionRequest(ctx, chatReq)
		}

		responsesOperation := func() (*schemas.BifrostResponse, *schemas.BifrostError) {
			responsesReq := &schemas.BifrostResponsesRequest{
				Provider: testConfig.Provider,
				Model:    testConfig.ChatModel,
				Input:    responsesMessages,
				Params: &schemas.ResponsesParameters{
					Tools: []schemas.ResponsesTool{*responsesTool},
				},
			}
			return client.ResponsesRequest(ctx, responsesReq)
		}

		// Execute dual API test - passes only if BOTH APIs succeed
		result := WithDualAPITestRetry(t,
			retryConfig,
			retryContext,
			expectations,
			"ToolCalls",
			chatOperation,
			responsesOperation)

		// Validate both APIs succeeded
		if !result.BothSucceeded {
			var errors []string
			if result.ChatCompletionsError != nil {
				errors = append(errors, "Chat Completions: "+GetErrorMessage(result.ChatCompletionsError))
			}
			if result.ResponsesAPIError != nil {
				errors = append(errors, "Responses API: "+GetErrorMessage(result.ResponsesAPIError))
			}
			if len(errors) == 0 {
				errors = append(errors, "One or both APIs failed validation (see logs above)")
			}
			t.Fatalf("❌ ToolCalls dual API test failed: %v", errors)
		}

		// Verify location argument mentions New York using universal tool extraction
		validateLocationInToolCalls := func(response *schemas.BifrostResponse, apiName string) {
			toolCalls := ExtractToolCalls(response)
			locationFound := false

			for _, toolCall := range toolCalls {
				if toolCall.Name == string(SampleToolTypeWeather) {
					var args map[string]interface{}
					if json.Unmarshal([]byte(toolCall.Arguments), &args) == nil {
						if location, exists := args["location"].(string); exists {
							lowerLocation := strings.ToLower(location)
							if strings.Contains(lowerLocation, "new york") || strings.Contains(lowerLocation, "nyc") {
								locationFound = true
								t.Logf("✅ %s tool call has correct location: %s", apiName, location)
								break
							}
						}
					}
				}
			}

			require.True(t, locationFound, "%s API tool call should specify New York as the location", apiName)
		}

		// Validate both API responses
		if result.ChatCompletionsResponse != nil {
			validateLocationInToolCalls(result.ChatCompletionsResponse, "Chat Completions")
		}

		if result.ResponsesAPIResponse != nil {
			validateLocationInToolCalls(result.ResponsesAPIResponse, "Responses")
		}

		t.Logf("🎉 Both Chat Completions and Responses APIs passed ToolCalls test!")
	})
}
