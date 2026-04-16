package storage

// RequestLog represents a single request/response log entry.
type RequestLog struct {
	ID           int64  `json:"id"`
	CreatedAt    string `json:"created_at"`
	Method       string `json:"method"`
	Path         string `json:"path"`
	Model        string `json:"model"`
	Headers      string `json:"headers"`
	RequestBody  string `json:"request_body"`
	ResponseBody string `json:"response_body"`
	StatusCode   int    `json:"status_code"`
	ElapsedMs    int64  `json:"elapsed_ms"`
	TTFBMs       int64  `json:"ttfb_ms"`
	InputTokens  int    `json:"input_tokens"`
	OutputTokens int    `json:"output_tokens"`
	RetryCount   int    `json:"retry_count"`
	IsSSE        bool   `json:"is_sse"`
	ErrorBody    string `json:"error_body,omitempty"`
}
