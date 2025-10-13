// Package providers implements various LLM providers and their utility functions.
// This file contains the Gemini provider implementation.
package providers

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/core/schemas/providers/gemini"
	"github.com/maximhq/bifrost/core/schemas/providers/openai"
	"github.com/valyala/fasthttp"
)

type GeminiProvider struct {
	logger               schemas.Logger                // Logger for provider operations
	client               *fasthttp.Client              // HTTP client for API requests
	streamClient         *http.Client                  // HTTP client for streaming requests
	networkConfig        schemas.NetworkConfig         // Network configuration including extra headers
	sendBackRawResponse  bool                          // Whether to include raw response in BifrostResponse
	customProviderConfig *schemas.CustomProviderConfig // Custom provider config
}

// NewGeminiProvider creates a new Gemini provider instance.
// It initializes the HTTP client with the provided configuration.
// The client is configured with timeouts, concurrency limits, and optional proxy settings.
func NewGeminiProvider(config *schemas.ProviderConfig, logger schemas.Logger) *GeminiProvider {
	config.CheckAndSetDefaults()

	client := &fasthttp.Client{
		ReadTimeout:     time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds),
		WriteTimeout:    time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds),
		MaxConnsPerHost: config.ConcurrencyAndBufferSize.Concurrency,
	}

	// Initialize streaming HTTP client
	streamClient := &http.Client{
		Timeout: time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds),
	}

	// Configure proxy if provided
	client = configureProxy(client, config.ProxyConfig, logger)

	// Set default BaseURL if not provided
	if config.NetworkConfig.BaseURL == "" {
		config.NetworkConfig.BaseURL = "https://generativelanguage.googleapis.com/v1beta"
	}
	config.NetworkConfig.BaseURL = strings.TrimRight(config.NetworkConfig.BaseURL, "/")

	return &GeminiProvider{
		logger:               logger,
		client:               client,
		streamClient:         streamClient,
		networkConfig:        config.NetworkConfig,
		customProviderConfig: config.CustomProviderConfig,
		sendBackRawResponse:  config.SendBackRawResponse,
	}
}

// GetProviderKey returns the provider identifier for Gemini.
func (provider *GeminiProvider) GetProviderKey() schemas.ModelProvider {
	return getProviderName(schemas.Gemini, provider.customProviderConfig)
}

// TextCompletion is not supported by the Gemini provider.
func (provider *GeminiProvider) TextCompletion(ctx context.Context, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("text completion", string(provider.GetProviderKey()))
}

// TextCompletionStream performs a streaming text completion request to Gemini's API.
// It formats the request, sends it to Gemini, and processes the response.
// Returns a channel of BifrostStream objects or an error if the request fails.
func (provider *GeminiProvider) TextCompletionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("text completion stream", "gemini")
}

// ChatCompletion performs a chat completion request to the Gemini API.
func (provider *GeminiProvider) ChatCompletion(ctx context.Context, key schemas.Key, request *schemas.BifrostChatRequest) (*schemas.BifrostResponse, *schemas.BifrostError) {
	// Check if chat completion is allowed for this provider
	if err := checkOperationAllowed(schemas.Gemini, provider.customProviderConfig, schemas.ChatCompletionRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	// Use centralized OpenAI converter since Gemini uses OpenAI-compatible endpoints
	reqBody := openai.ToOpenAIChatRequest(request)
	if reqBody == nil {
		return nil, newBifrostOperationError("chat completion input is not provided", nil, providerName)
	}

	jsonBody, err := sonic.Marshal(reqBody)
	if err != nil {
		return nil, newBifrostOperationError(schemas.ErrProviderJSONMarshaling, err, providerName)
	}

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Set any extra headers from network config
	setExtraHeaders(req, provider.networkConfig.ExtraHeaders, nil)

	req.SetRequestURI(provider.networkConfig.BaseURL + "/openai/chat/completions")
	req.Header.SetMethod("POST")
	req.Header.SetContentType("application/json")
	req.Header.Set("Authorization", "Bearer "+key.Value)

	req.SetBody(jsonBody)

	// Make request
	latency, bifrostErr := makeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		var errorResp map[string]interface{}
		bifrostErr := handleProviderAPIError(resp, &errorResp)
		bifrostErr.Error.Message = fmt.Sprintf("%s error: %v", providerName, errorResp)
		return nil, bifrostErr
	}

	responseBody := resp.Body()

	// Pre-allocate response structs from pools
	// response := acquireGeminiResponse()
	// defer releaseGeminiResponse(response)
	response := &schemas.BifrostResponse{}

	// Use enhanced response handler with pre-allocated response
	rawResponse, bifrostErr := handleProviderResponse(responseBody, response, provider.sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	for _, choice := range response.Choices {
		if choice.BifrostNonStreamResponseChoice.Message.ChatAssistantMessage != nil && choice.BifrostNonStreamResponseChoice.Message.ChatAssistantMessage.ToolCalls != nil {
			for i, toolCall := range choice.BifrostNonStreamResponseChoice.Message.ChatAssistantMessage.ToolCalls {
				if (toolCall.ID == nil || *toolCall.ID == "") && toolCall.Function.Name != nil && *toolCall.Function.Name != "" {
					id := *toolCall.Function.Name
					(choice.BifrostNonStreamResponseChoice.Message.ChatAssistantMessage.ToolCalls)[i].ID = &id
				}
			}
		}
	}

	response.ExtraFields.Provider = providerName
	response.ExtraFields.Latency = latency.Milliseconds()

	if provider.sendBackRawResponse {
		response.ExtraFields.RawResponse = rawResponse
	}

	return response, nil
}

// ChatCompletionStream performs a streaming chat completion request to the Gemini API.
// It supports real-time streaming of responses using Server-Sent Events (SSE).
// Uses Gemini's OpenAI-compatible streaming format.
// Returns a channel containing BifrostResponse objects representing the stream or an error if the request fails.
func (provider *GeminiProvider) ChatCompletionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostChatRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	// Check if chat completion stream is allowed for this provider
	if err := checkOperationAllowed(schemas.Gemini, provider.customProviderConfig, schemas.ChatCompletionStreamRequest); err != nil {
		return nil, err
	}

	// Use shared OpenAI-compatible streaming logic
	return handleOpenAIChatCompletionStreaming(
		ctx,
		provider.streamClient,
		provider.networkConfig.BaseURL+"/openai/chat/completions",
		request,
		map[string]string{"Authorization": "Bearer " + key.Value},
		provider.networkConfig.ExtraHeaders,
		provider.sendBackRawResponse,
		provider.GetProviderKey(),
		postHookRunner,
		provider.logger,
	)
}

// Responses performs a chat completion request to Anthropic's API.
// It formats the request, sends it to Anthropic, and processes the response.
// Returns a BifrostResponse containing the completion results or an error if the request fails.
func (provider *GeminiProvider) Responses(ctx context.Context, key schemas.Key, request *schemas.BifrostResponsesRequest) (*schemas.BifrostResponse, *schemas.BifrostError) {
	response, err := provider.ChatCompletion(ctx, key, request.ToChatRequest())
	if err != nil {
		return nil, err
	}

	response.ToResponsesOnly()
	response.ExtraFields.RequestType = schemas.ResponsesRequest
	response.ExtraFields.Provider = provider.GetProviderKey()
	response.ExtraFields.ModelRequested = request.Model

	return response, nil
}

func (provider *GeminiProvider) ResponsesStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostResponsesRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return provider.ChatCompletionStream(
		ctx,
		getResponsesChunkConverterCombinedPostHookRunner(postHookRunner),
		key,
		request.ToChatRequest(),
	)
}

// Embedding performs an embedding request to the Gemini API.
func (provider *GeminiProvider) Embedding(ctx context.Context, key schemas.Key, request *schemas.BifrostEmbeddingRequest) (*schemas.BifrostResponse, *schemas.BifrostError) {
	// Check if embedding is allowed for this provider
	if err := checkOperationAllowed(schemas.Gemini, provider.customProviderConfig, schemas.EmbeddingRequest); err != nil {
		return nil, err
	}

	// Use the shared embedding request handler
	return handleOpenAIEmbeddingRequest(
		ctx,
		provider.client,
		provider.networkConfig.BaseURL+"/openai/embeddings",
		request,
		key,
		provider.networkConfig.ExtraHeaders,
		provider.GetProviderKey(),
		provider.sendBackRawResponse,
		provider.logger,
	)
}

func (provider *GeminiProvider) Speech(ctx context.Context, key schemas.Key, request *schemas.BifrostSpeechRequest) (*schemas.BifrostResponse, *schemas.BifrostError) {
	// Check if speech is allowed for this provider
	if err := checkOperationAllowed(schemas.Gemini, provider.customProviderConfig, schemas.SpeechRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	// Validate input
	if request == nil || request.Input == nil || request.Input.Input == "" {
		return nil, newBifrostOperationError("invalid speech input: no text provided", fmt.Errorf("empty text input"), providerName)
	}

	// Prepare request body using speech-specific function
	requestBody := gemini.ToGeminiSpeechRequest(request, []string{"AUDIO"})
	if requestBody == nil {
		return nil, newBifrostOperationError("speech input is not provided", nil, providerName)
	}

	jsonBody, err := sonic.Marshal(requestBody)
	if err != nil {
		return nil, newBifrostOperationError(schemas.ErrProviderJSONMarshaling, err, providerName)
	}

	// Use common request function
	geminiResponse, rawResponse, latency, bifrostErr := provider.completeRequest(ctx, request.Model, key, jsonBody, ":generateContent")
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	bifrostResponse := geminiResponse.ToBifrostResponse()

	// Set ExtraFields
	bifrostResponse.ExtraFields.Provider = providerName
	bifrostResponse.ExtraFields.ModelRequested = request.Model
	bifrostResponse.ExtraFields.RequestType = schemas.SpeechRequest
	bifrostResponse.ExtraFields.Latency = latency.Milliseconds()

	if provider.sendBackRawResponse {
		bifrostResponse.ExtraFields.RawResponse = rawResponse
	}

	return bifrostResponse, nil
}

func (provider *GeminiProvider) SpeechStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostSpeechRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	// Check if speech stream is allowed for this provider
	if err := checkOperationAllowed(schemas.Gemini, provider.customProviderConfig, schemas.SpeechStreamRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	if request == nil || request.Input == nil || request.Input.Input == "" {
		return nil, newBifrostOperationError("speech input is not provided", fmt.Errorf("empty text input"), providerName)
	}

	// Prepare request body using speech-specific function
	requestBody := gemini.ToGeminiSpeechRequest(request, []string{"AUDIO"})
	if requestBody == nil {
		return nil, newBifrostOperationError("speech input is not provided", nil, providerName)
	}

	jsonBody, err := sonic.Marshal(requestBody)
	if err != nil {
		return nil, newBifrostOperationError(schemas.ErrProviderJSONMarshaling, err, providerName)
	}

	// Create HTTP request for streaming
	req, err := http.NewRequestWithContext(ctx, "POST", provider.networkConfig.BaseURL+"/models/"+request.Model+":streamGenerateContent?alt=sse", bytes.NewReader(jsonBody))
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, &schemas.BifrostError{
				IsBifrostError: false,
				Error: &schemas.ErrorField{
					Type:    schemas.Ptr(schemas.RequestCancelled),
					Message: schemas.ErrRequestCancelled,
					Error:   err,
				},
			}
		}
		if errors.Is(err, http.ErrHandlerTimeout) || errors.Is(err, context.DeadlineExceeded) {
			return nil, newBifrostOperationError(schemas.ErrProviderRequestTimedOut, err, providerName)
		}
		return nil, newBifrostOperationError(schemas.ErrProviderRequest, err, providerName)
	}

	// Set any extra headers from network config
	setExtraHeadersHTTP(req, provider.networkConfig.ExtraHeaders, nil)

	// Set headers for streaming
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", key.Value)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")

	// Make the request
	resp, err := provider.streamClient.Do(req)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, &schemas.BifrostError{
				IsBifrostError: false,
				Error: &schemas.ErrorField{
					Type:    schemas.Ptr(schemas.RequestCancelled),
					Message: schemas.ErrRequestCancelled,
					Error:   err,
				},
			}
		}
		if errors.Is(err, http.ErrHandlerTimeout) || errors.Is(err, context.DeadlineExceeded) {
			return nil, newBifrostOperationError(schemas.ErrProviderRequestTimedOut, err, providerName)
		}
		return nil, newBifrostOperationError(schemas.ErrProviderRequest, err, providerName)
	}

	// Check for HTTP errors
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, parseStreamGeminiError(providerName, resp)
	}

	// Create response channel
	responseChan := make(chan *schemas.BifrostStream, schemas.DefaultStreamBufferSize)

	// Start streaming in a goroutine
	go func() {
		defer close(responseChan)
		defer resp.Body.Close()

		scanner := bufio.NewScanner(resp.Body)
		// Increase buffer size to handle large chunks (especially for audio data)
		buf := make([]byte, 0, 64*1024) // 64KB buffer
		scanner.Buffer(buf, 1024*1024)  // Allow up to 1MB tokens
		chunkIndex := -1
		usage := &schemas.AudioLLMUsage{}
		startTime := time.Now()
		lastChunkTime := startTime

		for scanner.Scan() {
			line := scanner.Text()

			// Skip empty lines
			if line == "" {
				continue
			}

			var jsonData string
			// Parse SSE data
			if strings.HasPrefix(line, "data: ") {
				jsonData = strings.TrimPrefix(line, "data: ")
			} else {
				// Handle raw JSON errors (without "data: " prefix)
				jsonData = line
			}

			// Skip empty data
			if strings.TrimSpace(jsonData) == "" {
				continue
			}

			// Process chunk using shared function
			geminiResponse, err := processGeminiStreamChunk(jsonData)
			if err != nil {
				if strings.Contains(err.Error(), "gemini api error") {
					// Handle API error
					bifrostErr := &schemas.BifrostError{
						Type:           schemas.Ptr("gemini_api_error"),
						IsBifrostError: false,
						Error: &schemas.ErrorField{
							Message: err.Error(),
							Error:   err,
						},
						ExtraFields: schemas.BifrostErrorExtraFields{
							RequestType:    schemas.SpeechStreamRequest,
							Provider:       providerName,
							ModelRequested: request.Model,
						},
					}
					ctx = context.WithValue(ctx, schemas.BifrostContextKeyStreamEndIndicator, true)
					processAndSendBifrostError(ctx, postHookRunner, bifrostErr, responseChan, provider.logger)
					return
				}
				provider.logger.Warn(fmt.Sprintf("Failed to process chunk: %v", err))
				continue
			}

			// Extract audio data from Gemini response for regular chunks
			var audioChunk []byte
			if len(geminiResponse.Candidates) > 0 {
				candidate := geminiResponse.Candidates[0]
				if candidate.Content != nil && len(candidate.Content.Parts) > 0 {
					var buf []byte
					for _, part := range candidate.Content.Parts {
						if part.InlineData != nil && part.InlineData.Data != nil {
							buf = append(buf, part.InlineData.Data...)
						}
					}
					if len(buf) > 0 {
						audioChunk = buf
					}
				}
			}

			// Check if this is the final chunk (has finishReason)
			if len(geminiResponse.Candidates) > 0 && (geminiResponse.Candidates[0].FinishReason != "" || geminiResponse.UsageMetadata != nil) {
				// Extract usage metadata using shared function
				inputTokens, outputTokens, totalTokens := extractGeminiUsageMetadata(geminiResponse)
				usage.InputTokens = inputTokens
				usage.OutputTokens = outputTokens
				usage.TotalTokens = totalTokens
			}

			// Only send response if we have actual audio content
			if len(audioChunk) > 0 {
				chunkIndex++

				// Create Bifrost speech response for streaming
				response := &schemas.BifrostResponse{
					Object: "audio.speech.chunk",
					Model:  request.Model,
					Speech: &schemas.BifrostSpeech{
						Audio: audioChunk,
						BifrostSpeechStreamResponse: &schemas.BifrostSpeechStreamResponse{
							Type: "audio.speech.chunk",
						},
					},
					ExtraFields: schemas.BifrostResponseExtraFields{
						RequestType:    schemas.SpeechStreamRequest,
						Provider:       providerName,
						ModelRequested: request.Model,
						ChunkIndex:     chunkIndex,
						Latency:        time.Since(lastChunkTime).Milliseconds(),
					},
				}
				lastChunkTime = time.Now()

				if provider.sendBackRawResponse {
					response.ExtraFields.RawResponse = jsonData
				}

				// Process response through post-hooks and send to channel
				processAndSendResponse(ctx, postHookRunner, response, responseChan, provider.logger)
			}
		}

		// Handle scanner errors
		if err := scanner.Err(); err != nil {
			provider.logger.Warn(fmt.Sprintf("Error reading stream: %v", err))
			processAndSendError(ctx, postHookRunner, err, responseChan, schemas.SpeechStreamRequest, providerName, request.Model, provider.logger)
		} else {
			response := &schemas.BifrostResponse{
				Object: "audio.speech.chunk",
				Speech: &schemas.BifrostSpeech{
					Usage: usage,
				},
				ExtraFields: schemas.BifrostResponseExtraFields{
					RequestType:    schemas.SpeechStreamRequest,
					Provider:       providerName,
					ModelRequested: request.Model,
					ChunkIndex:     chunkIndex + 1,
					Latency:        time.Since(startTime).Milliseconds(),
				},
			}

			ctx = context.WithValue(ctx, schemas.BifrostContextKeyStreamEndIndicator, true)
			handleStreamEndWithSuccess(ctx, response, postHookRunner, responseChan, provider.logger)
		}
	}()

	return responseChan, nil
}

func (provider *GeminiProvider) Transcription(ctx context.Context, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (*schemas.BifrostResponse, *schemas.BifrostError) {
	// Check if transcription is allowed for this provider
	if err := checkOperationAllowed(schemas.Gemini, provider.customProviderConfig, schemas.TranscriptionRequest); err != nil {
		return nil, err
	}
	providerName := provider.GetProviderKey()
	// Check if input is provided
	if request.Input == nil || request.Input.File == nil {
		return nil, newBifrostOperationError("transcription input is not provided", fmt.Errorf("empty file input"), providerName)
	}
	// Check file size limit (Gemini has a 20MB limit for inline data)
	const maxFileSize = 20 * 1024 * 1024 // 20MB

	if len(request.Input.File) > maxFileSize {
		return nil, newBifrostOperationError("audio file too large for inline transcription", fmt.Errorf("file size %d bytes exceeds 20MB limit", len(request.Input.File)), providerName)
	}

	// Prepare request body using transcription-specific function
	requestBody := gemini.ToGeminiTranscriptionRequest(request)
	if requestBody == nil {
		return nil, newBifrostOperationError("transcription input is not provided", nil, providerName)
	}

	jsonBody, err := sonic.Marshal(requestBody)
	if err != nil {
		return nil, newBifrostOperationError(schemas.ErrProviderJSONMarshaling, err, providerName)
	}

	// Use common request function
	geminiResponse, rawResponse, latency, bifrostErr := provider.completeRequest(ctx, request.Model, key, jsonBody, ":generateContent")
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	bifrostResponse := geminiResponse.ToBifrostResponse()

	// Set ExtraFields
	bifrostResponse.ExtraFields.Provider = providerName
	bifrostResponse.ExtraFields.ModelRequested = request.Model
	bifrostResponse.ExtraFields.RequestType = schemas.TranscriptionRequest
	bifrostResponse.ExtraFields.Latency = latency.Milliseconds()

	if provider.sendBackRawResponse {
		bifrostResponse.ExtraFields.RawResponse = rawResponse
	}

	return bifrostResponse, nil
}

func (provider *GeminiProvider) TranscriptionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	// Check if transcription stream is allowed for this provider
	if err := checkOperationAllowed(schemas.Gemini, provider.customProviderConfig, schemas.TranscriptionStreamRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	if request.Input == nil || request.Input.File == nil {
		return nil, newBifrostOperationError("transcription input is not provided", fmt.Errorf("empty file input"), providerName)
	}

	// Check file size limit (Gemini has a 20MB limit for inline data)
	if request.Input.File != nil {
		const maxFileSize = 20 * 1024 * 1024 // 20MB
		if len(request.Input.File) > maxFileSize {
			return nil, newBifrostOperationError("audio file too large for inline transcription", fmt.Errorf("file size %d bytes exceeds 20MB limit", len(request.Input.File)), providerName)
		}
	}

	// Prepare request body using transcription-specific function
	requestBody := gemini.ToGeminiTranscriptionRequest(request)
	if requestBody == nil {
		return nil, newBifrostOperationError("transcription input is not provided", nil, providerName)
	}

	jsonBody, err := sonic.Marshal(requestBody)
	if err != nil {
		return nil, newBifrostOperationError(schemas.ErrProviderJSONMarshaling, err, providerName)
	}

	// Create HTTP request for streaming
	req, err := http.NewRequestWithContext(ctx, "POST", provider.networkConfig.BaseURL+"/models/"+request.Model+":streamGenerateContent?alt=sse", bytes.NewReader(jsonBody))
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, &schemas.BifrostError{
				IsBifrostError: false,
				Error: &schemas.ErrorField{
					Type:    schemas.Ptr(schemas.RequestCancelled),
					Message: schemas.ErrRequestCancelled,
					Error:   err,
				},
			}
		}
		if errors.Is(err, http.ErrHandlerTimeout) || errors.Is(err, context.DeadlineExceeded) {
			return nil, newBifrostOperationError(schemas.ErrProviderRequestTimedOut, err, providerName)
		}
		return nil, newBifrostOperationError(schemas.ErrProviderRequest, err, providerName)
	}

	// Set any extra headers from network config
	setExtraHeadersHTTP(req, provider.networkConfig.ExtraHeaders, nil)

	// Set headers for streaming
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", key.Value)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")

	// Make the request
	resp, err := provider.streamClient.Do(req)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, &schemas.BifrostError{
				IsBifrostError: false,
				Error: &schemas.ErrorField{
					Type:    schemas.Ptr(schemas.RequestCancelled),
					Message: schemas.ErrRequestCancelled,
					Error:   err,
				},
			}
		}
		if errors.Is(err, http.ErrHandlerTimeout) || errors.Is(err, context.DeadlineExceeded) {
			return nil, newBifrostOperationError(schemas.ErrProviderRequestTimedOut, err, providerName)
		}
		return nil, newBifrostOperationError(schemas.ErrProviderRequest, err, providerName)
	}

	// Check for HTTP errors
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, parseStreamGeminiError(providerName, resp)
	}

	// Create response channel
	responseChan := make(chan *schemas.BifrostStream, schemas.DefaultStreamBufferSize)

	// Start streaming in a goroutine
	go func() {
		defer close(responseChan)
		defer resp.Body.Close()

		scanner := bufio.NewScanner(resp.Body)
		chunkIndex := -1
		usage := &schemas.TranscriptionUsage{}
		startTime := time.Now()
		lastChunkTime := startTime

		var fullTranscriptionText string

		for scanner.Scan() {
			line := scanner.Text()

			// Skip empty lines
			if line == "" {
				continue
			}
			var jsonData string
			// Parse SSE data
			if strings.HasPrefix(line, "data: ") {
				jsonData = strings.TrimPrefix(line, "data: ")
			} else {
				// Handle raw JSON errors (without "data: " prefix)
				jsonData = line
			}

			// Skip empty data
			if strings.TrimSpace(jsonData) == "" {
				continue
			}

			// First, check if this is an error response
			var errorCheck map[string]interface{}
			if err := sonic.Unmarshal([]byte(jsonData), &errorCheck); err != nil {
				provider.logger.Warn(fmt.Sprintf("Failed to parse stream data as JSON: %v", err))
				continue
			}

			// Handle error responses
			if _, hasError := errorCheck["error"]; hasError {
				bifrostErr := &schemas.BifrostError{
					Type:           schemas.Ptr("gemini_api_error"),
					IsBifrostError: false,
					Error: &schemas.ErrorField{
						Message: fmt.Sprintf("Gemini API error: %v", errorCheck["error"]),
						Error:   fmt.Errorf("stream error: %v", errorCheck["error"]),
					},
					ExtraFields: schemas.BifrostErrorExtraFields{
						RequestType:    schemas.TranscriptionStreamRequest,
						Provider:       providerName,
						ModelRequested: request.Model,
					},
				}
				ctx = context.WithValue(ctx, schemas.BifrostContextKeyStreamEndIndicator, true)
				processAndSendBifrostError(ctx, postHookRunner, bifrostErr, responseChan, provider.logger)
				return
			}

			// Parse Gemini streaming response
			var geminiResponse gemini.GenerateContentResponse
			if err := sonic.Unmarshal([]byte(jsonData), &geminiResponse); err != nil {
				provider.logger.Warn(fmt.Sprintf("Failed to parse Gemini stream response: %v", err))
				continue
			}

			// Extract text from Gemini response for regular chunks
			var deltaText string
			if len(geminiResponse.Candidates) > 0 && geminiResponse.Candidates[0].Content != nil {
				if len(geminiResponse.Candidates[0].Content.Parts) > 0 {
					var sb strings.Builder
					for _, p := range geminiResponse.Candidates[0].Content.Parts {
						if p.Text != "" {
							sb.WriteString(p.Text)
						}
					}
					if sb.Len() > 0 {
						deltaText = sb.String()
						fullTranscriptionText += deltaText
					}
				}
			}

			// Check if this is the final chunk (has finishReason)
			if len(geminiResponse.Candidates) > 0 && (geminiResponse.Candidates[0].FinishReason != "" || geminiResponse.UsageMetadata != nil) {
				// Extract usage metadata from Gemini response
				inputTokens, outputTokens, totalTokens := extractGeminiUsageMetadata(&geminiResponse)
				usage.InputTokens = schemas.Ptr(inputTokens)
				usage.OutputTokens = schemas.Ptr(outputTokens)
				usage.TotalTokens = schemas.Ptr(totalTokens)
			}

			// Only send response if we have actual text content
			if deltaText != "" {
				chunkIndex++

				// Create Bifrost transcription response for streaming
				response := &schemas.BifrostResponse{
					Object: "audio.transcription.chunk",
					Transcribe: &schemas.BifrostTranscribe{
						BifrostTranscribeStreamResponse: &schemas.BifrostTranscribeStreamResponse{
							Type:  schemas.Ptr("transcript.text.delta"),
							Delta: &deltaText, // Delta text for this chunk
						},
					},
					Model: request.Model,
					ExtraFields: schemas.BifrostResponseExtraFields{
						RequestType:    schemas.TranscriptionStreamRequest,
						Provider:       providerName,
						ModelRequested: request.Model,
						ChunkIndex:     chunkIndex,
						Latency:        time.Since(lastChunkTime).Milliseconds(),
					},
				}
				lastChunkTime = time.Now()

				if provider.sendBackRawResponse {
					response.ExtraFields.RawResponse = jsonData
				}

				// Process response through post-hooks and send to channel
				processAndSendResponse(ctx, postHookRunner, response, responseChan, provider.logger)
			}
		}

		// Handle scanner errors
		if err := scanner.Err(); err != nil {
			provider.logger.Warn(fmt.Sprintf("Error reading stream: %v", err))
			processAndSendError(ctx, postHookRunner, err, responseChan, schemas.TranscriptionStreamRequest, providerName, request.Model, provider.logger)
		} else {
			response := &schemas.BifrostResponse{
				Object: "audio.transcription.chunk",
				Transcribe: &schemas.BifrostTranscribe{
					Text: fullTranscriptionText,
					Usage: &schemas.TranscriptionUsage{
						Type:         "tokens",
						InputTokens:  usage.InputTokens,
						OutputTokens: usage.OutputTokens,
						TotalTokens:  usage.TotalTokens,
					},
				},
				ExtraFields: schemas.BifrostResponseExtraFields{
					RequestType:    schemas.TranscriptionStreamRequest,
					Provider:       providerName,
					ModelRequested: request.Model,
					ChunkIndex:     chunkIndex + 1,
					Latency:        time.Since(startTime).Milliseconds(),
				},
			}

			ctx = context.WithValue(ctx, schemas.BifrostContextKeyStreamEndIndicator, true)
			handleStreamEndWithSuccess(ctx, response, postHookRunner, responseChan, provider.logger)
		}
	}()

	return responseChan, nil
}

// processGeminiStreamChunk processes a single chunk from Gemini streaming response
func processGeminiStreamChunk(jsonData string) (*gemini.GenerateContentResponse, error) {
	// First, check if this is an error response
	var errorCheck map[string]interface{}
	if err := sonic.Unmarshal([]byte(jsonData), &errorCheck); err != nil {
		return nil, fmt.Errorf("failed to parse stream data as JSON: %v", err)
	}

	// Handle error responses
	if _, hasError := errorCheck["error"]; hasError {
		return nil, fmt.Errorf("gemini api error: %v", errorCheck["error"])
	}

	// Parse Gemini streaming response
	var geminiResponse gemini.GenerateContentResponse
	if err := sonic.Unmarshal([]byte(jsonData), &geminiResponse); err != nil {
		return nil, fmt.Errorf("failed to parse Gemini stream response: %v", err)
	}

	return &geminiResponse, nil
}

// extractGeminiUsageMetadata extracts usage metadata (as ints) from Gemini response
func extractGeminiUsageMetadata(geminiResponse *gemini.GenerateContentResponse) (int, int, int) {
	var inputTokens, outputTokens, totalTokens int
	if geminiResponse.UsageMetadata != nil {
		usageMetadata := geminiResponse.UsageMetadata
		inputTokens = int(usageMetadata.PromptTokenCount)
		outputTokens = int(usageMetadata.CandidatesTokenCount)
		totalTokens = int(usageMetadata.TotalTokenCount)
	}
	return inputTokens, outputTokens, totalTokens
}

// completeRequest handles the common HTTP request pattern for Gemini API calls
func (provider *GeminiProvider) completeRequest(ctx context.Context, model string, key schemas.Key, jsonBody []byte, endpoint string) (*gemini.GenerateContentResponse, interface{}, time.Duration, *schemas.BifrostError) {
	providerName := provider.GetProviderKey()

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Set any extra headers from network config
	setExtraHeaders(req, provider.networkConfig.ExtraHeaders, nil)

	// Use Gemini's generateContent endpoint
	req.SetRequestURI(provider.networkConfig.BaseURL + "/models/" + model + endpoint)
	req.Header.SetMethod("POST")
	req.Header.SetContentType("application/json")
	req.Header.Set("x-goog-api-key", key.Value)

	req.SetBody(jsonBody)

	// Make request
	latency, bifrostErr := makeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, nil, latency, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, nil, latency, parseGeminiError(providerName, resp)
	}

	// Copy the response body before releasing the response
	// to avoid use-after-free since resp.Body() references fasthttp's internal buffer
	responseBody := append([]byte(nil), resp.Body()...)

	// Parse Gemini's response
	var geminiResponse gemini.GenerateContentResponse
	if err := sonic.Unmarshal(responseBody, &geminiResponse); err != nil {
		return nil, nil, latency, newBifrostOperationError(schemas.ErrProviderResponseUnmarshal, err, providerName)
	}

	var rawResponse interface{}
	if err := sonic.Unmarshal(responseBody, &rawResponse); err != nil {
		return nil, nil, latency, newBifrostOperationError(schemas.ErrProviderResponseUnmarshal, err, providerName)
	}

	return &geminiResponse, rawResponse, latency, nil
}

// parseStreamGeminiError parses Gemini streaming error responses
func parseStreamGeminiError(providerName schemas.ModelProvider, resp *http.Response) *schemas.BifrostError {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return newBifrostOperationError("failed to read error response body", err, providerName)
	}

	// Try to parse as JSON first
	var errorResp map[string]interface{}
	if err := sonic.Unmarshal(body, &errorResp); err == nil {
		// Successfully parsed as JSON
		return newBifrostOperationError(fmt.Sprintf("Gemini streaming error: %v", errorResp), fmt.Errorf("HTTP %d", resp.StatusCode), providerName)
	}

	// If JSON parsing fails, treat as plain text
	bodyStr := string(body)
	if bodyStr == "" {
		bodyStr = "empty response body"
	}

	return newBifrostOperationError(fmt.Sprintf("Gemini streaming error (HTTP %d): %s", resp.StatusCode, bodyStr), fmt.Errorf("HTTP %d", resp.StatusCode), providerName)
}

// parseGeminiError parses Gemini error responses
func parseGeminiError(providerName schemas.ModelProvider, resp *fasthttp.Response) *schemas.BifrostError {
	var errorResp map[string]interface{}
	body := resp.Body()

	if err := sonic.Unmarshal(body, &errorResp); err != nil {
		return newBifrostOperationError("failed to parse error response", err, providerName)
	}

	return newBifrostOperationError(fmt.Sprintf("Gemini error: %v", errorResp), fmt.Errorf("HTTP %d", resp.StatusCode()), providerName)
}
