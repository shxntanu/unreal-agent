package litellm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/llm/responsesapi"
	"github.com/unreallabsai/unreal-agent/harness/primitives"
)

const BaseURL = "http://localhost:4000/v1"

const maxResponseBytes = 4 << 20

var emptyObjectSchema = json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)

type Config struct {
	APIKey      string
	BaseURL     string
	HTTPClient  *http.Client
	MaxAttempts *int
	Trace       func(responsesapi.Exchange)
}

type Client struct {
	baseURL         string
	apiKey          string
	httpClient      *http.Client
	maxAttempts     int
	trace           func(responsesapi.Exchange)
	responses       llm.Adapter
	responsesRemote *primitives.RemoteClient
}

var _ llm.Adapter = (*Client)(nil)

func NewClient(config Config) (*Client, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(config.BaseURL), "/")
	if baseURL == "" {
		baseURL = BaseURL
	}
	maxAttempts := responsesapi.DefaultMaxAttempts
	if config.MaxAttempts != nil {
		maxAttempts = *config.MaxAttempts
	}
	if maxAttempts <= 0 {
		return nil, errors.New("max attempts must be positive")
	}
	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{}
	}

	remote := primitives.NewRemoteClient()
	headers := map[string][]string{"Content-Type": {"application/json"}}
	if strings.TrimSpace(config.APIKey) != "" {
		headers["Authorization"] = []string{"Bearer " + config.APIKey}
	}
	responses, err := responsesapi.NewAdapter(remote, responsesapi.Config{
		Endpoint:    baseURL + "/responses",
		Headers:     headers,
		MaxAttempts: &maxAttempts,
		Trace:       config.Trace,
		CacheKeyPlacement: responsesapi.CacheKeyPlacement{
			UsePromptCacheKeyField: true,
		},
	})
	if err != nil {
		_ = remote.Close()
		return nil, err
	}

	return &Client{
		baseURL:         baseURL,
		apiKey:          config.APIKey,
		httpClient:      httpClient,
		maxAttempts:     maxAttempts,
		trace:           config.Trace,
		responses:       responses,
		responsesRemote: remote,
	}, nil
}

func (client *Client) Respond(ctx context.Context, request llm.Request, options llm.RequestOptions) (llm.Response, error) {
	if shouldUseResponsesAPI(request) {
		return client.responses.Respond(ctx, request, options)
	}

	model := strings.TrimSpace(request.Model.ID)
	if model == "" {
		return llm.Response{}, errors.New("litellm model must be set")
	}
	messages, err := convertMessages(request.Input)
	if err != nil {
		return llm.Response{}, err
	}
	tools, err := convertTools(request.Tools)
	if err != nil {
		return llm.Response{}, err
	}
	body, err := json.Marshal(chatCompletionRequest{
		Model:           model,
		Messages:        messages,
		Tools:           tools,
		ReasoningEffort: string(request.Model.ReasoningEffort),
	})
	if err != nil {
		return llm.Response{}, fmt.Errorf("encode LiteLLM request: %w", err)
	}

	responseBody, statusCode, err := client.post(ctx, "/chat/completions", body)
	if err != nil {
		return llm.Response{}, err
	}
	if statusCode < http.StatusOK || statusCode >= http.StatusMultipleChoices {
		return llm.Response{}, fmt.Errorf("LiteLLM request failed: %w", providerError(statusCode, responseBody))
	}

	var decoded chatCompletionResponse
	if err := json.Unmarshal(responseBody, &decoded); err != nil {
		return llm.Response{}, fmt.Errorf("decode LiteLLM response: %w", err)
	}
	if len(decoded.Choices) == 0 {
		return llm.Response{}, errors.New("LiteLLM response has no choices")
	}
	return convertResponse(decoded), nil
}

func (client *Client) post(ctx context.Context, path string, body []byte) ([]byte, int, error) {
	var lastErr error
	for attempt := 0; attempt < client.maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, client.baseURL+path, bytes.NewReader(body))
		if err != nil {
			return nil, 0, fmt.Errorf("create LiteLLM request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		if strings.TrimSpace(client.apiKey) != "" {
			req.Header.Set("Authorization", "Bearer "+client.apiKey)
		}

		resp, err := client.httpClient.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, 0, ctx.Err()
			}
			lastErr = fmt.Errorf("send LiteLLM request: %w", err)
			continue
		}
		responseBody, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
		closeErr := resp.Body.Close()
		if readErr != nil {
			return nil, resp.StatusCode, fmt.Errorf("read LiteLLM response: %w", readErr)
		}
		if closeErr != nil {
			return nil, resp.StatusCode, fmt.Errorf("close LiteLLM response: %w", closeErr)
		}
		if len(responseBody) > maxResponseBytes {
			return nil, resp.StatusCode, errors.New("LiteLLM response exceeds the size limit")
		}
		if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
			if client.trace != nil {
				client.trace(responsesapi.Exchange{RequestBody: body, StatusCode: resp.StatusCode, ResponseBody: responseBody})
			}
			return responseBody, resp.StatusCode, nil
		}

		lastErr = providerError(resp.StatusCode, responseBody)
		if !retryable(resp.StatusCode) {
			return responseBody, resp.StatusCode, nil
		}
	}
	if lastErr == nil {
		lastErr = errors.New("request attempts exhausted")
	}
	return nil, 0, lastErr
}

type chatCompletionRequest struct {
	Model           string        `json:"model"`
	Messages        []chatMessage `json:"messages"`
	Tools           []chatTool    `json:"tools,omitempty"`
	ReasoningEffort string        `json:"reasoning_effort,omitempty"`
}

type chatMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content,omitempty"`
	ToolCalls  []chatToolCall  `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
}

type chatTool struct {
	Type     string       `json:"type"`
	Function chatFunction `json:"function"`
}

type chatFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

type chatToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type,omitempty"`
	Function chatCallFunction `json:"function"`
}

type chatCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type chatCompletionResponse struct {
	ID      string `json:"id"`
	Choices []struct {
		Message      chatMessage `json:"message"`
		FinishReason string      `json:"finish_reason"`
	} `json:"choices"`
	Usage chatUsage `json:"usage"`
}

type chatUsage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
}

func convertMessages(items []llm.Item) ([]chatMessage, error) {
	messages := make([]chatMessage, 0, len(items))
	for index, item := range items {
		switch item.Type {
		case llm.ItemMessage:
			message, ok := item.Data.(llm.Message)
			if !ok {
				return nil, fmt.Errorf("input item %d message data must be llm.Message, got %T", index, item.Data)
			}
			content, err := json.Marshal(message.Text)
			if err != nil {
				return nil, fmt.Errorf("encode input item %d: %w", index, err)
			}
			messages = append(messages, chatMessage{Role: string(message.Role), Content: content})
		case llm.ItemToolCall:
			call, ok := item.Data.(llm.ToolCall)
			if !ok {
				return nil, fmt.Errorf("input item %d tool call data must be llm.ToolCall, got %T", index, item.Data)
			}
			messages = append(messages, chatMessage{
				Role: "assistant",
				ToolCalls: []chatToolCall{{
					ID: call.CallID, Type: "function",
					Function: chatCallFunction{Name: call.Name, Arguments: call.Arguments},
				}},
			})
		case llm.ItemToolResult:
			result, ok := item.Data.(llm.ToolResult)
			if !ok {
				return nil, fmt.Errorf("input item %d tool result data must be llm.ToolResult, got %T", index, item.Data)
			}
			content, err := toolResultContent(result)
			if err != nil {
				return nil, fmt.Errorf("encode input item %d: %w", index, err)
			}
			messages = append(messages, chatMessage{Role: "tool", ToolCallID: result.CallID, Content: content})
		case llm.ItemReasoning:
			// Chat Completions has no portable representation for Responses reasoning items.
		default:
			return nil, fmt.Errorf("input item %d has unsupported type %q", index, item.Type)
		}
	}
	return messages, nil
}

type chatContentPart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL *struct {
		URL string `json:"url"`
	} `json:"image_url,omitempty"`
}

func toolResultContent(result llm.ToolResult) (json.RawMessage, error) {
	if len(result.Output) == 0 {
		return json.Marshal("")
	}
	parts := make([]chatContentPart, 0, len(result.Output))
	hasImage := false
	var text strings.Builder
	for _, output := range result.Output {
		switch output.Kind {
		case llm.ToolResultText:
			text.WriteString(output.Value)
			parts = append(parts, chatContentPart{Type: "text", Text: output.Value})
		case llm.ToolResultImage:
			hasImage = true
			parts = append(parts, chatContentPart{
				Type: "image_url",
				ImageURL: &struct {
					URL string `json:"url"`
				}{URL: output.Value},
			})
		default:
			return nil, fmt.Errorf("unsupported tool result kind %q", output.Kind)
		}
	}
	if !hasImage {
		return json.Marshal(text.String())
	}
	return json.Marshal(parts)
}

func convertTools(tools []llm.Tool) ([]chatTool, error) {
	converted := make([]chatTool, 0, len(tools))
	for _, tool := range tools {
		parameters := emptyObjectSchema
		if tool.Parameters != nil {
			encoded, err := json.Marshal(tool.Parameters)
			if err != nil {
				return nil, fmt.Errorf("encode tool %q parameters: %w", tool.Name, err)
			}
			parameters = encoded
		}
		converted = append(converted, chatTool{
			Type: "function",
			Function: chatFunction{
				Name: tool.Name, Description: tool.Description, Parameters: parameters,
			},
		})
	}
	return converted, nil
}

func convertResponse(response chatCompletionResponse) llm.Response {
	choice := response.Choices[0]
	message := choice.Message
	output := make([]llm.Item, 0, 1+len(message.ToolCalls))
	if text := textContent(message.Content); text != "" {
		output = append(output, llm.Item{
			Type: llm.ItemMessage,
			Data: llm.Message{Role: llm.RoleAssistant, Text: text},
		})
	}
	for _, call := range message.ToolCalls {
		output = append(output, llm.Item{
			ProviderID: call.ID,
			Type:       llm.ItemToolCall,
			Data:       llm.ToolCall{CallID: call.ID, Name: call.Function.Name, Arguments: call.Function.Arguments},
		})
	}
	stop := llm.StopComplete
	switch choice.FinishReason {
	case "length", "max_tokens":
		stop = llm.StopMaxOutputTokens
	case "content_filter", "refusal":
		stop = llm.StopRefused
	}
	return llm.Response{
		ID: response.ID, Stop: stop, Output: output,
		Usage: llm.Usage{
			InputTokens: response.Usage.PromptTokens, OutputTokens: response.Usage.CompletionTokens,
		},
	}
}

func textContent(content json.RawMessage) string {
	var text string
	if json.Unmarshal(content, &text) == nil {
		return text
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(content, &parts) == nil {
		for _, part := range parts {
			if part.Type == "text" || part.Type == "output_text" {
				text += part.Text
			}
		}
	}
	return text
}

func providerError(statusCode int, body []byte) error {
	var envelope struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &envelope) == nil && envelope.Error.Message != "" {
		return fmt.Errorf("status %d: %s", statusCode, envelope.Error.Message)
	}
	message := strings.TrimSpace(string(body))
	if message == "" {
		message = http.StatusText(statusCode)
	}
	return fmt.Errorf("status %d: %s", statusCode, message)
}

func retryable(statusCode int) bool {
	return statusCode == http.StatusTooManyRequests || statusCode >= http.StatusInternalServerError
}

func shouldUseResponsesAPI(request llm.Request) bool {
	if request.Model.ReasoningEffort == "" || len(request.Tools) == 0 {
		return false
	}
	model := strings.TrimSpace(request.Model.ID)
	if provider, name, ok := strings.Cut(model, "/"); ok && provider == "openai" {
		model = name
	}
	return model == "gpt-5.5" || strings.HasPrefix(model, "gpt-5.5-")
}

func (client *Client) Close() error {
	return client.responsesRemote.Close()
}
