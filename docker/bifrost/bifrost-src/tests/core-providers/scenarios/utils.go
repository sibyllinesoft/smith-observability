package scenarios

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// Shared test texts for TTS->SST round-trip validation
const (
	// Basic test text for simple round-trip validation
	TTSTestTextBasic = "Hello, this is a test of speech synthesis from Bifrost."

	// Medium length text with punctuation for comprehensive testing
	TTSTestTextMedium = "Testing speech synthesis and transcription round-trip. This text includes punctuation, numbers like 123, and technical terms."

	// Short technical text for WAV format testing
	TTSTestTextTechnical = "Bifrost AI gateway processes audio requests efficiently."
)

// GetProviderVoice returns an appropriate voice for the given provider
func GetProviderVoice(provider schemas.ModelProvider, voiceType string) string {
	switch provider {
	case schemas.OpenAI:
		switch voiceType {
		case "primary":
			return "alloy"
		case "secondary":
			return "nova"
		case "tertiary":
			return "echo"
		default:
			return "alloy"
		}
	case schemas.Gemini:
		switch voiceType {
		case "primary":
			return "achernar"
		case "secondary":
			return "aoede"
		case "tertiary":
			return "charon"
		default:
			return "achernar"
		}
	default:
		// Default to OpenAI voices for other providers
		switch voiceType {
		case "primary":
			return "alloy"
		case "secondary":
			return "nova"
		case "tertiary":
			return "echo"
		default:
			return "alloy"
		}
	}
}

type SampleToolType string

const (
	SampleToolTypeWeather   SampleToolType = "weather"
	SampleToolTypeCalculate SampleToolType = "calculate"
	SampleToolTypeTime      SampleToolType = "time"
)

var SampleToolFunctions = map[SampleToolType]*schemas.ChatToolFunction{
	SampleToolTypeWeather:   WeatherToolFunction,
	SampleToolTypeCalculate: CalculatorToolFunction,
	SampleToolTypeTime:      TimeToolFunction,
}

var sampleToolDescriptions = map[SampleToolType]string{
	SampleToolTypeWeather:   "Get the current weather in a given location",
	SampleToolTypeCalculate: "Perform basic mathematical calculations",
	SampleToolTypeTime:      "Get the current time in a specific timezone",
}

var WeatherToolFunction = &schemas.ChatToolFunction{
	Parameters: &schemas.ToolFunctionParameters{
		Type: "object",
		Properties: map[string]interface{}{
			"location": map[string]interface{}{
				"type":        "string",
				"description": "The city and state, e.g. San Francisco, CA",
			},
			"unit": map[string]interface{}{
				"type": "string",
				"enum": []string{"celsius", "fahrenheit"},
			},
		},
		Required: []string{"location"},
	},
}

var CalculatorToolFunction = &schemas.ChatToolFunction{
	Parameters: &schemas.ToolFunctionParameters{
		Type: "object",
		Properties: map[string]interface{}{
			"expression": map[string]interface{}{
				"type":        "string",
				"description": "The mathematical expression to evaluate, e.g. '2 + 3' or '10 * 5'",
			},
		},
		Required: []string{"expression"},
	},
}

var TimeToolFunction = &schemas.ChatToolFunction{
	Parameters: &schemas.ToolFunctionParameters{
		Type: "object",
		Properties: map[string]interface{}{
			"timezone": map[string]interface{}{
				"type":        "string",
				"description": "The timezone identifier, e.g. 'America/New_York' or 'UTC'",
			},
		},
		Required: []string{"timezone"},
	},
}

func GetSampleChatTool(toolName SampleToolType) *schemas.ChatTool {
	function, ok := SampleToolFunctions[toolName]
	if !ok {
		return nil
	}

	description, ok := sampleToolDescriptions[toolName]
	if !ok {
		return nil
	}

	return &schemas.ChatTool{
		Type: "function",
		Function: &schemas.ChatToolFunction{
			Name:        string(toolName),
			Description: bifrost.Ptr(description),
			Parameters:  function.Parameters,
		},
	}
}

func GetSampleResponsesTool(toolName SampleToolType) *schemas.ResponsesTool {
	function, ok := SampleToolFunctions[toolName]
	if !ok {
		return nil
	}

	description, ok := sampleToolDescriptions[toolName]
	if !ok {
		return nil
	}

	return &schemas.ResponsesTool{
		Type:        "function",
		Name:        bifrost.Ptr(string(toolName)),
		Description: bifrost.Ptr(description),
		ResponsesToolFunction: &schemas.ResponsesToolFunction{
			Parameters: function.Parameters,
		},
	}
}

// Test image of an ant
const TestImageURL = "https://upload.wikimedia.org/wikipedia/commons/thumb/f/fb/Carpenter_ant_Tanzania_crop.jpg/1200px-Carpenter_ant_Tanzania_crop.png"

// Test image of the Eiffel Tower
const TestImageURL2 = "https://upload.wikimedia.org/wikipedia/commons/thumb/4/4b/La_Tour_Eiffel_vue_de_la_Tour_Saint-Jacques%2C_Paris_ao%C3%BBt_2014_%282%29.jpg/960px-La_Tour_Eiffel_vue_de_la_Tour_Saint-Jacques%2C_Paris_ao%C3%BBt_2014_%282%29.png"

// Test image base64 of a grey solid
const TestImageBase64 = "data:image/jpeg;base64,/9j/4AAQSkZJRgABAQEAYABgAAD/2wBDAAgGBgcGBQgHBwcJCQgKDBQNDAsLDBkSEw8UHRofHh0aHBwgJC4nICIsIxwcKDcpLDAxNDQ0Hyc5PTgyPC4zNDL/2wBDAQkJCQwLDBgNDRgyIRwhMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjL/wAARCAAIAAoDASIAAhEBAxEB/8QAFQEBAQAAAAAAAAAAAAAAAAAAAAb/xAAUEAEAAAAAAAAAAAAAAAAAAAAA/8QAFQEBAQAAAAAAAAAAAAAAAAAAAAX/xAAUEQEAAAAAAAAAAAAAAAAAAAAA/9oADAMBAAIRAxEAPwCdABmX/9k="

// GetLionBase64Image loads and returns the lion base64 image data from file
func GetLionBase64Image() (string, error) {
	data, err := os.ReadFile("scenarios/media/lion_base64.txt")
	if err != nil {
		return "", err
	}
	return "data:image/png;base64," + string(data), nil
}

// CreateSpeechInput creates a basic speech input for testing
func CreateSpeechRequest(text, voice, format string) *schemas.BifrostSpeechRequest {
	return &schemas.BifrostSpeechRequest{
		Input: &schemas.SpeechInput{
			Input: text,
		},
		Params: &schemas.SpeechParameters{
			VoiceConfig: &schemas.SpeechVoiceInput{
				Voice: &voice,
			},
			ResponseFormat: format,
		},
	}
}

// CreateTranscriptionInput creates a basic transcription input for testing
func CreateTranscriptionInput(audioData []byte, language, responseFormat *string) *schemas.BifrostTranscriptionRequest {
	return &schemas.BifrostTranscriptionRequest{
		Input: &schemas.TranscriptionInput{
			File: audioData,
		},
		Params: &schemas.TranscriptionParameters{
			Language:       language,
			ResponseFormat: responseFormat,
		},
	}
}

// Helper functions for creating requests
func CreateBasicChatMessage(content string) schemas.ChatMessage {
	return schemas.ChatMessage{
		Role: schemas.ChatMessageRoleUser,
		Content: &schemas.ChatMessageContent{
			ContentStr: bifrost.Ptr(content),
		},
	}
}

func CreateBasicResponsesMessage(content string) schemas.ResponsesMessage {
	return schemas.ResponsesMessage{
		Type: bifrost.Ptr(schemas.ResponsesMessageTypeMessage),
		Role: bifrost.Ptr(schemas.ResponsesInputMessageRoleUser),
		Content: &schemas.ResponsesMessageContent{
			ContentStr: bifrost.Ptr(content),
		},
	}
}

func CreateImageChatMessage(text, imageURL string) schemas.ChatMessage {
	return schemas.ChatMessage{
		Role: schemas.ChatMessageRoleUser,
		Content: &schemas.ChatMessageContent{
			ContentBlocks: []schemas.ChatContentBlock{
				{Type: schemas.ChatContentBlockTypeText, Text: bifrost.Ptr(text)},
				{Type: schemas.ChatContentBlockTypeImage, ImageURLStruct: &schemas.ChatInputImage{URL: imageURL}},
			},
		},
	}
}

func CreateImageResponsesMessage(text, imageURL string) schemas.ResponsesMessage {
	return schemas.ResponsesMessage{
		Type: bifrost.Ptr(schemas.ResponsesMessageTypeMessage),
		Role: bifrost.Ptr(schemas.ResponsesInputMessageRoleUser),
		Content: &schemas.ResponsesMessageContent{
			ContentBlocks: []schemas.ResponsesMessageContentBlock{
				{Type: schemas.ResponsesInputMessageContentBlockTypeText, Text: bifrost.Ptr(text)},
				{Type: schemas.ResponsesInputMessageContentBlockTypeImage,
					ResponsesInputMessageContentBlockImage: &schemas.ResponsesInputMessageContentBlockImage{
						ImageURL: bifrost.Ptr(imageURL),
					},
				},
			},
		},
	}
}

func CreateToolChatMessage(content string, toolCallID string) schemas.ChatMessage {
	return schemas.ChatMessage{
		Role: schemas.ChatMessageRoleTool,
		Content: &schemas.ChatMessageContent{
			ContentStr: bifrost.Ptr(content),
		},
		ChatToolMessage: &schemas.ChatToolMessage{
			ToolCallID: bifrost.Ptr(toolCallID),
		},
	}
}

func CreateToolResponsesMessage(content string, toolCallID string) schemas.ResponsesMessage {
	return schemas.ResponsesMessage{
		Type: bifrost.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
		// Note: function_call_output messages don't have a role field per OpenAI API
		ResponsesToolMessage: &schemas.ResponsesToolMessage{
			CallID: bifrost.Ptr(toolCallID),
			// Set ResponsesFunctionToolCallOutput for OpenAI's native Responses API
			ResponsesFunctionToolCallOutput: &schemas.ResponsesFunctionToolCallOutput{
				ResponsesFunctionToolCallOutputStr: bifrost.Ptr(content),
			},
		},
	}
}

// GetResultContent returns the string content from a BifrostResponse
// It looks through all choices and returns content from the first choice that has any
func GetResultContent(result *schemas.BifrostResponse) string {
	if result == nil || (result.Choices == nil && result.ResponsesResponse == nil) {
		return ""
	}

	if result.Choices != nil {
		// Try to find content from any choice, prioritizing non-empty content
		for _, choice := range result.Choices {
			if choice.BifrostTextCompletionResponseChoice != nil && choice.BifrostTextCompletionResponseChoice.Text != nil {
				return *choice.Text
			}

			if choice.Message.Content != nil {
				// Check if content has any data (either ContentStr or ContentBlocks)
				if choice.Message.Content.ContentStr != nil || choice.Message.Content.ContentBlocks != nil {
					if choice.Message.Content.ContentStr != nil && *choice.Message.Content.ContentStr != "" {
						return *choice.Message.Content.ContentStr
					} else if choice.Message.Content.ContentBlocks != nil {
						var builder strings.Builder
						for _, block := range choice.Message.Content.ContentBlocks {
							if block.Text != nil {
								builder.WriteString(*block.Text)
							}
						}
						content := builder.String()
						if content != "" {
							return content
						}
					}
				}
			}
		}

		// Fallback to first choice if no content found
		if len(result.Choices) > 0 {
			choice := result.Choices[0]
			if choice.Message.Content != nil {
				if choice.Message.Content.ContentStr != nil || choice.Message.Content.ContentBlocks != nil {
					if choice.Message.Content.ContentStr != nil {
						return *choice.Message.Content.ContentStr
					} else if choice.Message.Content.ContentBlocks != nil {
						var builder strings.Builder
						for _, block := range choice.Message.Content.ContentBlocks {
							if block.Text != nil {
								builder.WriteString(*block.Text)
							}
						}
						return builder.String()
					}
				}
			}
		}
	} else if result.ResponsesResponse != nil {
		for _, output := range result.ResponsesResponse.Output {
			if output.Content != nil {
				if output.Content.ContentStr != nil {
					return *output.Content.ContentStr
				} else if output.Content.ContentBlocks != nil {
					var builder strings.Builder
					for _, block := range output.Content.ContentBlocks {
						if block.Text != nil {
							builder.WriteString(*block.Text)
						}
					}
					content := builder.String()
					if content != "" {
						return content
					}
				}
			}
		}
	}
	return ""
}

// ToolCallInfo represents extracted tool call information for both API formats
type ToolCallInfo struct {
	Name      string
	Arguments string
	ID        string
}

// ExtractToolCalls extracts tool call information from a BifrostResponse for both Chat Completions and Responses API
func ExtractToolCalls(response *schemas.BifrostResponse) []ToolCallInfo {
	if response == nil {
		return nil
	}

	var toolCalls []ToolCallInfo

	// Extract from Chat Completions API format
	if response.Choices != nil {
		for _, choice := range response.Choices {
			if choice.Message.ChatAssistantMessage != nil &&
				choice.Message.ChatAssistantMessage.ToolCalls != nil {

				chatToolCalls := choice.Message.ChatAssistantMessage.ToolCalls
				for _, toolCall := range chatToolCalls {
					info := ToolCallInfo{
						Arguments: toolCall.Function.Arguments,
					}
					if toolCall.Function.Name != nil {
						info.Name = *toolCall.Function.Name
					}
					if toolCall.ID != nil {
						info.ID = *toolCall.ID
					}

					// Only append if we have at least some meaningful data
					// (Name, ID, or non-empty Arguments)
					if info.Name != "" || info.ID != "" || info.Arguments != "" {
						toolCalls = append(toolCalls, info)
					}
				}
			}
		}
	}

	// Extract from Responses API format
	if response.ResponsesResponse != nil {
		for _, output := range response.ResponsesResponse.Output {
			// Check for function calls in assistant messages
			// Only process if this is a function_call type with ResponsesToolMessage
			if output.ResponsesToolMessage != nil &&
				output.Type != nil &&
				*output.Type == "function_call" {

				info := ToolCallInfo{}

				if output.Name != nil {
					info.Name = *output.Name
				}
				if output.ResponsesToolMessage.CallID != nil {
					info.ID = *output.ResponsesToolMessage.CallID
				}

				// Get arguments from embedded function tool call if available
				if output.Arguments != nil {
					info.Arguments = *output.Arguments
				}

				// Only append if we have at least one of Name, ID, or Arguments
				if info.Name != "" || info.ID != "" || info.Arguments != "" {
					toolCalls = append(toolCalls, info)
				}
			}
		}
	}

	return toolCalls
}

// getEmbeddingVector extracts the float32 vector from a BifrostEmbeddingResponse
func getEmbeddingVector(embedding schemas.BifrostEmbeddingResponse) ([]float32, error) {
	if embedding.EmbeddingArray != nil {
		return embedding.EmbeddingArray, nil
	}

	if embedding.Embedding2DArray != nil {
		// For 2D arrays, return the first vector
		if len(embedding.Embedding2DArray) > 0 {
			return embedding.Embedding2DArray[0], nil
		}
		return nil, fmt.Errorf("2D embedding array is empty")
	}

	if embedding.EmbeddingStr != nil {
		return nil, fmt.Errorf("string embeddings not supported for vector extraction")
	}

	return nil, fmt.Errorf("no valid embedding data found")
}

// --- Additional test helpers appended below (imported on demand) ---

// NOTE: importing context, os, testing only in this block to avoid breaking existing imports.
// We duplicate types by fully qualifying to not touch import list above.

// GenerateTTSAudioForTest generates real audio using TTS and writes a temp file.
// Returns audio bytes and temp filepath. Caller’s t will clean it up.
func GenerateTTSAudioForTest(ctx context.Context, t *testing.T, client *bifrost.Bifrost, provider schemas.ModelProvider, ttsModel string, text string, voiceType string, format string) ([]byte, string) {
	// inline import guard comment: context/testing/os are required at call sites; Go compiler will include them.
	voice := GetProviderVoice(provider, voiceType)
	if voice == "" {
		voice = GetProviderVoice(provider, "primary")
	}
	if format == "" {
		format = "mp3"
	}

	req := &schemas.BifrostSpeechRequest{
		Provider: provider,
		Model:    ttsModel,
		Input:    &schemas.SpeechInput{Input: text},
		Params: &schemas.SpeechParameters{
			VoiceConfig: &schemas.SpeechVoiceInput{
				Voice: &voice,
			},
			ResponseFormat: format,
		},
	}

	resp, err := client.SpeechRequest(ctx, req)
	if err != nil {
		t.Fatalf("TTS request failed: %v", err)
	}
	if resp == nil || resp.Speech == nil || len(resp.Speech.Audio) == 0 {
		t.Fatalf("TTS response missing audio data")
	}

	suffix := "." + format
	f, cerr := os.CreateTemp("", "bifrost-tts-*"+suffix)
	if cerr != nil {
		t.Fatalf("failed to create temp audio file: %v", cerr)
	}
	tempPath := f.Name()
	if _, werr := f.Write(resp.Speech.Audio); werr != nil {
		_ = f.Close()
		t.Fatalf("failed to write temp audio file: %v", werr)
	}
	_ = f.Close()

	t.Cleanup(func() { _ = os.Remove(tempPath) })

	return resp.Speech.Audio, tempPath
}

func GetErrorMessage(err *schemas.BifrostError) string {
	if err == nil {
		return ""
	}

	errorType := ""
	if err.Type != nil && *err.Type != "" {
		errorType = *err.Type
	}

	if errorType == "" && err.Error.Type != nil && *err.Error.Type != "" {
		errorType = *err.Error.Type
	}

	errorCode := ""
	if err.Error.Code != nil && *err.Error.Code != "" {
		errorCode = *err.Error.Code
	}

	errorMessage := err.Error.Message

	errorString := fmt.Sprintf("%s %s: %s", errorType, errorCode, errorMessage)

	return errorString
}
