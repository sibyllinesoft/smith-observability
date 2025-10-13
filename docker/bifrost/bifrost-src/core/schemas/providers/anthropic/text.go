package anthropic

import (
	"fmt"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// ToAnthropicTextCompletionRequest converts a Bifrost text completion request to Anthropic format
func ToAnthropicTextCompletionRequest(bifrostReq *schemas.BifrostTextCompletionRequest) *AnthropicTextRequest {
	if bifrostReq == nil {
		return nil
	}

	prompt := ""
	if bifrostReq.Input.PromptStr != nil {
		prompt = *bifrostReq.Input.PromptStr
	} else if len(bifrostReq.Input.PromptArray) > 0 {
		prompt = strings.Join(bifrostReq.Input.PromptArray, "\n\n")
	}

	anthropicReq := &AnthropicTextRequest{
		Model:             bifrostReq.Model,
		Prompt:            fmt.Sprintf("\n\nHuman: %s\n\nAssistant:", prompt),
		MaxTokensToSample: AnthropicDefaultMaxTokens, // Default value
	}

	// Convert parameters
	if bifrostReq.Params != nil {
		if bifrostReq.Params.MaxTokens != nil {
			anthropicReq.MaxTokensToSample = *bifrostReq.Params.MaxTokens
		}
		anthropicReq.Temperature = bifrostReq.Params.Temperature
		anthropicReq.TopP = bifrostReq.Params.TopP
		anthropicReq.StopSequences = bifrostReq.Params.Stop

		if bifrostReq.Params.ExtraParams != nil {
			if topK, ok := schemas.SafeExtractIntPointer(bifrostReq.Params.ExtraParams["top_k"]); ok {
				anthropicReq.TopK = topK
			}
		}
	}

	return anthropicReq
}

// ToBifrostRequest converts an Anthropic text request back to Bifrost format
func (r *AnthropicTextRequest) ToBifrostRequest() *schemas.BifrostTextCompletionRequest {
	if r == nil {
		return nil
	}

	provider, model := schemas.ParseModelString(r.Model, schemas.Anthropic)

	bifrostReq := &schemas.BifrostTextCompletionRequest{
		Provider: provider,
		Model:    model,
		Input: &schemas.TextCompletionInput{
			PromptStr: &r.Prompt,
		},
		Params: &schemas.TextCompletionParameters{
			MaxTokens:   &r.MaxTokensToSample,
			Temperature: r.Temperature,
			TopP:        r.TopP,
			Stop:        r.StopSequences,
		},
	}

	// Add extra params if present
	if r.TopK != nil {
		bifrostReq.Params.ExtraParams = map[string]interface{}{
			"top_k": *r.TopK,
		}
	}

	return bifrostReq
}

func (response *AnthropicTextResponse) ToBifrostResponse() *schemas.BifrostResponse {
	if response == nil {
		return nil
	}
	return &schemas.BifrostResponse{
		ID: response.ID,
		Choices: []schemas.BifrostChatResponseChoice{
			{
				Index: 0,
				BifrostTextCompletionResponseChoice: &schemas.BifrostTextCompletionResponseChoice{
					Text: &response.Completion,
				},
			},
		},
		Usage: &schemas.LLMUsage{
			PromptTokens:     response.Usage.InputTokens,
			CompletionTokens: response.Usage.OutputTokens,
			TotalTokens:      response.Usage.InputTokens + response.Usage.OutputTokens,
		},
		Model: response.Model,
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType: schemas.TextCompletionRequest,
			Provider:    schemas.Anthropic,
		},
	}
}

// ToAnthropicTextCompletionResponse converts a BifrostResponse back to Anthropic text completion format
func ToAnthropicTextCompletionResponse(bifrostResp *schemas.BifrostResponse) *AnthropicTextResponse {
	if bifrostResp == nil {
		return nil
	}

	anthropicResp := &AnthropicTextResponse{
		ID:    bifrostResp.ID,
		Type:  "completion",
		Model: bifrostResp.Model,
	}

	// Convert choices to completion text
	if len(bifrostResp.Choices) > 0 {
		choice := bifrostResp.Choices[0] // Anthropic text API typically returns one choice

		if choice.BifrostTextCompletionResponseChoice != nil && choice.BifrostTextCompletionResponseChoice.Text != nil {
			anthropicResp.Completion = *choice.BifrostTextCompletionResponseChoice.Text
		}
	}

	// Convert usage information
	if bifrostResp.Usage != nil {
		anthropicResp.Usage.InputTokens = bifrostResp.Usage.PromptTokens
		anthropicResp.Usage.OutputTokens = bifrostResp.Usage.CompletionTokens
	}

	return anthropicResp
}
