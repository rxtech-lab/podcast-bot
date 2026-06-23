package imagegen

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestNewReadsGeminiKeyFromDotEnv(t *testing.T) {
	t.Setenv("AI_GATEWAY_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")

	dir := t.TempDir()
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir temp: %v", err)
	}
	defer func() {
		if err := os.Chdir(oldwd); err != nil {
			t.Fatalf("restore cwd: %v", err)
		}
	}()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("GEMINI_API_KEY=dotenv-gemini-key\n"), 0o600); err != nil {
		t.Fatalf("write .env: %v", err)
	}

	client, err := New("")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if client.geminiKey != "dotenv-gemini-key" {
		t.Fatalf("geminiKey = %q", client.geminiKey)
	}
}

func TestGenerateGeminiImageUsesNativeInteractionsAPI(t *testing.T) {
	t.Setenv("AI_GATEWAY_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "gemini-test-key")

	oldGeminiURL := GeminiInteractionsURL
	defer func() { GeminiInteractionsURL = oldGeminiURL }()

	var got map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/interactions" {
			t.Fatalf("path = %s, want /v1beta/interactions", r.URL.Path)
		}
		if r.Header.Get("x-goog-api-key") != "gemini-test-key" {
			t.Fatalf("x-goog-api-key = %q", r.Header.Get("x-goog-api-key"))
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"output_image":{"data":"aW1hZ2UtYnl0ZXM="}}`))
	}))
	defer server.Close()
	GeminiInteractionsURL = server.URL + "/v1beta/interactions"

	client, err := New("")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	raw, err := client.Generate(context.Background(), Request{
		Model:  "google/gemini-3.1-flash-image",
		Prompt: "Square podcast cover artwork",
		Size:   "1024x1024",
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if string(raw) != "image-bytes" {
		t.Fatalf("raw = %q", string(raw))
	}
	if got["model"] != "gemini-3.1-flash-image" {
		t.Fatalf("model = %v", got["model"])
	}
	format, ok := got["response_format"].(map[string]any)
	if !ok {
		t.Fatalf("response_format = %#v", got["response_format"])
	}
	if format["type"] != "image" {
		t.Fatalf("response_format.type = %v", format["type"])
	}
	if format["mime_type"] != "image/jpeg" {
		t.Fatalf("response_format.mime_type = %v", format["mime_type"])
	}
	if format["aspect_ratio"] != "1:1" {
		t.Fatalf("response_format.aspect_ratio = %v", format["aspect_ratio"])
	}
	if format["image_size"] != "1K" {
		t.Fatalf("response_format.image_size = %v", format["image_size"])
	}
}

func TestGeminiImageDataFallsBackToStepContent(t *testing.T) {
	raw := []byte(`{
		"steps": [
			{
				"type": "model_output",
				"content": [
					{"type": "text", "text": "done"},
					{"type": "image", "data": "ZmFsbGJhY2s="}
				]
			}
		]
	}`)
	if got := geminiImageData(raw); got != "ZmFsbGJhY2s=" {
		t.Fatalf("geminiImageData = %q", got)
	}
}
