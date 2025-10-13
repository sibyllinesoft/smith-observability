// Package providers implements various LLM providers and their utility functions.
// This file contains the Cohere provider implementation.
package providers

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"net/http"

	"github.com/bytedance/sonic"
	schemas "github.com/maximhq/bifrost/core/schemas"
	cohere "github.com/maximhq/bifrost/core/schemas/providers/cohere"
	"github.com/valyala/fasthttp"
)

// cohereResponsePool provides a pool for Cohere v2 response objects.
var cohereResponsePool = sync.Pool{
	New: func() interface{} {
		return &cohere.CohereChatResponse{}
	},
}

// acquireCohereResponse gets a Cohere v2 response from the pool and resets it.
func acquireCohereResponse() *cohere.CohereChatResponse {
	resp := cohereResponsePool.Get().(*cohere.CohereChatResponse)
	*resp = cohere.CohereChatResponse{} // Reset the struct
	return resp
}

// releaseCohereResponse returns a Cohere v2 response to the pool.
func releaseCohereResponse(resp *cohere.CohereChatResponse) {
	if resp != nil {
		cohereResponsePool.Put(resp)
	}
}

// CohereProvider implements the Provider interface for Cohere.
type CohereProvider struct {
	logger               schemas.Logger                // Logger for provider operations
	client               *fasthttp.Client              // HTTP client for API requests
	streamClient         *http.Client                  // HTTP client for streaming requests
	networkConfig        schemas.NetworkConfig         // Network configuration including extra headers
	sendBackRawResponse  bool                          // Whether to include raw response in BifrostResponse
	customProviderConfig *schemas.CustomProviderConfig // Custom provider config
}

// NewCohereProvider creates a new Cohere provider instance.
// It initializes the HTTP client with the provided configuration and sets up response pools.
// The client is configured with timeouts and connection limits.
func NewCohereProvider(config *schemas.ProviderConfig, logger schemas.Logger) *CohereProvider {
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

	// Pre-warm response pools
	for i := 0; i < config.ConcurrencyAndBufferSize.Concurrency; i++ {
		cohereResponsePool.Put(&cohere.CohereChatResponse{})
	}

	// Set default BaseURL if not provided
	if config.NetworkConfig.BaseURL == "" {
		config.NetworkConfig.BaseURL = "https://api.cohere.ai"
	}
	config.NetworkConfig.BaseURL = strings.TrimRight(config.NetworkConfig.BaseURL, "/")

	return &CohereProvider{
		logger:               logger,
		client:               client,
		streamClient:         streamClient,
		networkConfig:        config.NetworkConfig,
		customProviderConfig: config.CustomProviderConfig,
		sendBackRawResponse:  config.SendBackRawResponse,
	}
}

// GetProviderKey returns the provider identifier for Cohere.
func (provider *CohereProvider) GetProviderKey() schemas.ModelProvider {
	return getProviderName(schemas.Cohere, provider.customProviderConfig)
}

// TextCompletion is not supported by the Cohere provider.
// Returns an error indicating that text completion is not supported.
func (provider *CohereProvider) TextCompletion(ctx context.Context, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("text completion", "cohere")
}

// TextCompletionStream performs a streaming text completion request to Cohere's API.
// It formats the request, sends it to Cohere, and processes the response.
// Returns a channel of BifrostStream objects or an error if the request fails.
func (provider *CohereProvider) TextCompletionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("text completion stream", "cohere")
}

func (provider *CohereProvider) handleCohereChatCompletionRequest(ctx context.Context, reqBody *cohere.CohereChatRequest, key schemas.Key) (*cohere.CohereChatResponse, interface{}, time.Duration, *schemas.BifrostError) {
	providerName := provider.GetProviderKey()

	// Marshal request body
	jsonBody, err := sonic.Marshal(reqBody)
	if err != nil {
		return nil, nil, time.Duration(0), &schemas.BifrostError{
			IsBifrostError: true,
			Error: &schemas.ErrorField{
				Message: schemas.ErrProviderJSONMarshaling,
				Error:   err,
			},
		}
	}

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Set any extra headers from network config
	setExtraHeaders(req, provider.networkConfig.ExtraHeaders, nil)

	req.SetRequestURI(provider.networkConfig.BaseURL + "/v2/chat")
	req.Header.SetMethod("POST")
	req.Header.SetContentType("application/json")
	req.Header.Set("Authorization", "Bearer "+key.Value)

	req.SetBody(jsonBody)

	// Make request
	latency, bifrostErr := makeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, nil, latency, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		provider.logger.Debug(fmt.Sprintf("error from %s provider: %s", providerName, string(resp.Body())))

		var errorResp cohere.CohereError
		bifrostErr := handleProviderAPIError(resp, &errorResp)
		bifrostErr.Error.Message = errorResp.Message

		return nil, nil, latency, bifrostErr
	}

	// Parse Cohere v2 response
	var cohereResponse cohere.CohereChatResponse
	if err := sonic.Unmarshal(resp.Body(), &cohereResponse); err != nil {
		return nil, nil, latency, &schemas.BifrostError{
			IsBifrostError: true,
			Error: &schemas.ErrorField{
				Message: "error parsing Cohere v2 response",
				Error:   err,
			},
		}
	}

	// Parse raw response for sendBackRawResponse
	var rawResponse interface{}
	if provider.sendBackRawResponse {
		if err := sonic.Unmarshal(resp.Body(), &rawResponse); err != nil {
			return nil, nil, latency, &schemas.BifrostError{
				IsBifrostError: true,
				Error: &schemas.ErrorField{
					Message: "error parsing raw response",
					Error:   err,
				},
			}
		}
	}

	return &cohereResponse, rawResponse, latency, nil
}

// ChatCompletion performs a chat completion request to the Cohere API using v2 converter.
// It formats the request, sends it to Cohere, and processes the response.
// Returns a BifrostResponse containing the completion results or an error if the request fails.
func (provider *CohereProvider) ChatCompletion(ctx context.Context, key schemas.Key, request *schemas.BifrostChatRequest) (*schemas.BifrostResponse, *schemas.BifrostError) {
	// Check if chat completion is allowed
	if err := checkOperationAllowed(schemas.Cohere, provider.customProviderConfig, schemas.ChatCompletionRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	// Convert to Cohere v2 request
	reqBody := cohere.ToCohereChatCompletionRequest(request)
	if reqBody == nil {
		return nil, newBifrostOperationError("chat completion input is not provided", nil, providerName)
	}

	cohereResponse, rawResponse, latency, err := provider.handleCohereChatCompletionRequest(ctx, reqBody, key)
	if err != nil {
		return nil, err
	}

	// Convert Cohere v2 response to Bifrost response
	bifrostResponse := cohereResponse.ToBifrostResponse()

	bifrostResponse.Model = request.Model
	bifrostResponse.ExtraFields.Provider = providerName
	bifrostResponse.ExtraFields.ModelRequested = request.Model
	bifrostResponse.ExtraFields.RequestType = schemas.ChatCompletionRequest
	bifrostResponse.ExtraFields.Latency = latency.Milliseconds()

	if provider.sendBackRawResponse {
		bifrostResponse.ExtraFields.RawResponse = rawResponse
	}

	return bifrostResponse, nil
}

// ChatCompletionStream performs a streaming chat completion request to the Cohere API.
// It supports real-time streaming of responses using Server-Sent Events (SSE).
// Returns a channel containing BifrostResponse objects representing the stream or an error if the request fails.
func (provider *CohereProvider) ChatCompletionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostChatRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	// Check if chat completion stream is allowed
	if err := checkOperationAllowed(schemas.Cohere, provider.customProviderConfig, schemas.ChatCompletionStreamRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()
	// Convert to Cohere v2 request and add streaming
	reqBody := cohere.ToCohereChatCompletionRequest(request)
	if reqBody == nil {
		return nil, newBifrostOperationError("chat completion input is not provided", nil, providerName)
	}
	reqBody.Stream = schemas.Ptr(true)

	jsonBody, err := sonic.Marshal(reqBody)
	if err != nil {
		return nil, newBifrostOperationError(schemas.ErrProviderJSONMarshaling, err, providerName)
	}

	// Create HTTP request for streaming
	req, err := http.NewRequestWithContext(ctx, "POST", provider.networkConfig.BaseURL+"/v2/chat", bytes.NewReader(jsonBody))
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

	// Set headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key.Value)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")

	// Set any extra headers from network config
	setExtraHeadersHTTP(req, provider.networkConfig.ExtraHeaders, nil)

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
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: &schemas.ErrorField{
				Message: schemas.ErrProviderRequest,
				Error:   err,
			},
		}
	}

	// Check for HTTP errors
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, newProviderAPIError(fmt.Sprintf("HTTP error from %s: %d", providerName, resp.StatusCode), fmt.Errorf("%s", string(body)), resp.StatusCode, providerName, nil, nil)
	}

	// Create response channel
	responseChan := make(chan *schemas.BifrostStream, schemas.DefaultStreamBufferSize)

	chunkIndex := -1

	// Start streaming in a goroutine
	go func() {
		defer close(responseChan)
		defer resp.Body.Close()

		scanner := bufio.NewScanner(resp.Body)
		var responseID string
		startTime := time.Now()
		lastChunkTime := startTime

		for scanner.Scan() {
			line := scanner.Text()

			// Skip empty lines and comments
			if line == "" || strings.HasPrefix(line, ":") {
				continue
			}

			// Parse SSE data
			if strings.HasPrefix(line, "data: ") {
				jsonData := strings.TrimPrefix(line, "data: ")

				// Handle [DONE] marker
				if strings.TrimSpace(jsonData) == "[DONE]" {
					provider.logger.Debug("Received [DONE] marker, ending stream")
					return
				}

				// Parse the unified streaming event
				var event cohere.CohereStreamEvent
				if err := sonic.Unmarshal([]byte(jsonData), &event); err != nil {
					provider.logger.Warn(fmt.Sprintf("Failed to parse stream event: %v", err))
					continue
				}

				chunkIndex++

				// Extract response ID from message-start events
				if event.Type == cohere.StreamEventMessageStart && event.ID != nil {
					responseID = *event.ID
				}

				// Create base response with current responseID
				response := &schemas.BifrostResponse{
					ID:     responseID,
					Object: "chat.completion.chunk",
					Model:  request.Model,
					Choices: []schemas.BifrostChatResponseChoice{
						{
							Index: 0,
							BifrostStreamResponseChoice: &schemas.BifrostStreamResponseChoice{
								Delta: &schemas.BifrostStreamDelta{},
							},
						},
					},
					ExtraFields: schemas.BifrostResponseExtraFields{
						RequestType:    schemas.ChatCompletionStreamRequest,
						Provider:       providerName,
						ModelRequested: request.Model,
						ChunkIndex:     chunkIndex,
						Latency:        time.Since(lastChunkTime).Milliseconds(),
					},
				}
				lastChunkTime = time.Now()

				switch event.Type {
				case cohere.StreamEventMessageStart:
					if event.Delta != nil && event.Delta.Message != nil && event.Delta.Message.Role != nil {
						response.Choices[0].BifrostStreamResponseChoice.Delta.Role = event.Delta.Message.Role
					}

				case cohere.StreamEventContentDelta:
					if event.Delta != nil && event.Delta.Message != nil && event.Delta.Message.Content != nil && event.Delta.Message.Content.Text != nil {
						// Try to cast content to CohereStreamContent
						response.Choices[0].BifrostStreamResponseChoice.Delta.Content = event.Delta.Message.Content.Text
					}

				case cohere.StreamEventToolPlanDelta:
					if event.Delta != nil && event.Delta.Message != nil && event.Delta.Message.ToolPlan != nil {
						response.Choices[0].BifrostStreamResponseChoice.Delta.Content = event.Delta.Message.ToolPlan
					}

				case cohere.StreamEventContentStart:
					// Content start event - just continue, actual content comes in content-delta

				case cohere.StreamEventToolCallStart, cohere.StreamEventToolCallDelta:
					if event.Delta != nil && event.Delta.Message != nil && event.Delta.Message.ToolCalls != nil {
						// Handle single tool call object (tool-call-start/delta events)
						cohereToolCall := event.Delta.Message.ToolCalls
						toolCall := schemas.ChatAssistantMessageToolCall{}

						if cohereToolCall.ID != nil {
							toolCall.ID = cohereToolCall.ID
						}

						if cohereToolCall.Function != nil {
							if cohereToolCall.Function.Name != nil {
								toolCall.Function.Name = cohereToolCall.Function.Name
							}
							toolCall.Function.Arguments = cohereToolCall.Function.Arguments
						}

						response.Choices[0].BifrostStreamResponseChoice.Delta.ToolCalls = []schemas.ChatAssistantMessageToolCall{toolCall}
					}

				case cohere.StreamEventMessageEnd:
					if event.Delta != nil {
						// Set finish reason
						if event.Delta.FinishReason != nil {
							finishReason := string(*event.Delta.FinishReason)
							response.Choices[0].FinishReason = &finishReason
						}

						// Set usage information
						if event.Delta.Usage != nil {
							usage := &schemas.LLMUsage{}
							if event.Delta.Usage.Tokens != nil {
								if event.Delta.Usage.Tokens.InputTokens != nil {
									usage.PromptTokens = int(*event.Delta.Usage.Tokens.InputTokens)
								}
								if event.Delta.Usage.Tokens.OutputTokens != nil {
									usage.CompletionTokens = int(*event.Delta.Usage.Tokens.OutputTokens)
								}
								usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
							}
							response.Usage = usage

							// Set billed usage
							if event.Delta.Usage.BilledUnits != nil {
								response.ExtraFields.BilledUsage = &schemas.BilledLLMUsage{}
								if event.Delta.Usage.BilledUnits.InputTokens != nil {
									response.ExtraFields.BilledUsage.PromptTokens = event.Delta.Usage.BilledUnits.InputTokens
								}
								if event.Delta.Usage.BilledUnits.OutputTokens != nil {
									response.ExtraFields.BilledUsage.CompletionTokens = event.Delta.Usage.BilledUnits.OutputTokens
								}
							}
						}

						ctx = context.WithValue(ctx, schemas.BifrostContextKeyStreamEndIndicator, true)
						response.ExtraFields.Latency = time.Since(startTime).Milliseconds()
					}

				case cohere.StreamEventToolCallEnd, cohere.StreamEventContentEnd:
					// These events just signal completion, no additional data needed

				default:
					provider.logger.Debug(fmt.Sprintf("Unknown v2 stream event type: %s", event.Type))
					continue
				}

				if provider.sendBackRawResponse {
					response.ExtraFields.RawResponse = jsonData
				}

				processAndSendResponse(ctx, postHookRunner, response, responseChan, provider.logger)

				// End stream after message-end
				if event.Type == cohere.StreamEventMessageEnd {
					return
				}
			}
		}

		if err := scanner.Err(); err != nil {
			provider.logger.Warn(fmt.Sprintf("Error reading stream: %v", err))
			processAndSendError(ctx, postHookRunner, err, responseChan, schemas.ChatCompletionStreamRequest, providerName, request.Model, provider.logger)
		}
	}()

	return responseChan, nil
}

func (provider *CohereProvider) Responses(ctx context.Context, key schemas.Key, request *schemas.BifrostResponsesRequest) (*schemas.BifrostResponse, *schemas.BifrostError) {
	// Check if chat completion is allowed
	if err := checkOperationAllowed(schemas.Cohere, provider.customProviderConfig, schemas.ResponsesRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	// Convert to Cohere v2 request
	reqBody := cohere.ToCohereResponsesRequest(request)
	if reqBody == nil {
		return nil, newBifrostOperationError("responses input is not provided", nil, providerName)
	}

	cohereResponse, rawResponse, latency, err := provider.handleCohereChatCompletionRequest(ctx, reqBody, key)
	if err != nil {
		return nil, err
	}

	// Convert Cohere v2 response to Bifrost response
	bifrostResponse := cohereResponse.ToResponsesBifrostResponse()

	bifrostResponse.Model = request.Model
	bifrostResponse.ExtraFields.Provider = providerName
	bifrostResponse.ExtraFields.ModelRequested = request.Model
	bifrostResponse.ExtraFields.RequestType = schemas.ResponsesRequest
	bifrostResponse.ExtraFields.Latency = latency.Milliseconds()

	if provider.sendBackRawResponse {
		bifrostResponse.ExtraFields.RawResponse = rawResponse
	}

	return bifrostResponse, nil
}

func (provider *CohereProvider) ResponsesStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostResponsesRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	// Check if responses stream is allowed
	if err := checkOperationAllowed(schemas.Cohere, provider.customProviderConfig, schemas.ResponsesStreamRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()
	// Convert to Cohere v2 request and add streaming
	reqBody := cohere.ToCohereResponsesRequest(request)
	if reqBody == nil {
		return nil, newBifrostOperationError("responses input is not provided", nil, providerName)
	}
	reqBody.Stream = schemas.Ptr(true)

	jsonBody, err := sonic.Marshal(reqBody)
	if err != nil {
		return nil, newBifrostOperationError(schemas.ErrProviderJSONMarshaling, err, providerName)
	}

	// Create HTTP request for streaming
	req, err := http.NewRequestWithContext(ctx, "POST", provider.networkConfig.BaseURL+"/v2/chat", bytes.NewReader(jsonBody))
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
		if errors.Is(err, fasthttp.ErrTimeout) || errors.Is(err, context.DeadlineExceeded) {
			return nil, newBifrostOperationError(schemas.ErrProviderRequestTimedOut, err, providerName)
		}
		return nil, newBifrostOperationError(schemas.ErrProviderRequest, err, providerName)
	}

	// Set headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key.Value)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")

	// Set any extra headers from network config
	setExtraHeadersHTTP(req, provider.networkConfig.ExtraHeaders, nil)

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
		if errors.Is(err, fasthttp.ErrTimeout) || errors.Is(err, context.DeadlineExceeded) {
			return nil, newBifrostOperationError(schemas.ErrProviderRequestTimedOut, err, providerName)
		}
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: &schemas.ErrorField{
				Message: schemas.ErrProviderRequest,
				Error:   err,
			},
		}
	}

	// Check for HTTP errors
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, newProviderAPIError(fmt.Sprintf("HTTP error from %s: %d", providerName, resp.StatusCode), fmt.Errorf("%s", string(body)), resp.StatusCode, providerName, nil, nil)
	}

	// Create response channel
	responseChan := make(chan *schemas.BifrostStream, schemas.DefaultStreamBufferSize)

	// Start streaming in a goroutine
	go func() {
		defer close(responseChan)
		defer resp.Body.Close()

		scanner := bufio.NewScanner(resp.Body)
		chunkIndex := 0

		startTime := time.Now()
		lastChunkTime := startTime

		// Track SSE event parsing state
		var eventData string

		for scanner.Scan() {
			line := scanner.Text()

			// Skip empty lines and comments
			if line == "" || strings.HasPrefix(line, ":") {
				continue
			}

			// Parse SSE event - track event data
			if after, ok := strings.CutPrefix(line, "data: "); ok {
				eventData = after
			} else {
				continue
			}

			// Skip if we don't have event data
			if eventData == "" {
				continue
			}

			// Handle [DONE] marker
			if strings.TrimSpace(eventData) == "[DONE]" {
				provider.logger.Debug("Received [DONE] marker, ending stream")
				return
			}

			// Parse the unified streaming event
			var event cohere.CohereStreamEvent
			if err := sonic.Unmarshal([]byte(eventData), &event); err != nil {
				provider.logger.Warn(fmt.Sprintf("Failed to parse stream event: %v", err))
				continue
			}

			if chunkIndex == 0 {
				sendCreatedEventResponsesChunk(ctx, postHookRunner, providerName, request.Model, startTime, responseChan, provider.logger)
				sendInProgressEventResponsesChunk(ctx, postHookRunner, providerName, request.Model, startTime, responseChan, provider.logger)
				chunkIndex = 2
			}

			response, bifrostErr, isLastChunk := event.ToBifrostResponsesStream(chunkIndex)
			if response != nil {
				response.ExtraFields = schemas.BifrostResponseExtraFields{
					RequestType:    schemas.ResponsesStreamRequest,
					Provider:       providerName,
					ModelRequested: request.Model,
					ChunkIndex:     chunkIndex,
					Latency:        time.Since(lastChunkTime).Milliseconds(),
				}

				if event.Type == cohere.StreamEventMessageEnd && event.Delta != nil && event.Delta.Usage != nil && event.Delta.Usage.BilledUnits != nil {
					response.ExtraFields.BilledUsage = &schemas.BilledLLMUsage{}
					if event.Delta.Usage.BilledUnits.InputTokens != nil {
						response.ExtraFields.BilledUsage.PromptTokens = event.Delta.Usage.BilledUnits.InputTokens
					}
					if event.Delta.Usage.BilledUnits.OutputTokens != nil {
						response.ExtraFields.BilledUsage.CompletionTokens = event.Delta.Usage.BilledUnits.OutputTokens
					}
				}
				
				lastChunkTime = time.Now()
				chunkIndex++

				if provider.sendBackRawResponse {
					response.ExtraFields.RawResponse = eventData
				}

				if isLastChunk {
					if response.ResponsesStreamResponse == nil {
						response.ResponsesStreamResponse = &schemas.ResponsesStreamResponse{
							Response: &schemas.ResponsesStreamResponseStruct{},
						}
					} else if response.ResponsesStreamResponse.Response == nil {
						response.ResponsesStreamResponse.Response = &schemas.ResponsesStreamResponseStruct{}
					}
					response.ExtraFields.Latency = time.Since(startTime).Milliseconds()
					handleStreamEndWithSuccess(ctx, response, postHookRunner, responseChan, provider.logger)
					break
				}
				processAndSendResponse(ctx, postHookRunner, response, responseChan, provider.logger)
			}
			if bifrostErr != nil {
				bifrostErr.ExtraFields = schemas.BifrostErrorExtraFields{
					RequestType:    schemas.ResponsesStreamRequest,
					Provider:       providerName,
					ModelRequested: request.Model,
				}

				processAndSendBifrostError(ctx, postHookRunner, bifrostErr, responseChan, provider.logger)
				break
			}
		}

		if err := scanner.Err(); err != nil {
			provider.logger.Warn(fmt.Sprintf("Error reading %s stream: %v", providerName, err))
			processAndSendError(ctx, postHookRunner, err, responseChan, schemas.ResponsesStreamRequest, providerName, request.Model, provider.logger)
		}
	}()

	return responseChan, nil
}

// Embedding generates embeddings for the given input text(s) using the Cohere API.
// Supports Cohere's embedding models and returns a BifrostResponse containing the embedding(s).
func (provider *CohereProvider) Embedding(ctx context.Context, key schemas.Key, request *schemas.BifrostEmbeddingRequest) (*schemas.BifrostResponse, *schemas.BifrostError) {
	// Check if embedding is allowed
	if err := checkOperationAllowed(schemas.Cohere, provider.customProviderConfig, schemas.EmbeddingRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	// Create Bifrost request for conversion
	reqBody := cohere.ToCohereEmbeddingRequest(request)
	if reqBody == nil {
		return nil, newBifrostOperationError("embedding input is not provided", nil, providerName)
	}

	// Marshal request body
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

	req.SetRequestURI(provider.networkConfig.BaseURL + "/v2/embed")
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

		var errorResp cohere.CohereError
		bifrostErr := handleProviderAPIError(resp, &errorResp)
		bifrostErr.Error.Message = errorResp.Message

		return nil, bifrostErr
	}

	// Parse response
	var cohereResp cohere.CohereEmbeddingResponse
	if err := sonic.Unmarshal(resp.Body(), &cohereResp); err != nil {
		return nil, newBifrostOperationError("error parsing embedding response", err, providerName)
	}

	// Parse raw response for consistent format
	var rawResponse interface{}
	if err := sonic.Unmarshal(resp.Body(), &rawResponse); err != nil {
		return nil, newBifrostOperationError("error parsing raw response for embedding", err, providerName)
	}

	// Create BifrostResponse
	bifrostResponse := cohereResp.ToBifrostResponse()

	bifrostResponse.Model = request.Model
	bifrostResponse.ExtraFields.Provider = providerName
	bifrostResponse.ExtraFields.ModelRequested = request.Model
	bifrostResponse.ExtraFields.RequestType = schemas.EmbeddingRequest
	bifrostResponse.ExtraFields.Latency = latency.Milliseconds()

	// Only include RawResponse if sendBackRawResponse is enabled
	if provider.sendBackRawResponse {
		bifrostResponse.ExtraFields.RawResponse = rawResponse
	}

	return bifrostResponse, nil
}

func (provider *CohereProvider) Speech(ctx context.Context, key schemas.Key, request *schemas.BifrostSpeechRequest) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("speech", "cohere")
}

func (provider *CohereProvider) SpeechStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostSpeechRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("speech stream", "cohere")
}

func (provider *CohereProvider) Transcription(ctx context.Context, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("transcription", "cohere")
}

func (provider *CohereProvider) TranscriptionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("transcription stream", "cohere")
}
