package scenarios

import (
	"context"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/tests/core-providers/config"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// RunAutomaticFunctionCallingTest executes the automatic function calling test scenario using dual API testing framework
func RunAutomaticFunctionCallingTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig config.ComprehensiveTestConfig) {
	if !testConfig.Scenarios.AutomaticFunctionCall {
		t.Logf("Automatic function calling not supported for provider %s", testConfig.Provider)
		return
	}

	t.Run("AutomaticFunctionCalling", func(t *testing.T) {
		chatMessages := []schemas.ChatMessage{
			CreateBasicChatMessage("Get the current time in UTC timezone"),
		}
		responsesMessages := []schemas.ResponsesMessage{
			CreateBasicResponsesMessage("Get the current time in UTC timezone"),
		}

		// Get tools for both APIs using the new GetSampleTool function
		chatTool := GetSampleChatTool(SampleToolTypeTime) // Chat Completions API
		if chatTool == nil {
			t.Fatalf("GetSampleChatTool returned nil for SampleToolTypeTime")
		}

		responsesTool := GetSampleResponsesTool(SampleToolTypeTime) // Responses API
		if responsesTool == nil {
			t.Fatalf("GetSampleResponsesTool returned nil for SampleToolTypeTime")
		}

		// Use specialized tool call retry configuration
		retryConfig := ToolCallRetryConfig(string(SampleToolTypeTime))
		retryContext := TestRetryContext{
			ScenarioName: "AutomaticFunctionCalling",
			ExpectedBehavior: map[string]interface{}{
				"expected_tool_name": string(SampleToolTypeTime),
				"is_forced_call":     true,
				"timezone":           "UTC",
			},
			TestMetadata: map[string]interface{}{
				"provider":    testConfig.Provider,
				"model":       testConfig.ChatModel,
				"tool_choice": "forced",
			},
		}

		// Enhanced tool call validation for automatic/forced function calls (same for both APIs)
		expectations := ToolCallExpectations(string(SampleToolTypeTime), []string{"timezone"})
		expectations = ModifyExpectationsForProvider(expectations, testConfig.Provider)
		expectations.ExpectedToolCalls[0].ArgumentTypes = map[string]string{
			"timezone": "string",
		}

		// Create operations for both Chat Completions and Responses API
		chatOperation := func() (*schemas.BifrostResponse, *schemas.BifrostError) {
			chatReq := &schemas.BifrostChatRequest{
				Provider: testConfig.Provider,
				Model:    testConfig.ChatModel,
				Input:    chatMessages,
				Params: &schemas.ChatParameters{
					Tools: []schemas.ChatTool{
						*chatTool,
					},
					ToolChoice: &schemas.ChatToolChoice{
						ChatToolChoiceStruct: &schemas.ChatToolChoiceStruct{
							Type: schemas.ChatToolChoiceTypeFunction,
							Function: schemas.ChatToolChoiceFunction{
								Name: string(SampleToolTypeTime),
							},
						},
					},
				},
			}

			return client.ChatCompletionRequest(ctx, chatReq)
		}

		responsesOperation := func() (*schemas.BifrostResponse, *schemas.BifrostError) {
			responsesReq := &schemas.BifrostResponsesRequest{
				Provider: testConfig.Provider,
				Model:    testConfig.ChatModel,
				Input:    responsesMessages,
				Params: &schemas.ResponsesParameters{
					Tools: []schemas.ResponsesTool{
						*responsesTool,
					},
					ToolChoice: &schemas.ResponsesToolChoice{
						ResponsesToolChoiceStruct: &schemas.ResponsesToolChoiceStruct{
							Type: schemas.ResponsesToolChoiceTypeFunction,
							Name: bifrost.Ptr(string(SampleToolTypeTime)),
						},
					},
				},
			}

			return client.ResponsesRequest(ctx, responsesReq)
		}

		// Execute dual API test - passes only if BOTH APIs succeed
		result := WithDualAPITestRetry(t,
			retryConfig,
			retryContext,
			expectations,
			"AutomaticFunctionCalling",
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
			t.Fatalf("❌ AutomaticFunctionCalling dual API test failed: %v", errors)
		}

		// Additional validation specific to automatic function calling using universal tool extraction
		validateAutomaticToolCall := func(response *schemas.BifrostResponse, apiName string) {
			toolCalls := ExtractToolCalls(response)
			foundValidToolCall := false

			for _, toolCall := range toolCalls {
				if toolCall.Name == string(SampleToolTypeTime) {
					foundValidToolCall = true
					t.Logf("✅ %s automatic function call: %s", apiName, toolCall.Arguments)

					// Additional validation for timezone argument
					lowerArgs := strings.ToLower(toolCall.Arguments)
					if strings.Contains(lowerArgs, "utc") || strings.Contains(lowerArgs, "timezone") {
						t.Logf("✅ %s tool call correctly includes timezone information", apiName)
					} else {
						t.Logf("⚠️ %s tool call may be missing timezone specification: %s", apiName, toolCall.Arguments)
					}
					break
				}
			}

			if !foundValidToolCall {
				t.Fatalf("Expected %s API to have automatic tool call for 'time'", apiName)
			}
		}

		// Validate both API responses
		if result.ChatCompletionsResponse != nil {
			validateAutomaticToolCall(result.ChatCompletionsResponse, "Chat Completions")
		}

		if result.ResponsesAPIResponse != nil {
			validateAutomaticToolCall(result.ResponsesAPIResponse, "Responses")
		}

		t.Logf("🎉 Both Chat Completions and Responses APIs passed AutomaticFunctionCalling test!")
	})
}
