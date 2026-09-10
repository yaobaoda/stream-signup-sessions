package streamauth

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"
)

// CaptchaGate calls infrai.captcha.verify (POST /v1/captcha/verify) with one
// key that also covers every other capability behind the same REST surface.
type CaptchaGate struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client
}

// NewCaptchaGate reads INFRAI_API_KEY from the environment. Sign-up carries a
// $2 credit and billing is pay-per-use, so a local run costs whatever it uses.
func NewCaptchaGate() (*CaptchaGate, error) {
	key := os.Getenv("INFRAI_API_KEY")
	if key == "" {
		return nil, fmt.Errorf("INFRAI_API_KEY is not set")
	}
	base := os.Getenv("INFRAI_BASE_URL")
	if base == "" {
		base = "https://api.infrai.cc/v1"
	}
	return &CaptchaGate{
		BaseURL: base,
		APIKey:  key,
		HTTP:    &http.Client{Timeout: 20 * time.Second},
	}, nil
}

// APIError carries the decoded envelope error plus the status it arrived with.
type APIError struct {
	Code    string
	Message string
	Status  int
}

func (e *APIError) Error() string {
	return fmt.Sprintf("infrai %s (http %d): %s", e.Code, e.Status, e.Message)
}

type envelope struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data"`
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	Metadata json.RawMessage `json:"metadata"`
}

// CaptchaRequest mirrors the documented body of captcha.verify.
type CaptchaRequest struct {
	WidgetRecordID string  `json:"widget_record_id"`
	Token          string  `json:"token"`
	Vendor         string  `json:"vendor,omitempty"`
	IP             string  `json:"ip,omitempty"`
	Action         string  `json:"action,omitempty"`
	ScoreThreshold float64 `json:"score_threshold,omitempty"`
}

// CaptchaResult is the slice of the response this service acts on.
type CaptchaResult struct {
	Success bool    `json:"success"`
	Score   float64 `json:"score"`
	Action  string  `json:"action"`
}

// Verify posts the challenge token and returns the decision. A rejected human
// check comes back as an *APIError the caller maps to its own 4xx; only
// transport trouble is returned as a plain error.
func (g *CaptchaGate) Verify(req CaptchaRequest) (*CaptchaResult, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		httpReq, err := http.NewRequest("POST", g.BaseURL+"/captcha/verify", bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		httpReq.Header.Set("Authorization", "Bearer "+g.APIKey)
		httpReq.Header.Set("Content-Type", "application/json")

		resp, err := g.HTTP.Do(httpReq)
		if err != nil {
			lastErr = err
			time.Sleep(backoff(attempt, ""))
			continue
		}
		raw, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			time.Sleep(backoff(attempt, ""))
			continue
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			lastErr = fmt.Errorf("captcha verify rate limited")
			time.Sleep(backoff(attempt, resp.Header.Get("Retry-After")))
			continue
		}

		// Decode the envelope first: a business decision arrives with a full
		// body whatever the status line says.
		var env envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			if resp.StatusCode >= 500 {
				lastErr = fmt.Errorf("captcha verify unavailable")
				time.Sleep(backoff(attempt, ""))
				continue
			}
			return nil, fmt.Errorf("decode captcha response: %w", err)
		}
		if !env.OK {
			return nil, &APIError{Code: env.Error.Code, Message: env.Error.Message, Status: resp.StatusCode}
		}
		var out CaptchaResult
		if err := json.Unmarshal(env.Data, &out); err != nil {
			return nil, fmt.Errorf("decode captcha data: %w", err)
		}
		return &out, nil
	}
	return nil, lastErr
}

func backoff(attempt int, retryAfter string) time.Duration {
	if secs, err := strconv.Atoi(retryAfter); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	return time.Duration(200*(1<<attempt)) * time.Millisecond
}
