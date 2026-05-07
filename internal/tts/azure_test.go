package tts

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type trackingBody struct {
	*strings.Reader
	closed bool
}

func (b *trackingBody) Close() error {
	b.closed = true
	return nil
}

func TestAzureRetryTransportErrorThenSuccess(t *testing.T) {
	restore := stubAzureBackoff(t)
	defer restore()

	attempts := 0
	client := &AzureClient{
		key:      "key",
		endpoint: "https://tts.test",
		http: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			attempts++
			if attempts == 1 {
				return nil, io.ErrUnexpectedEOF
			}
			return azureTestResponse(http.StatusOK, "mp3"), nil
		})},
	}

	body, err := client.SynthesizeSSML(context.Background(), `<speak>hello</speak>`)
	if err != nil {
		t.Fatalf("SynthesizeSSML: %v", err)
	}
	defer body.Close()
	got, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != "mp3" {
		t.Fatalf("body = %q, want mp3", got)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
}

func TestAzureRetry500ThenSuccessClosesFailedBody(t *testing.T) {
	restore := stubAzureBackoff(t)
	defer restore()

	failedBody := &trackingBody{Reader: strings.NewReader("temporary")}
	attempts := 0
	client := &AzureClient{
		key:      "key",
		endpoint: "https://tts.test",
		http: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			attempts++
			if attempts == 1 {
				return &http.Response{
					StatusCode: http.StatusInternalServerError,
					Body:       failedBody,
					Header:     make(http.Header),
				}, nil
			}
			return azureTestResponse(http.StatusOK, "ok"), nil
		})},
	}

	body, err := client.SynthesizeSSML(context.Background(), `<speak>hello</speak>`)
	if err != nil {
		t.Fatalf("SynthesizeSSML: %v", err)
	}
	body.Close()
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
	if !failedBody.closed {
		t.Fatalf("retryable failed response body was not closed")
	}
}

func TestAzureRetries429(t *testing.T) {
	restore := stubAzureBackoff(t)
	defer restore()

	attempts := 0
	client := &AzureClient{
		key:      "key",
		endpoint: "https://tts.test",
		http: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			attempts++
			if attempts == 1 {
				return azureTestResponse(http.StatusTooManyRequests, "slow down"), nil
			}
			return azureTestResponse(http.StatusOK, "ok"), nil
		})},
	}

	body, err := client.SynthesizeSSML(context.Background(), `<speak>hello</speak>`)
	if err != nil {
		t.Fatalf("SynthesizeSSML: %v", err)
	}
	body.Close()
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
}

func TestAzureDoesNotRetry400(t *testing.T) {
	restore := stubAzureBackoff(t)
	defer restore()

	attempts := 0
	client := &AzureClient{
		key:      "key",
		endpoint: "https://tts.test",
		http: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			attempts++
			return azureTestResponse(http.StatusBadRequest, "bad ssml"), nil
		})},
	}

	_, err := client.SynthesizeSSML(context.Background(), `<speak>hello</speak>`)
	if err == nil {
		t.Fatalf("SynthesizeSSML succeeded, want error")
	}
	if !strings.Contains(err.Error(), "tts status 400: bad ssml") {
		t.Fatalf("err = %v, want 400 status body", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
}

func TestAzureContextCancellationStopsBackoff(t *testing.T) {
	oldBackoff := azureRetryBackoff
	azureRetryBackoff = func(int) time.Duration { return time.Hour }
	defer func() { azureRetryBackoff = oldBackoff }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	attempts := 0
	client := &AzureClient{
		key:      "key",
		endpoint: "https://tts.test",
		http: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			attempts++
			cancel()
			return nil, io.ErrUnexpectedEOF
		})},
	}

	_, err := client.SynthesizeSSML(ctx, `<speak>hello</speak>`)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
}

func azureTestResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

func stubAzureBackoff(t *testing.T) func() {
	t.Helper()
	old := azureRetryBackoff
	azureRetryBackoff = func(int) time.Duration { return time.Millisecond }
	return func() { azureRetryBackoff = old }
}
