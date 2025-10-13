// Package providers implements various LLM providers and their utility functions.
// This file contains the OpenAI provider implementation.
package providers

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/core/schemas/providers/openai"
	"github.com/valyala/fasthttp"
)

// OpenAIProvider implements the Provider interface for OpenAI's GPT API.
type OpenAIProvider struct {
	logger               schemas.Logger                // Logger for provider operations
	client               *fasthttp.Client              // HTTP client for API requests
	streamClient         *http.Client                  // HTTP client for streaming requests
	networkConfig        schemas.NetworkConfig         // Network configuration including extra headers
	sendBackRawResponse  bool                          // Whether to include raw response in BifrostResponse
	customProviderConfig *schemas.CustomProviderConfig // Custom provider config
}

// NewOpenAIProvider creates a new OpenAI provider instance.
// It initializes the HTTP client with the provided configuration and sets up response pools.
// The client is configured with timeouts, concurrency limits, and optional proxy settings.
func NewOpenAIProvider(config *schemas.ProviderConfig, logger schemas.Logger) *OpenAIProvider {
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

	// // Pre-warm response pools
	// for range config.ConcurrencyAndBufferSize.Concurrency {
	// 	openAIResponsePool.Put(&schemas.BifrostResponse{})
	// }

	// Configure proxy if provided
	client = configureProxy(client, config.ProxyConfig, logger)

	// Set default BaseURL if not provided
	if config.NetworkConfig.BaseURL == "" {
		config.NetworkConfig.BaseURL = "https://api.openai.com"
	}
	config.NetworkConfig.BaseURL = strings.TrimRight(config.NetworkConfig.BaseURL, "/")

	return &OpenAIProvider{
		logger:               logger,
		client:               client,
		streamClient:         streamClient,
		networkConfig:        config.NetworkConfig,
		sendBackRawResponse:  config.SendBackRawResponse,
		customProviderConfig: config.CustomProviderConfig,
	}
}

// GetProviderKey returns the provider identifier for OpenAI.
func (provider *OpenAIProvider) GetProviderKey() schemas.ModelProvider {
	return getProviderName(schemas.OpenAI, provider.customProviderConfig)
}

// TextCompletion is not supported by the OpenAI provider.
// Returns an error indicating that text completion is not available.
func (provider *OpenAIProvider) TextCompletion(ctx context.Context, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (*schemas.BifrostResponse, *schemas.BifrostError) {
	if err := checkOperationAllowed(schemas.OpenAI, provider.customProviderConfig, schemas.TextCompletionRequest); err != nil {
		return nil, err
	}
	return handleOpenAITextCompletionRequest(
		ctx,
		provider.client,
		provider.networkConfig.BaseURL+"/v1/completions",
		request,
		key,
		provider.networkConfig.ExtraHeaders,
		provider.GetProviderKey(),
		provider.sendBackRawResponse,
		provider.logger,
	)
}

func handleOpenAITextCompletionRequest(
	ctx context.Context,
	client *fasthttp.Client,
	url string,
	request *schemas.BifrostTextCompletionRequest,
	key schemas.Key,
	extraHeaders map[string]string,
	providerName schemas.ModelProvider,
	sendBackRawResponse bool,
	logger schemas.Logger,
) (*schemas.BifrostResponse, *schemas.BifrostError) {
	reqBody := openai.ToOpenAITextCompletionRequest(request)
	if reqBody == nil {
		return nil, newBifrostOperationError("text completion input is not provided", nil, providerName)
	}
	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	jsonBody, err := sonic.Marshal(reqBody)
	if err != nil {
		return nil, newBifrostOperationError(schemas.ErrProviderJSONMarshaling, err, providerName)
	}

	// Set any extra headers from network config
	setExtraHeaders(req, extraHeaders, nil)

	req.SetRequestURI(url)
	req.Header.SetMethod("POST")
	req.Header.SetContentType("application/json")

	if key.Value != "" {
		req.Header.Set("Authorization", "Bearer "+key.Value)
	}

	req.SetBody(jsonBody)

	// Make request
	latency, bifrostErr := makeRequestWithContext(ctx, client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		logger.Debug(fmt.Sprintf("error from %s provider: %s", providerName, string(resp.Body())))

		var errorResp map[string]interface{}
		bifrostErr := handleProviderAPIError(resp, &errorResp)
		bifrostErr.Error.Message = fmt.Sprintf("%s error: %v", providerName, errorResp)
		return nil, bifrostErr
	}

	responseBody := resp.Body()

	response := &schemas.BifrostResponse{}

	rawResponse, bifrostErr := handleProviderResponse(responseBody, response, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	response.ExtraFields.Provider = providerName
	response.ExtraFields.ModelRequested = request.Model
	response.ExtraFields.RequestType = schemas.TextCompletionRequest
	response.ExtraFields.Latency = latency.Milliseconds()

	// Set raw response if enabled
	if sendBackRawResponse {
		response.ExtraFields.RawResponse = rawResponse
	}

	return response, nil
}

// TextCompletionStream performs a streaming text completion request to OpenAI's API.
// It formats the request, sends it to OpenAI, and processes the response.
// Returns a channel of BifrostStream objects or an error if the request fails.
func (provider *OpenAIProvider) TextCompletionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	if err := checkOperationAllowed(schemas.OpenAI, provider.customProviderConfig, schemas.TextCompletionStreamRequest); err != nil {
		return nil, err
	}
	return handleOpenAITextCompletionStreaming(
		ctx,
		provider.streamClient,
		provider.networkConfig.BaseURL+"/v1/completions",
		request,
		map[string]string{"Authorization": "Bearer " + key.Value},
		provider.networkConfig.ExtraHeaders,
		provider.sendBackRawResponse,
		provider.GetProviderKey(),
		postHookRunner,
		provider.logger,
	)
}

// handleOpenAITextCompletionStreaming handles text completion streaming for OpenAI-compatible APIs.
// This shared function reduces code duplication between providers that use the same SSE format.
func handleOpenAITextCompletionStreaming(
	ctx context.Context,
	client *http.Client,
	url string,
	request *schemas.BifrostTextCompletionRequest,
	authHeader map[string]string,
	extraHeaders map[string]string,
	sendBackRawResponse bool,
	providerName schemas.ModelProvider,
	postHookRunner schemas.PostHookRunner,
	logger schemas.Logger,
) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	reqBody := openai.ToOpenAITextCompletionRequest(request)
	if reqBody == nil {
		return nil, newBifrostOperationError("text completion input is not provided", nil, providerName)
	}
	reqBody.Stream = schemas.Ptr(true)
	reqBody.StreamOptions = &schemas.ChatStreamOptions{
		IncludeUsage: schemas.Ptr(true),
	}

	// Prepare SGL headers (SGL typically doesn't require authorization, but we include it if provided)
	headers := map[string]string{
		"Content-Type":  "application/json",
		"Accept":        "text/event-stream",
		"Cache-Control": "no-cache",
	}

	if authHeader != nil {
		// Copy auth header to headers
		maps.Copy(headers, authHeader)
	}

	jsonBody, err := sonic.Marshal(reqBody)
	if err != nil {
		return nil, newBifrostOperationError(schemas.ErrProviderJSONMarshaling, err, providerName)
	}

	// Create HTTP request for streaming
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, newBifrostOperationError(schemas.ErrProviderRequest, err, providerName)
	}

	// Set any extra headers from network config
	setExtraHeadersHTTP(req, extraHeaders, nil)

	// Set headers
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	// Make the request
	resp, err := client.Do(req)
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
		return nil, parseStreamOpenAIError(resp)
	}

	// Create response channel
	responseChan := make(chan *schemas.BifrostStream, schemas.DefaultStreamBufferSize)

	// Start streaming in a goroutine
	go func() {
		defer close(responseChan)
		defer resp.Body.Close()

		scanner := bufio.NewScanner(resp.Body)
		chunkIndex := -1
		usage := &schemas.LLMUsage{}

		var finishReason *string
		var messageID string
		startTime := time.Now()
		lastChunkTime := startTime

		for scanner.Scan() {
			line := scanner.Text()

			// Skip empty lines and comments
			if line == "" || strings.HasPrefix(line, ":") {
				continue
			}

			// Check for end of stream
			if line == "data: [DONE]" {
				break
			}

			var jsonData string

			// Parse SSE data
			if after, ok := strings.CutPrefix(line, "data: "); ok {
				jsonData = after
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
				logger.Warn(fmt.Sprintf("Failed to parse stream data as JSON: %v", err))
				continue
			}

			// Handle error responses
			if _, hasError := errorCheck["error"]; hasError {
				bifrostErr, err := parseOpenAIErrorForStreamDataLine(jsonData, schemas.TextCompletionStreamRequest, providerName, request.Model)
				if err != nil {
					logger.Warn(fmt.Sprintf("Failed to parse error response: %v", err))
					continue
				}
				ctx = context.WithValue(ctx, schemas.BifrostContextKeyStreamEndIndicator, true)
				processAndSendBifrostError(ctx, postHookRunner, bifrostErr, responseChan, logger)
				return
			}

			// Parse into bifrost response
			var response schemas.BifrostResponse
			if err := sonic.Unmarshal([]byte(jsonData), &response); err != nil {
				logger.Warn(fmt.Sprintf("Failed to parse stream response: %v", err))
				continue
			}

			// Handle usage-only chunks (when stream_options include_usage is true)
			if response.Usage != nil {
				// Collect usage information and send at the end of the stream
				// Here in some cases usage comes before final message
				// So we need to check if the response.Usage is nil and then if usage != nil
				// then add up all tokens
				if response.Usage.PromptTokens > usage.PromptTokens {
					usage.PromptTokens = response.Usage.PromptTokens
				}
				if response.Usage.CompletionTokens > usage.CompletionTokens {
					usage.CompletionTokens = response.Usage.CompletionTokens
				}
				if response.Usage.TotalTokens > usage.TotalTokens {
					usage.TotalTokens = response.Usage.TotalTokens
				}
				calculatedTotal := usage.PromptTokens + usage.CompletionTokens
				if calculatedTotal > usage.TotalTokens {
					usage.TotalTokens = calculatedTotal
				}
				response.Usage = nil
			}

			// Skip empty responses or responses without choices
			if len(response.Choices) == 0 {
				continue
			}

			// Handle finish reason, usually in the final chunk
			choice := response.Choices[0]
			if choice.FinishReason != nil && *choice.FinishReason != "" {
				// Collect finish reason and send at the end of the stream
				finishReason = choice.FinishReason
				response.Choices[0].FinishReason = nil
			}

			if response.ID != "" && messageID == "" {
				messageID = response.ID
			}

			// Handle regular content chunks
			if choice.BifrostTextCompletionResponseChoice != nil && choice.BifrostTextCompletionResponseChoice.Text != nil {
				chunkIndex++

				response.ExtraFields.RequestType = schemas.TextCompletionStreamRequest
				response.ExtraFields.Provider = providerName
				response.ExtraFields.ModelRequested = request.Model
				response.ExtraFields.ChunkIndex = chunkIndex
				response.ExtraFields.Latency = time.Since(lastChunkTime).Milliseconds()
				lastChunkTime = time.Now()

				if sendBackRawResponse {
					response.ExtraFields.RawResponse = jsonData
				}

				processAndSendResponse(ctx, postHookRunner, &response, responseChan, logger)
			}
		}

		// Handle scanner errors first
		if err := scanner.Err(); err != nil {
			logger.Warn(fmt.Sprintf("Error reading stream: %v", err))
			processAndSendError(ctx, postHookRunner, err, responseChan, schemas.TextCompletionStreamRequest, providerName, request.Model, logger)
		} else {
			response := createBifrostCompletionChunkResponse(messageID, usage, finishReason, chunkIndex, schemas.TextCompletionStreamRequest, providerName, request.Model)
			response.ExtraFields.Latency = time.Since(startTime).Milliseconds()
			handleStreamEndWithSuccess(ctx, response, postHookRunner, responseChan, logger)
		}
	}()

	return responseChan, nil
}

// ChatCompletion performs a chat completion request to the OpenAI API.
// It supports both text and image content in messages.
// Returns a BifrostResponse containing the completion results or an error if the request fails.
func (provider *OpenAIProvider) ChatCompletion(ctx context.Context, key schemas.Key, request *schemas.BifrostChatRequest) (*schemas.BifrostResponse, *schemas.BifrostError) {
	// Check if chat completion is allowed for this provider
	if err := checkOperationAllowed(schemas.OpenAI, provider.customProviderConfig, schemas.ChatCompletionRequest); err != nil {
		return nil, err
	}

	return handleOpenAIChatCompletionRequest(
		ctx,
		provider.client,
		provider.networkConfig.BaseURL+"/v1/chat/completions",
		request,
		key,
		provider.networkConfig.ExtraHeaders,
		provider.sendBackRawResponse,
		provider.GetProviderKey(),
		provider.logger,
	)
}

func handleOpenAIChatCompletionRequest(
	ctx context.Context,
	client *fasthttp.Client,
	url string,
	request *schemas.BifrostChatRequest,
	key schemas.Key,
	extraHeaders map[string]string,
	sendBackRawResponse bool,
	providerName schemas.ModelProvider,
	logger schemas.Logger,
) (*schemas.BifrostResponse, *schemas.BifrostError) {
	// Use centralized converter
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
	setExtraHeaders(req, extraHeaders, nil)

	req.SetRequestURI(url)
	req.Header.SetMethod("POST")
	req.Header.SetContentType("application/json")

	if key.Value != "" {
		req.Header.Set("Authorization", "Bearer "+key.Value)
	}

	req.SetBody(jsonBody)

	// Make request
	latency, bifrostErr := makeRequestWithContext(ctx, client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		logger.Debug(fmt.Sprintf("error from %s provider: %s", providerName, string(resp.Body())))
		return nil, parseOpenAIError(resp)
	}

	responseBody := resp.Body()

	// Pre-allocate response structs from pools
	// response := acquireOpenAIResponse()
	// defer releaseOpenAIResponse(response)
	response := &schemas.BifrostResponse{}

	// Use enhanced response handler with pre-allocated response
	rawResponse, bifrostErr := handleProviderResponse(responseBody, response, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Set raw response if enabled
	if sendBackRawResponse {
		response.ExtraFields.RawResponse = rawResponse
	}

	response.ExtraFields.Provider = providerName
	response.ExtraFields.ModelRequested = request.Model
	response.ExtraFields.RequestType = schemas.ChatCompletionRequest
	response.ExtraFields.Latency = latency.Milliseconds()

	return response, nil
}

// ChatCompletionStream handles streaming for OpenAI chat completions.
// It formats messages, prepares request body, and uses shared streaming logic.
// Returns a channel for streaming responses and any error that occurred.
func (provider *OpenAIProvider) ChatCompletionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostChatRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	// Check if chat completion stream is allowed for this provider
	if err := checkOperationAllowed(schemas.OpenAI, provider.customProviderConfig, schemas.ChatCompletionStreamRequest); err != nil {
		return nil, err
	}

	// Use shared streaming logic
	return handleOpenAIChatCompletionStreaming(
		ctx,
		provider.streamClient,
		provider.networkConfig.BaseURL+"/v1/chat/completions",
		request,
		map[string]string{"Authorization": "Bearer " + key.Value},
		provider.networkConfig.ExtraHeaders,
		provider.sendBackRawResponse,
		provider.GetProviderKey(),
		postHookRunner,
		provider.logger,
	)
}

// handleOpenAIChatCompletionStreaming handles streaming for OpenAI-compatible APIs.
// This shared function reduces code duplication between providers that use the same SSE format.
func handleOpenAIChatCompletionStreaming(
	ctx context.Context,
	client *http.Client,
	url string,
	request *schemas.BifrostChatRequest,
	authHeader map[string]string,
	extraHeaders map[string]string,
	sendBackRawResponse bool,
	providerName schemas.ModelProvider,
	postHookRunner schemas.PostHookRunner,
	logger schemas.Logger,
) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	reqBody := openai.ToOpenAIChatRequest(request)
	if reqBody == nil {
		return nil, newBifrostOperationError("chat completion input is not provided", nil, providerName)
	}
	reqBody.Stream = schemas.Ptr(true)
	reqBody.StreamOptions = &schemas.ChatStreamOptions{
		IncludeUsage: schemas.Ptr(true),
	}

	// Prepare SGL headers (SGL typically doesn't require authorization, but we include it if provided)
	headers := map[string]string{
		"Content-Type":  "application/json",
		"Accept":        "text/event-stream",
		"Cache-Control": "no-cache",
	}

	if authHeader != nil {
		// Copy auth header to headers
		maps.Copy(headers, authHeader)
	}

	jsonBody, err := sonic.Marshal(reqBody)
	if err != nil {
		return nil, newBifrostOperationError(schemas.ErrProviderJSONMarshaling, err, providerName)
	}

	// Create HTTP request for streaming
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(jsonBody))
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
	setExtraHeadersHTTP(req, extraHeaders, nil)

	// Set headers
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	// Make the request
	resp, err := client.Do(req)
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
		return nil, parseStreamOpenAIError(resp)
	}

	// Create response channel
	responseChan := make(chan *schemas.BifrostStream, schemas.DefaultStreamBufferSize)

	// Start streaming in a goroutine
	go func() {
		defer close(responseChan)
		defer resp.Body.Close()

		scanner := bufio.NewScanner(resp.Body)
		chunkIndex := -1
		usage := &schemas.LLMUsage{}

		startTime := time.Now()
		lastChunkTime := startTime

		var finishReason *string
		var messageID string

		for scanner.Scan() {
			line := scanner.Text()

			// Skip empty lines and comments
			if line == "" || strings.HasPrefix(line, ":") {
				continue
			}

			// Check for end of stream
			if line == "data: [DONE]" {
				break
			}

			var jsonData string

			// Parse SSE data
			if after, ok := strings.CutPrefix(line, "data: "); ok {
				jsonData = after
			} else {
				// Handle raw JSON errors (without "data: " prefix)
				jsonData = line
			}

			// Skip empty data
			if strings.TrimSpace(jsonData) == "" {
				continue
			}

			// First, check if this is an error response
			//TODO: FIX THIS WITHING RESPONSE STRUCT
			var errorCheck map[string]interface{}
			if err := sonic.Unmarshal([]byte(jsonData), &errorCheck); err != nil {
				logger.Warn(fmt.Sprintf("Failed to parse stream data as JSON: %v", err))
				continue
			}

			// Handle error responses
			if _, hasError := errorCheck["error"]; hasError {
				bifrostErr, err := parseOpenAIErrorForStreamDataLine(jsonData, schemas.ChatCompletionStreamRequest, providerName, request.Model)
				if err != nil {
					logger.Warn(fmt.Sprintf("Failed to parse error response: %v", err))
					continue
				}
				ctx = context.WithValue(ctx, schemas.BifrostContextKeyStreamEndIndicator, true)
				processAndSendBifrostError(ctx, postHookRunner, bifrostErr, responseChan, logger)
				return
			}

			// Parse into bifrost response
			var response schemas.BifrostResponse
			if err := sonic.Unmarshal([]byte(jsonData), &response); err != nil {
				logger.Warn(fmt.Sprintf("Failed to parse stream response: %v", err))
				continue
			}

			// Handle usage-only chunks (when stream_options include_usage is true)
			if response.Usage != nil {
				// Collect usage information and send at the end of the stream
				// Here in some cases usage comes before final message
				// So we need to check if the response.Usage is nil and then if usage != nil
				// then add up all tokens
				if response.Usage.PromptTokens > usage.PromptTokens {
					usage.PromptTokens = response.Usage.PromptTokens
				}
				if response.Usage.CompletionTokens > usage.CompletionTokens {
					usage.CompletionTokens = response.Usage.CompletionTokens
				}
				if response.Usage.TotalTokens > usage.TotalTokens {
					usage.TotalTokens = response.Usage.TotalTokens
				}
				calculatedTotal := usage.PromptTokens + usage.CompletionTokens
				if calculatedTotal > usage.TotalTokens {
					usage.TotalTokens = calculatedTotal
				}
				response.Usage = nil
			}

			// Skip empty responses or responses without choices
			if len(response.Choices) == 0 {
				continue
			}

			// Handle finish reason, usually in the final chunk
			choice := response.Choices[0]
			if choice.FinishReason != nil && *choice.FinishReason != "" {
				// Collect finish reason and send at the end of the stream
				finishReason = choice.FinishReason
				response.Choices[0].FinishReason = nil
			}

			if response.ID != "" && messageID == "" {
				messageID = response.ID
			}

			// Handle regular content chunks
			if choice.BifrostStreamResponseChoice != nil &&
				choice.BifrostStreamResponseChoice.Delta != nil &&
				(choice.BifrostStreamResponseChoice.Delta.Content != nil ||
					len(choice.BifrostStreamResponseChoice.Delta.ToolCalls) > 0) {
				chunkIndex++

				response.ExtraFields.RequestType = schemas.ChatCompletionStreamRequest
				response.ExtraFields.Provider = providerName
				response.ExtraFields.ModelRequested = request.Model
				response.ExtraFields.ChunkIndex = chunkIndex
				response.ExtraFields.Latency = time.Since(lastChunkTime).Milliseconds()
				lastChunkTime = time.Now()

				if sendBackRawResponse {
					response.ExtraFields.RawResponse = jsonData
				}

				processAndSendResponse(ctx, postHookRunner, &response, responseChan, logger)
			}
		}

		// Handle scanner errors first
		if err := scanner.Err(); err != nil {
			logger.Warn(fmt.Sprintf("Error reading stream: %v", err))
			processAndSendError(ctx, postHookRunner, err, responseChan, schemas.ChatCompletionStreamRequest, providerName, request.Model, logger)
		} else {
			response := createBifrostChatCompletionChunkResponse(messageID, usage, finishReason, chunkIndex, schemas.ChatCompletionStreamRequest, providerName, request.Model)
			response.ExtraFields.Latency = time.Since(startTime).Milliseconds()
			handleStreamEndWithSuccess(ctx, response, postHookRunner, responseChan, logger)
		}
	}()

	return responseChan, nil
}

func (provider *OpenAIProvider) Responses(ctx context.Context, key schemas.Key, request *schemas.BifrostResponsesRequest) (*schemas.BifrostResponse, *schemas.BifrostError) {
	// Check if chat completion is allowed for this provider
	if err := checkOperationAllowed(schemas.OpenAI, provider.customProviderConfig, schemas.ResponsesRequest); err != nil {
		return nil, err
	}

	return handleOpenAIResponsesRequest(
		ctx,
		provider.client,
		provider.networkConfig.BaseURL+"/v1/responses",
		request,
		key,
		provider.networkConfig.ExtraHeaders,
		provider.sendBackRawResponse,
		provider.GetProviderKey(),
		provider.logger,
	)
}

func handleOpenAIResponsesRequest(
	ctx context.Context,
	client *fasthttp.Client,
	url string,
	request *schemas.BifrostResponsesRequest,
	key schemas.Key,
	extraHeaders map[string]string,
	sendBackRawResponse bool,
	providerName schemas.ModelProvider,
	logger schemas.Logger,
) (*schemas.BifrostResponse, *schemas.BifrostError) {
	// Use centralized converter
	reqBody := openai.ToOpenAIResponsesRequest(request)
	if reqBody == nil {
		return nil, newBifrostOperationError("responses input is not provided", nil, providerName)
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
	setExtraHeaders(req, extraHeaders, nil)

	req.SetRequestURI(url)
	req.Header.SetMethod("POST")
	req.Header.SetContentType("application/json")

	if key.Value != "" {
		req.Header.Set("Authorization", "Bearer "+key.Value)
	}

	req.SetBody(jsonBody)

	// Make request
	latency, bifrostErr := makeRequestWithContext(ctx, client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		logger.Debug(fmt.Sprintf("error from %s provider: %s", providerName, string(resp.Body())))
		return nil, parseOpenAIError(resp)
	}

	responseBody := resp.Body()

	// Pre-allocate response structs from pools
	// response := acquireOpenAIResponse()
	// defer releaseOpenAIResponse(response)
	response := &schemas.BifrostResponse{}

	// Use enhanced response handler with pre-allocated response
	rawResponse, bifrostErr := handleProviderResponse(responseBody, response, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Set raw response if enabled
	if sendBackRawResponse {
		response.ExtraFields.RawResponse = rawResponse
	}

	response.ExtraFields.Provider = providerName
	response.ExtraFields.ModelRequested = request.Model
	response.ExtraFields.RequestType = schemas.ResponsesRequest
	response.ExtraFields.Latency = latency.Milliseconds()

	return response, nil
}

func (provider *OpenAIProvider) ResponsesStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostResponsesRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	// Check if chat completion stream is allowed for this provider
	if err := checkOperationAllowed(schemas.OpenAI, provider.customProviderConfig, schemas.ResponsesStreamRequest); err != nil {
		return nil, err
	}

	// Use shared streaming logic
	return handleOpenAIResponsesStreaming(
		ctx,
		provider.streamClient,
		provider.networkConfig.BaseURL+"/v1/responses",
		request,
		map[string]string{"Authorization": "Bearer " + key.Value},
		provider.networkConfig.ExtraHeaders,
		provider.sendBackRawResponse,
		provider.GetProviderKey(),
		postHookRunner,
		provider.logger,
	)
}

// handleOpenAIResponsesStreaming handles streaming for OpenAI-compatible APIs.
// This shared function reduces code duplication between providers that use the same SSE format.
func handleOpenAIResponsesStreaming(
	ctx context.Context,
	client *http.Client,
	url string,
	request *schemas.BifrostResponsesRequest,
	authHeader map[string]string,
	extraHeaders map[string]string,
	sendBackRawResponse bool,
	providerName schemas.ModelProvider,
	postHookRunner schemas.PostHookRunner,
	logger schemas.Logger,
) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	reqBody := openai.ToOpenAIResponsesRequest(request)
	if reqBody == nil {
		return nil, newBifrostOperationError("responses input is not provided", nil, providerName)
	}
	reqBody.Stream = schemas.Ptr(true)

	// Prepare SGL headers (SGL typically doesn't require authorization, but we include it if provided)
	headers := map[string]string{
		"Content-Type":  "application/json",
		"Accept":        "text/event-stream",
		"Cache-Control": "no-cache",
	}

	if authHeader != nil {
		// Copy auth header to headers
		maps.Copy(headers, authHeader)
	}

	jsonBody, err := sonic.Marshal(reqBody)
	if err != nil {
		return nil, newBifrostOperationError(schemas.ErrProviderJSONMarshaling, err, providerName)
	}

	// Create HTTP request for streaming
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(jsonBody))
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
	setExtraHeadersHTTP(req, extraHeaders, nil)

	// Set headers
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	// Make the request
	resp, err := client.Do(req)
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
		return nil, parseStreamOpenAIError(resp)
	}

	// Create response channel
	responseChan := make(chan *schemas.BifrostStream, schemas.DefaultStreamBufferSize)

	// Start streaming in a goroutine
	go func() {
		defer close(responseChan)
		defer resp.Body.Close()

		scanner := bufio.NewScanner(resp.Body)

		startTime := time.Now()
		lastChunkTime := startTime

		for scanner.Scan() {
			line := scanner.Text()

			// Skip empty lines, comments, and event lines
			if line == "" || strings.HasPrefix(line, ":") || strings.HasPrefix(line, "event:") {
				continue
			}

			// Check for end of stream
			if line == "data: [DONE]" {
				break
			}

			var jsonData string

			// Parse SSE data
			if after, ok := strings.CutPrefix(line, "data: "); ok {
				jsonData = after
			} else if !strings.HasPrefix(line, "event:") {
				// Handle raw JSON errors (without "data: " prefix) but skip event lines
				jsonData = line
			} else {
				// This is an event line, skip it
				continue
			}

			// Skip empty data
			if strings.TrimSpace(jsonData) == "" {
				continue
			}

			// Parse into bifrost response
			var response schemas.BifrostResponse
			if err := sonic.Unmarshal([]byte(jsonData), &response); err != nil {
				logger.Warn(fmt.Sprintf("Failed to parse stream response: %v", err))
				continue
			}

			if response.ResponsesStreamResponse == nil {
				continue
			}

			if response.ResponsesStreamResponse.Type == schemas.ResponsesStreamResponseTypeError {
				bifrostErr := &schemas.BifrostError{
					Type:           schemas.Ptr(string(schemas.ResponsesStreamResponseTypeError)),
					IsBifrostError: false,
					Error:          &schemas.ErrorField{},
					ExtraFields: schemas.BifrostErrorExtraFields{
						RequestType:    schemas.ResponsesStreamRequest,
						Provider:       providerName,
						ModelRequested: request.Model,
					},
				}

				if response.ResponsesStreamResponse.Message != nil {
					bifrostErr.Error.Message = *response.ResponsesStreamResponse.Message
				}
				if response.ResponsesStreamResponse.Param != nil {
					bifrostErr.Error.Param = *response.ResponsesStreamResponse.Param
				}
				if response.ResponsesStreamResponse.Code != nil {
					bifrostErr.Error.Code = response.ResponsesStreamResponse.Code
				}

				ctx = context.WithValue(ctx, schemas.BifrostContextKeyStreamEndIndicator, true)
				processAndSendBifrostError(ctx, postHookRunner, bifrostErr, responseChan, logger)
				return
			}

			response.ExtraFields.RequestType = schemas.ResponsesStreamRequest
			response.ExtraFields.Provider = providerName
			response.ExtraFields.ModelRequested = request.Model
			response.ExtraFields.ChunkIndex = response.ResponsesStreamResponse.SequenceNumber

			if sendBackRawResponse {
				response.ExtraFields.RawResponse = jsonData
			}

			if response.ResponsesStreamResponse.Type == schemas.ResponsesStreamResponseTypeCompleted {
				ctx = context.WithValue(ctx, schemas.BifrostContextKeyStreamEndIndicator, true)
				response.ExtraFields.Latency = time.Since(startTime).Milliseconds()
				processAndSendResponse(ctx, postHookRunner, &response, responseChan, logger)
				return
			}

			response.ExtraFields.Latency = time.Since(lastChunkTime).Milliseconds()
			lastChunkTime = time.Now()

			processAndSendResponse(ctx, postHookRunner, &response, responseChan, logger)
		}
		// Handle scanner errors first
		if err := scanner.Err(); err != nil {
			logger.Warn(fmt.Sprintf("Error reading stream: %v", err))
			processAndSendError(ctx, postHookRunner, err, responseChan, schemas.ResponsesStreamRequest, providerName, request.Model, logger)
		}
	}()

	return responseChan, nil
}

// Embedding generates embeddings for the given input text(s).
// The input can be either a single string or a slice of strings for batch embedding.
// Returns a BifrostResponse containing the embedding(s) and any error that occurred.
func (provider *OpenAIProvider) Embedding(ctx context.Context, key schemas.Key, request *schemas.BifrostEmbeddingRequest) (*schemas.BifrostResponse, *schemas.BifrostError) {
	// Check if embedding is allowed for this provider
	if err := checkOperationAllowed(schemas.OpenAI, provider.customProviderConfig, schemas.EmbeddingRequest); err != nil {
		return nil, err
	}

	// Use the shared embedding request handler
	return handleOpenAIEmbeddingRequest(
		ctx,
		provider.client,
		provider.networkConfig.BaseURL+"/v1/embeddings",
		request,
		key,
		provider.networkConfig.ExtraHeaders,
		provider.GetProviderKey(),
		provider.sendBackRawResponse,
		provider.logger,
	)
}

// handleOpenAIEmbeddingRequest handles embedding requests for OpenAI-compatible APIs.
// This shared function reduces code duplication between providers that use the same embedding request format.
func handleOpenAIEmbeddingRequest(
	ctx context.Context,
	client *fasthttp.Client,
	url string,
	request *schemas.BifrostEmbeddingRequest,
	key schemas.Key,
	extraHeaders map[string]string,
	providerName schemas.ModelProvider,
	sendBackRawResponse bool,
	logger schemas.Logger,
) (*schemas.BifrostResponse, *schemas.BifrostError) {
	// Use centralized converter
	reqBody := openai.ToOpenAIEmbeddingRequest(request)
	if reqBody == nil {
		return nil, newBifrostOperationError("embedding input is not provided", nil, providerName)
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
	setExtraHeaders(req, extraHeaders, nil)

	req.SetRequestURI(url)
	req.Header.SetMethod("POST")
	req.Header.SetContentType("application/json")

	if key.Value != "" {
		req.Header.Set("Authorization", "Bearer "+key.Value)
	}

	req.SetBody(jsonBody)

	// Make request
	latency, bifrostErr := makeRequestWithContext(ctx, client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		logger.Debug(fmt.Sprintf("error from %s provider: %s", providerName, string(resp.Body())))
		return nil, parseOpenAIError(resp)
	}

	responseBody := resp.Body()

	// Pre-allocate response structs
	response := &schemas.BifrostResponse{}

	// Use enhanced response handler with pre-allocated response
	rawResponse, bifrostErr := handleProviderResponse(responseBody, response, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	response.ExtraFields.Provider = providerName
	response.ExtraFields.ModelRequested = request.Model
	response.ExtraFields.RequestType = schemas.EmbeddingRequest
	response.ExtraFields.Latency = latency.Milliseconds()

	if sendBackRawResponse {
		response.ExtraFields.RawResponse = rawResponse
	}

	return response, nil
}

// Speech handles non-streaming speech synthesis requests.
// It formats the request body, makes the API call, and returns the response.
// Returns the response and any error that occurred.
func (provider *OpenAIProvider) Speech(ctx context.Context, key schemas.Key, request *schemas.BifrostSpeechRequest) (*schemas.BifrostResponse, *schemas.BifrostError) {
	if err := checkOperationAllowed(schemas.OpenAI, provider.customProviderConfig, schemas.SpeechRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	// Use centralized converter
	reqBody := openai.ToOpenAISpeechRequest(request)
	if reqBody == nil {
		return nil, newBifrostOperationError("speech input is not provided", nil, providerName)
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

	req.SetRequestURI(provider.networkConfig.BaseURL + "/v1/audio/speech")
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
		provider.logger.Debug(fmt.Sprintf("error from %s provider: %s", providerName, string(resp.Body())))
		return nil, parseOpenAIError(resp)
	}

	// Get the binary audio data from the response body
	audioData := resp.Body()

	// Create final response with the audio data
	// Note: For speech synthesis, we return the binary audio data in the raw response
	// The audio data is typically in MP3, WAV, or other audio formats as specified by response_format
	bifrostResponse := &schemas.BifrostResponse{
		Object: "audio.speech",
		Model:  request.Model,
		Speech: &schemas.BifrostSpeech{
			Audio: audioData,
		},
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType:    schemas.SpeechRequest,
			Provider:       providerName,
			ModelRequested: request.Model,
			Latency:        latency.Milliseconds(),
		},
	}

	return bifrostResponse, nil
}

// SpeechStream handles streaming for speech synthesis.
// It formats the request body, creates HTTP request, and uses shared streaming logic.
// Returns a channel for streaming responses and any error that occurred.
func (provider *OpenAIProvider) SpeechStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostSpeechRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	if err := checkOperationAllowed(schemas.OpenAI, provider.customProviderConfig, schemas.SpeechStreamRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	// Use centralized converter
	reqBody := openai.ToOpenAISpeechRequest(request)
	if reqBody == nil {
		return nil, newBifrostOperationError("speech input is not provided", nil, providerName)
	}
	reqBody.StreamFormat = schemas.Ptr("sse")

	jsonBody, err := sonic.Marshal(reqBody)
	if err != nil {
		return nil, newBifrostOperationError(schemas.ErrProviderJSONMarshaling, err, providerName)
	}

	// Prepare OpenAI headers
	headers := map[string]string{
		"Content-Type":  "application/json",
		"Authorization": "Bearer " + key.Value,
		"Accept":        "text/event-stream",
		"Cache-Control": "no-cache",
	}

	// Create HTTP request for streaming
	req, err := http.NewRequestWithContext(ctx, "POST", provider.networkConfig.BaseURL+"/v1/audio/speech", bytes.NewReader(jsonBody))
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

	// Set headers
	for key, value := range headers {
		req.Header.Set(key, value)
	}

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
		return nil, parseStreamOpenAIError(resp)
	}

	// Create response channel
	responseChan := make(chan *schemas.BifrostStream, schemas.DefaultStreamBufferSize)

	// Start streaming in a goroutine
	go func() {
		defer close(responseChan)
		defer resp.Body.Close()

		scanner := bufio.NewScanner(resp.Body)
		chunkIndex := -1

		startTime := time.Now()
		lastChunkTime := startTime

		for scanner.Scan() {
			line := scanner.Text()

			// Skip empty lines and comments
			if line == "" || strings.HasPrefix(line, ":") {
				continue
			}

			// Check for end of stream
			if line == "data: [DONE]" {
				break
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
				bifrostErr, err := parseOpenAIErrorForStreamDataLine(jsonData, schemas.SpeechStreamRequest, providerName, request.Model)
				if err != nil {
					provider.logger.Warn(fmt.Sprintf("Failed to parse error response: %v", err))
					continue
				}
				ctx = context.WithValue(ctx, schemas.BifrostContextKeyStreamEndIndicator, true)
				processAndSendBifrostError(ctx, postHookRunner, bifrostErr, responseChan, provider.logger)
				return
			}

			// Parse into bifrost response
			var response schemas.BifrostResponse

			var speechResponse schemas.BifrostSpeech
			if err := sonic.Unmarshal([]byte(jsonData), &speechResponse); err != nil {
				provider.logger.Warn(fmt.Sprintf("Failed to parse stream response: %v", err))
				continue
			}

			chunkIndex++

			response.Speech = &speechResponse
			response.Object = "audio.speech.chunk"
			response.Model = request.Model
			response.ExtraFields = schemas.BifrostResponseExtraFields{
				RequestType:    schemas.SpeechStreamRequest,
				Provider:       providerName,
				ModelRequested: request.Model,
				ChunkIndex:     chunkIndex,
				Latency:        time.Since(lastChunkTime).Milliseconds(),
			}
			lastChunkTime = time.Now()

			if provider.sendBackRawResponse {
				response.ExtraFields.RawResponse = jsonData
			}

			if speechResponse.Usage != nil {
				ctx = context.WithValue(ctx, schemas.BifrostContextKeyStreamEndIndicator, true)
				response.ExtraFields.Latency = time.Since(startTime).Milliseconds()
				processAndSendResponse(ctx, postHookRunner, &response, responseChan, provider.logger)
				return
			}

			processAndSendResponse(ctx, postHookRunner, &response, responseChan, provider.logger)
		}

		// Handle scanner errors
		if err := scanner.Err(); err != nil {
			provider.logger.Warn(fmt.Sprintf("Error reading stream: %v", err))
			processAndSendError(ctx, postHookRunner, err, responseChan, schemas.SpeechStreamRequest, providerName, request.Model, provider.logger)
		}
	}()

	return responseChan, nil
}

// Transcription handles non-streaming transcription requests.
// It creates a multipart form, adds fields, makes the API call, and returns the response.
// Returns the response and any error that occurred.
func (provider *OpenAIProvider) Transcription(ctx context.Context, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (*schemas.BifrostResponse, *schemas.BifrostError) {
	if err := checkOperationAllowed(schemas.OpenAI, provider.customProviderConfig, schemas.TranscriptionRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	// Use centralized converter
	reqBody := openai.ToOpenAITranscriptionRequest(request)
	if reqBody == nil {
		return nil, newBifrostOperationError("transcription input is not provided", nil, providerName)
	}

	// Create multipart form
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	if bifrostErr := parseTranscriptionFormDataBodyFromRequest(writer, reqBody, providerName); bifrostErr != nil {
		return nil, bifrostErr
	}

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Set any extra headers from network config
	setExtraHeaders(req, provider.networkConfig.ExtraHeaders, nil)

	req.SetRequestURI(provider.networkConfig.BaseURL + "/v1/audio/transcriptions")
	req.Header.SetMethod("POST")
	req.Header.SetContentType(writer.FormDataContentType()) // This sets multipart/form-data with boundary
	req.Header.Set("Authorization", "Bearer "+key.Value)

	req.SetBody(body.Bytes())

	// Make request
	latency, bifrostErr := makeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		provider.logger.Debug(fmt.Sprintf("error from %s provider: %s", providerName, string(resp.Body())))
		return nil, parseOpenAIError(resp)
	}

	responseBody := resp.Body()

	// Parse OpenAI's transcription response directly into BifrostTranscribe
	transcribeResponse := &schemas.BifrostTranscribe{
		BifrostTranscribeNonStreamResponse: &schemas.BifrostTranscribeNonStreamResponse{},
	}

	if err := sonic.Unmarshal(responseBody, transcribeResponse); err != nil {
		return nil, newBifrostOperationError(schemas.ErrProviderResponseUnmarshal, err, providerName)
	}

	// Parse raw response for RawResponse field
	var rawResponse interface{}
	if err := sonic.Unmarshal(responseBody, &rawResponse); err != nil {
		return nil, newBifrostOperationError(schemas.ErrProviderDecodeRaw, err, providerName)
	}

	// Create final response
	bifrostResponse := &schemas.BifrostResponse{
		Object:     "audio.transcription",
		Model:      request.Model,
		Transcribe: transcribeResponse,
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType:    schemas.TranscriptionRequest,
			Provider:       providerName,
			ModelRequested: request.Model,
			Latency:        latency.Milliseconds(),
		},
	}

	if provider.sendBackRawResponse {
		bifrostResponse.ExtraFields.RawResponse = rawResponse
	}

	return bifrostResponse, nil

}

func (provider *OpenAIProvider) TranscriptionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	if err := checkOperationAllowed(schemas.OpenAI, provider.customProviderConfig, schemas.TranscriptionStreamRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	// Use centralized converter
	reqBody := openai.ToOpenAITranscriptionRequest(request)
	if reqBody == nil {
		return nil, newBifrostOperationError("transcription input is not provided", nil, providerName)
	}
	reqBody.Stream = schemas.Ptr(true)

	// Create multipart form
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	if bifrostErr := parseTranscriptionFormDataBodyFromRequest(writer, reqBody, providerName); bifrostErr != nil {
		return nil, bifrostErr
	}

	// Prepare OpenAI headers
	headers := map[string]string{
		"Content-Type":  writer.FormDataContentType(),
		"Authorization": "Bearer " + key.Value,
		"Accept":        "text/event-stream",
		"Cache-Control": "no-cache",
	}

	// Create HTTP request for streaming
	req, err := http.NewRequestWithContext(ctx, "POST", provider.networkConfig.BaseURL+"/v1/audio/transcriptions", &body)
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

	// Set headers
	for key, value := range headers {
		req.Header.Set(key, value)
	}

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
		return nil, parseStreamOpenAIError(resp)
	}

	// Create response channel
	responseChan := make(chan *schemas.BifrostStream, schemas.DefaultStreamBufferSize)

	// Start streaming in a goroutine
	go func() {
		defer close(responseChan)
		defer resp.Body.Close()

		scanner := bufio.NewScanner(resp.Body)
		chunkIndex := -1

		startTime := time.Now()
		lastChunkTime := startTime

		for scanner.Scan() {
			line := scanner.Text()

			// Skip empty lines and comments
			if line == "" {
				continue
			}

			// Check for end of stream
			if line == "data: [DONE]" {
				break
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
				bifrostErr, err := parseOpenAIErrorForStreamDataLine(jsonData, schemas.TranscriptionStreamRequest, providerName, request.Model)
				if err != nil {
					provider.logger.Warn(fmt.Sprintf("Failed to parse error response: %v", err))
					continue
				}
				ctx = context.WithValue(ctx, schemas.BifrostContextKeyStreamEndIndicator, true)
				processAndSendBifrostError(ctx, postHookRunner, bifrostErr, responseChan, provider.logger)
				return
			}

			var response schemas.BifrostResponse

			var transcriptionResponse schemas.BifrostTranscribe
			if err := sonic.Unmarshal([]byte(jsonData), &transcriptionResponse); err != nil {
				provider.logger.Warn(fmt.Sprintf("Failed to parse stream response: %v", err))
				continue
			}

			chunkIndex++

			response.Transcribe = &transcriptionResponse
			response.Object = "audio.transcription.chunk"
			response.Model = request.Model
			response.ExtraFields = schemas.BifrostResponseExtraFields{
				RequestType:    schemas.TranscriptionStreamRequest,
				Provider:       providerName,
				ModelRequested: request.Model,
				ChunkIndex:     chunkIndex,
				Latency:        time.Since(lastChunkTime).Milliseconds(),
			}
			lastChunkTime = time.Now()

			if provider.sendBackRawResponse {
				response.ExtraFields.RawResponse = jsonData
			}

			if transcriptionResponse.Usage != nil {
				ctx = context.WithValue(ctx, schemas.BifrostContextKeyStreamEndIndicator, true)
				response.ExtraFields.Latency = time.Since(startTime).Milliseconds()
				processAndSendResponse(ctx, postHookRunner, &response, responseChan, provider.logger)
				return
			}

			processAndSendResponse(ctx, postHookRunner, &response, responseChan, provider.logger)
		}

		// Handle scanner errors
		if err := scanner.Err(); err != nil {
			provider.logger.Warn(fmt.Sprintf("Error reading stream: %v", err))
			processAndSendError(ctx, postHookRunner, err, responseChan, schemas.TranscriptionStreamRequest, providerName, request.Model, provider.logger)
		}
	}()

	return responseChan, nil
}

func parseTranscriptionFormDataBodyFromRequest(writer *multipart.Writer, openaiReq *openai.OpenAITranscriptionRequest, providerName schemas.ModelProvider) *schemas.BifrostError {
	// Add file field
	fileWriter, err := writer.CreateFormFile("file", "audio.mp3") // OpenAI requires a filename
	if err != nil {
		return newBifrostOperationError("failed to create form file", err, providerName)
	}
	if _, err := fileWriter.Write(openaiReq.File); err != nil {
		return newBifrostOperationError("failed to write file data", err, providerName)
	}

	// Add model field
	if err := writer.WriteField("model", openaiReq.Model); err != nil {
		return newBifrostOperationError("failed to write model field", err, providerName)
	}

	// Add optional fields
	if openaiReq.Language != nil {
		if err := writer.WriteField("language", *openaiReq.Language); err != nil {
			return newBifrostOperationError("failed to write language field", err, providerName)
		}
	}

	if openaiReq.Prompt != nil {
		if err := writer.WriteField("prompt", *openaiReq.Prompt); err != nil {
			return newBifrostOperationError("failed to write prompt field", err, providerName)
		}
	}

	if openaiReq.ResponseFormat != nil {
		if err := writer.WriteField("response_format", *openaiReq.ResponseFormat); err != nil {
			return newBifrostOperationError("failed to write response_format field", err, providerName)
		}
	}

	if openaiReq.Stream != nil && *openaiReq.Stream {
		if err := writer.WriteField("stream", "true"); err != nil {
			return newBifrostOperationError("failed to write stream field", err, providerName)
		}
	}

	// Close the multipart writer
	if err := writer.Close(); err != nil {
		return newBifrostOperationError("failed to close multipart writer", err, providerName)
	}

	return nil
}

func parseOpenAIError(resp *fasthttp.Response) *schemas.BifrostError {
	var errorResp schemas.BifrostError

	bifrostErr := handleProviderAPIError(resp, &errorResp)

	if errorResp.EventID != nil {
		bifrostErr.EventID = errorResp.EventID
	}

	if errorResp.Error != nil {
		if bifrostErr.Error == nil {
			bifrostErr.Error = &schemas.ErrorField{}
		}
		bifrostErr.Error.Type = errorResp.Error.Type
		bifrostErr.Error.Code = errorResp.Error.Code
		bifrostErr.Error.Message = errorResp.Error.Message
		bifrostErr.Error.Param = errorResp.Error.Param
		if errorResp.Error.EventID != nil {
			bifrostErr.Error.EventID = errorResp.Error.EventID
		}
	}

	return bifrostErr
}

func parseStreamOpenAIError(resp *http.Response) *schemas.BifrostError {
	var errorResp schemas.BifrostError

	statusCode := resp.StatusCode
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if err := sonic.Unmarshal(body, &errorResp); err != nil {
		return &schemas.BifrostError{
			IsBifrostError: true,
			StatusCode:     &statusCode,
			Error: &schemas.ErrorField{
				Message: schemas.ErrProviderResponseUnmarshal,
				Error:   err,
			},
		}
	}

	bifrostErr := &schemas.BifrostError{
		IsBifrostError: false,
		StatusCode:     &statusCode,
		Error:          &schemas.ErrorField{},
	}

	if errorResp.EventID != nil {
		bifrostErr.EventID = errorResp.EventID
	}
	if errorResp.Error != nil {
		if bifrostErr.Error == nil {
			bifrostErr.Error = &schemas.ErrorField{}
		}
		bifrostErr.Error.Type = errorResp.Error.Type
		bifrostErr.Error.Code = errorResp.Error.Code
		bifrostErr.Error.Message = errorResp.Error.Message
		bifrostErr.Error.Param = errorResp.Error.Param
		if errorResp.Error.EventID != nil {
			bifrostErr.Error.EventID = errorResp.Error.EventID
		}
	}

	return bifrostErr
}

func parseOpenAIErrorForStreamDataLine(jsonData string, requestType schemas.RequestType, providerName schemas.ModelProvider, model string) (*schemas.BifrostError, error) {
	var openAIError schemas.BifrostError
	if err := sonic.Unmarshal([]byte(jsonData), &openAIError); err != nil {
		return nil, err
	}

	// Send error through channel
	bifrostErr := &schemas.BifrostError{
		IsBifrostError: false,
		Error: &schemas.ErrorField{
			Type:    openAIError.Error.Type,
			Code:    openAIError.Error.Code,
			Message: openAIError.Error.Message,
			Param:   openAIError.Error.Param,
		},
		ExtraFields: schemas.BifrostErrorExtraFields{
			RequestType:    requestType,
			Provider:       providerName,
			ModelRequested: model,
		},
	}

	if openAIError.EventID != nil {
		bifrostErr.EventID = openAIError.EventID
	}
	if openAIError.Error.EventID != nil {
		bifrostErr.Error.EventID = openAIError.Error.EventID
	}

	return bifrostErr, nil
}
