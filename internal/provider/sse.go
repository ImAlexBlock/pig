package provider

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Provider HTTP timeouts (see plan: connect 10s, first event 60s, gap 120s,
// whole request 600s).
const (
	ConnectTimeout      = 10 * time.Second
	FirstEventTimeout   = 60 * time.Second
	EventGapTimeout     = 120 * time.Second
	WholeRequestTimeout = 600 * time.Second
)

// HTTPClient returns the shared streaming HTTP client.
func HTTPClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   ConnectTimeout,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			TLSHandshakeTimeout:   ConnectTimeout,
			ResponseHeaderTimeout: FirstEventTimeout,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          8,
			IdleConnTimeout:       60 * time.Second,
		},
	}
}

type sseScanner struct {
	sc *bufio.Scanner
}

func newSSEScanner(r io.Reader) *sseScanner {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	return &sseScanner{sc: sc}
}

// Next returns the next SSE event name and payload. An empty name means the
// default "message" event.
func (s *sseScanner) Next() (event string, data string, ok bool) {
	var name string
	var payload strings.Builder
	for s.sc.Scan() {
		line := s.sc.Text()
		if line == "" {
			if payload.Len() > 0 || name != "" {
				return name, payload.String(), true
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		f, v, _ := strings.Cut(line, ":")
		v = strings.TrimPrefix(v, " ")
		switch f {
		case "event":
			name = v
		case "data":
			if payload.Len() > 0 {
				payload.WriteString("\n")
			}
			payload.WriteString(v)
		}
	}
	if payload.Len() > 0 {
		return name, payload.String(), true
	}
	return "", "", false
}

func (s *sseScanner) Err() error { return s.sc.Err() }

// errStreamDone signals normal stream completion from a handler.
var errStreamDone = &doneError{}

type doneError struct{}

func (*doneError) Error() string { return "stream done" }

// pumpSSE reads SSE events in a goroutine and calls handle for each one,
// enforcing first-event and inter-event timeouts.
func pumpSSE(ctx context.Context, body io.ReadCloser, first, gap time.Duration, handle func(event, data string) error) error {
	pctx, cancel := context.WithCancel(ctx)
	defer cancel()

	beat := make(chan struct{}, 1)
	errc := make(chan error, 1)
	go func() {
		defer body.Close()
		sc := newSSEScanner(body)
		for {
			ev, data, ok := sc.Next()
			if !ok {
				if err := sc.Err(); err != nil && pctx.Err() == nil {
					errc <- err
					return
				}
				errc <- nil
				return
			}
			select {
			case beat <- struct{}{}:
			default:
			}
			if err := handle(ev, data); err != nil {
				errc <- err
				return
			}
		}
	}()

	timer := time.NewTimer(first)
	defer timer.Stop()
	for {
		select {
		case err := <-errc:
			if err == errStreamDone {
				return nil
			}
			return err
		case <-beat:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(gap)
		case <-timer.C:
			cancel()
			<-errc
			return &ProviderError{Class: ClassTimeout, Message: "stream idle timeout"}
		case <-ctx.Done():
			cancel()
			<-errc
			return &ProviderError{Class: ClassCanceled, Message: "canceled"}
		}
	}
}

// postStream issues a streaming POST and returns the body on 2xx.
func postStream(ctx context.Context, client *http.Client, url string, headers map[string]string, payload []byte) (io.ReadCloser, *ProviderError) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(payload)))
	if err != nil {
		return nil, &ProviderError{Class: ClassInvalidRequest, Message: err.Error()}
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("accept", "text/event-stream")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, Classify(ctx, "", 0, "", 0, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		resp.Body.Close()
		return nil, Classify(ctx, "", resp.StatusCode, string(b), parseRetryAfter(resp.Header.Get("retry-after")), nil)
	}
	return resp.Body, nil
}

func parseRetryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		d := time.Until(t)
		if d > 0 {
			return d
		}
	}
	return 0
}

// splitDataLines returns the JSON data text of an SSE payload.
func splitDataLines(data string) string {
	return strings.TrimSpace(data)
}
