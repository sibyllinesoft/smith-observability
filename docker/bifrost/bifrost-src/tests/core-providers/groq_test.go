package tests

import (
	"testing"

	"github.com/maximhq/bifrost/tests/core-providers/config"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestGroq(t *testing.T) {
	client, ctx, cancel, err := config.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	testConfig := config.ComprehensiveTestConfig{
		Provider:       schemas.Groq,
		ChatModel:      "llama-3.3-70b-versatile",
		TextModel:      "llama-3.3-70b-versatile", // Use same model for text completion (via conversion)
		EmbeddingModel: "",                        // Groq doesn't support embedding
		Scenarios: config.TestScenarios{
			TextCompletion:        true, // Supported via chat completion conversion
			TextCompletionStream:  true, // Supported via chat completion streaming conversion
			SimpleChat:            true,
			CompletionStream:      true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			MultipleToolCalls:     true,
			End2EndToolCalling:    true,
			AutomaticFunctionCall: true,
			ImageURL:              false,
			ImageBase64:           false,
			MultipleImages:        false,
			CompleteEnd2End:       true,
			Embedding:             false,
		},
	}

	runAllComprehensiveTests(t, client, ctx, testConfig)
}
