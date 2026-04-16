// Package proxy implements the reverse-proxy handler with automatic retry on overload.
package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"anthropic-proxy/internal/config"
	"anthropic-proxy/internal/provider"
	"anthropic-proxy/internal/storage"

	"github.com/pkoukk/tiktoken-go"
)

// New returns an http.Handler that forwards every request to cfg.Upstream,
// automatically retrying when the response matches an overload rule.
func New(cfg *config.Config, client *http.Client, store *storage.Storage) http.Handler {
	return &handler{cfg: cfg, client: client, store: store}
}

type handler struct {
	cfg    *config.Config
	client *http.Client
	store  *storage.Storage
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	label := h.cfg.ProviderName
	target := h.cfg.Upstream + r.RequestURI
	start := time.Now()
	requestStartMs := start.UnixMilli()

	slog.Info("->", "method", r.Method, "path", r.URL.Path)

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read request body", http.StatusInternalServerError)
		return
	}
	r.Body.Close()

	// rule is locked in on the first overload match and reused for subsequent retries.
	var rule *provider.Rule

	for attempt := 0; ; attempt++ {
		if rule != nil {
			if attempt > rule.MaxRetries {
				slog.Warn("max retries reached, giving up",
					"provider", label, "max", rule.MaxRetries)
				break
			}
			wait := rule.RetryDelay + time.Duration(attempt)*rule.RetryJitter
			slog.Info("retry",
				"provider", label, "attempt", attempt,
				"max", rule.MaxRetries, "wait", wait, "path", r.URL.Path)

			select {
			case <-r.Context().Done():
				http.Error(w, "client disconnected", http.StatusGatewayTimeout)
				return
			case <-time.After(wait):
			}
		}

		resp, err := h.do(r.Context(), r.Method, target, r.Header, body)
		if err != nil {
			if rule != nil && attempt >= rule.MaxRetries {
				slog.Error("upstream failed", "provider", label, "attempts", attempt+1, "err", err)
				http.Error(w, "upstream error: "+err.Error(), http.StatusBadGateway)
				return
			}
			slog.Warn("upstream error, will retry", "provider", label, "attempt", attempt+1, "err", err)
			if rule == nil {
				rule = &h.cfg.OverloadRules[0] // use first rule's params for connection errors
			}
			continue
		}

		// 2xx: stream and capture response
		if resp.StatusCode < 400 {
			slog.Info("<-",
				"status", resp.StatusCode, "path", r.URL.Path,
				"attempts", attempt+1, "elapsed", time.Since(start).Round(time.Millisecond))
			respBody, ttfb := streamWithCapture(w, resp)
			ttfbMs := ttfb - requestStartMs
			h.logRequest(r, body, respBody, resp.StatusCode, time.Since(start).Milliseconds(), ttfbMs, attempt, true, "")
			return
		}

		// Error response: buffer to check for overload
		errBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if matched := provider.Match(h.cfg.OverloadRules, resp.StatusCode, errBody); matched != nil {
			if rule == nil {
				rule = matched // lock in retry params from the first matched rule
			}
			continue // wait and retry handled at the top of the loop
		}

		// Non-overload error: forward as-is
		h.logRequest(r, body, errBody, resp.StatusCode, time.Since(start).Milliseconds(), 0, attempt, false, string(errBody))
		forward(w, resp, errBody)
		return
	}

	// Fell through: still overloaded after max retries — return last error response
	// Re-issue one final request just to get the response to forward.
	// (We can't reuse the previous resp.Body as it was already read.)
	resp, err := h.do(r.Context(), r.Method, target, r.Header, body)
	if err != nil {
		http.Error(w, "upstream error: "+err.Error(), http.StatusBadGateway)
		return
	}
	errBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	h.logRequest(r, body, errBody, resp.StatusCode, time.Since(start).Milliseconds(), 0, rule.MaxRetries, false, string(errBody))
	forward(w, resp, errBody)
}

func (h *handler) do(ctx context.Context, method, url string, headers http.Header, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	for k, vs := range headers {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	return h.client.Do(req)
}

// streamWithCapture writes a successful response with SSE-friendly chunked flushing,
// captures the full response body and TTFB (time to first byte).
func streamWithCapture(w http.ResponseWriter, resp *http.Response) ([]byte, int64) {
	defer resp.Body.Close()
	copyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)

	flusher, ok := w.(http.Flusher)
	buf := make([]byte, 4096)
	var captured []byte
	var ttfb int64
	firstByte := true

	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if firstByte {
				ttfb = time.Now().UnixMilli()
				firstByte = false
			}
			captured = append(captured, buf[:n]...)
			_, _ = w.Write(buf[:n])
			if ok {
				flusher.Flush()
			}
		}
		if err != nil {
			break
		}
	}
	return captured, ttfb
}

// forward writes a buffered (error) response back to the client.
func forward(w http.ResponseWriter, resp *http.Response, body []byte) {
	copyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
}

func copyHeaders(dst, src http.Header) {
	for k, vs := range src {
		if strings.EqualFold(k, "Content-Length") {
			continue
		}
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}

func (h *handler) logRequest(r *http.Request, reqBody, respBody []byte, statusCode int, elapsedMs, ttfbMs int64, attempt int, isSSE bool, errorBody string) {
	if h.store == nil {
		return
	}
	headersJSON, _ := json.Marshal(r.Header)
	model := extractModel(reqBody)
	inputTokens, outputTokens := countTokens(model, reqBody, respBody)

	h.store.InsertLog(&storage.RequestLog{
		Method:       r.Method,
		Path:         r.URL.Path,
		Model:        model,
		Headers:      string(headersJSON),
		RequestBody:  string(reqBody),
		ResponseBody: string(respBody),
		StatusCode:   statusCode,
		ElapsedMs:    elapsedMs,
		TTFBMs:       ttfbMs,
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		RetryCount:   attempt,
		IsSSE:        isSSE,
		ErrorBody:    errorBody,
	})
}

func extractModel(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var req struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return ""
	}
	return req.Model
}

// countTokens counts input and output tokens using tiktoken.
func countTokens(model string, reqBody, respBody []byte) (int, int) {
	// Determine encoding based on model
	encoding := getEncodingForModel(model)

	tke, err := tiktoken.GetEncoding(encoding)
	if err != nil {
		// Fallback to cl100k_base (used by Claude and GPT-4)
		tke, err = tiktoken.GetEncoding("cl100k_base")
		if err != nil {
			return 0, 0
		}
	}

	inputTokens := 0
	outputTokens := 0

	// Count input tokens from messages
	if len(reqBody) > 0 {
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content any    `json:"content"`
			} `json:"messages"`
			System string `json:"system"`
		}
		if err := json.Unmarshal(reqBody, &req); err == nil {
			if req.System != "" {
				inputTokens += len(tke.Encode(req.System, nil, nil))
			}
			for _, msg := range req.Messages {
				inputTokens += len(tke.Encode(msg.Role, nil, nil))
				switch c := msg.Content.(type) {
				case string:
					inputTokens += len(tke.Encode(c, nil, nil))
				case []interface{}:
					for _, item := range c {
						if m, ok := item.(map[string]interface{}); ok {
							if text, ok := m["text"].(string); ok {
								inputTokens += len(tke.Encode(text, nil, nil))
							}
						}
					}
				}
			}
		}
	}

	// Count output tokens from SSE response
	if len(respBody) > 0 {
		outputTokens = countSSEOutputTokens(respBody, tke)
	}

	return inputTokens, outputTokens
}

func getEncodingForModel(model string) string {
	// Claude models use cl100k_base
	if strings.Contains(strings.ToLower(model), "claude") {
		return "cl100k_base"
	}
	// GPT-4, GPT-3.5 also use cl100k_base
	if strings.Contains(strings.ToLower(model), "gpt") {
		return "cl100k_base"
	}
	return "cl100k_base"
}

// countSSEOutputTokens extracts text content from SSE events and counts tokens.
func countSSEOutputTokens(body []byte, tke *tiktoken.Tiktoken) int {
	lines := strings.Split(string(body), "\n")
	var textContent strings.Builder

	for _, line := range lines {
		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				continue
			}

			var event map[string]interface{}
			if err := json.Unmarshal([]byte(data), &event); err != nil {
				continue
			}

			// Handle Anthropic/Claude format
			if eventType, ok := event["type"].(string); ok {
				switch eventType {
				case "content_block_delta":
					if delta, ok := event["delta"].(map[string]interface{}); ok {
						if text, ok := delta["text"].(string); ok {
							textContent.WriteString(text)
						}
					}
				case "message_start":
					// Initial message, no text content
				}
			}

			// Handle OpenAI format
			if choices, ok := event["choices"].([]interface{}); ok {
				for _, choice := range choices {
					if c, ok := choice.(map[string]interface{}); ok {
						if delta, ok := c["delta"].(map[string]interface{}); ok {
							if content, ok := delta["content"].(string); ok {
								textContent.WriteString(content)
							}
						}
					}
				}
			}
		}
	}

	return len(tke.Encode(textContent.String(), nil, nil))
}
