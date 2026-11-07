package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// Class is a provider failure category.
type Class string

const (
	ClassAuth            Class = "auth"
	ClassRateLimit       Class = "rate_limit"
	ClassOverloaded      Class = "overloaded"
	ClassServer          Class = "server"
	ClassTimeout         Class = "timeout"
	ClassNetwork         Class = "network"
	ClassContextOverflow Class = "context_overflow"
	ClassInvalidRequest  Class = "invalid_request"
	ClassUnsupported     Class = "unsupported"
	ClassCanceled        Class = "canceled"
)

// ProviderError is a classified provider failure.
type ProviderError struct {
	Class      Class
	Status     int
	RetryAfter time.Duration
	Message    string
}

func (e *ProviderError) Error() string {
	if e.Status > 0 {
		return fmt.Sprintf("%s: %s (status %d)", e.Class, e.Message, e.Status)
	}
	return fmt.Sprintf("%s: %s", e.Class, e.Message)
}

// Retryable reports whether the same model may be retried immediately.
func (e *ProviderError) Retryable() bool {
	return e.Class == ClassTimeout || e.Class == ClassNetwork
}

// Fatal reports whether the model must not be retried for this request.
func (e *ProviderError) Fatal() bool {
	return e.Class == ClassAuth || e.Class == ClassInvalidRequest
}

// AsProviderError extracts a ProviderError from an error chain.
func AsProviderError(err error) (*ProviderError, bool) {
	var pe *ProviderError
	if errors.As(err, &pe) {
		return pe, true
	}
	return nil, false
}

var overflowMarkers = []string{
	"prompt is too long",
	"context_length_exceeded",
	"maximum context length",
	"too many tokens",
	"context_length",
}

var overloadedMarkers = []string{"overloaded_error", "overloaded"}

// Classify builds a ProviderError for an HTTP failure.
func Classify(ctx context.Context, kind string, status int, body string, retryAfter time.Duration, err error) *ProviderError {
	if ctx != nil && ctx.Err() != nil {
		return &ProviderError{Class: ClassCanceled, Status: status, Message: "canceled"}
	}
	if err != nil {
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return &ProviderError{Class: ClassTimeout, Status: status, Message: err.Error()}
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return &ProviderError{Class: ClassTimeout, Status: status, Message: "deadline exceeded"}
		}
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return &ProviderError{Class: ClassNetwork, Status: status, Message: "connection closed"}
		}
		return &ProviderError{Class: ClassNetwork, Status: status, Message: err.Error()}
	}

	lower := strings.ToLower(body)
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return &ProviderError{Class: ClassAuth, Status: status, RetryAfter: retryAfter, Message: truncate(body, 400)}
	case status == http.StatusTooManyRequests:
		return &ProviderError{Class: ClassRateLimit, Status: status, RetryAfter: retryAfter, Message: truncate(body, 400)}
	case status == 529 || containsAny(lower, overloadedMarkers):
		return &ProviderError{Class: ClassOverloaded, Status: status, RetryAfter: retryAfter, Message: truncate(body, 400)}
	case status == http.StatusRequestEntityTooLarge || containsAny(lower, overflowMarkers):
		return &ProviderError{Class: ClassContextOverflow, Status: status, Message: truncate(body, 400)}
	case (status == http.StatusNotFound || status == http.StatusMethodNotAllowed) &&
		(kind == "openai-responses" || kind == "anthropic"):
		return &ProviderError{Class: ClassUnsupported, Status: status, Message: truncate(body, 400)}
	case status >= 500:
		return &ProviderError{Class: ClassServer, Status: status, RetryAfter: retryAfter, Message: truncate(body, 400)}
	}
	return &ProviderError{Class: ClassInvalidRequest, Status: status, Message: truncate(body, 400)}
}

func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// StreamError classifies an in-stream provider error object.
func StreamError(kind, typ, msg string) *ProviderError {
	lower := strings.ToLower(typ + " " + msg)
	switch {
	case strings.Contains(lower, "overloaded"):
		return &ProviderError{Class: ClassOverloaded, Message: msg}
	case strings.Contains(lower, "rate_limit") || strings.Contains(lower, "rate limit"):
		return &ProviderError{Class: ClassRateLimit, Message: msg}
	case strings.Contains(lower, "not_found") || strings.Contains(lower, "unsupported"):
		return &ProviderError{Class: ClassUnsupported, Message: msg}
	case containsAny(lower, overflowMarkers):
		return &ProviderError{Class: ClassContextOverflow, Message: msg}
	}
	return &ProviderError{Class: ClassInvalidRequest, Message: msg}
}
