package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/sirily11/debate-bot/internal/config"
)

// errCloudflareNotConfigured is returned when CLOUDFLARE_ACCOUNT_ID /
// CLOUDFLARE_API_TOKEN are unset, so the PDF endpoint can answer 503 rather
// than attempt a call that is guaranteed to fail.
var errCloudflareNotConfigured = errors.New("cloudflare browser rendering is not configured")

const cloudflarePDFTimeout = 60 * time.Second

// summaryPDFTemplateVersion is baked into the cache key so that changes to the
// HTML/CSS template (e.g. page margins) invalidate previously-rendered PDFs.
// Bump this whenever summaryPDFCSS / summaryPDFScript / summaryPDFHTML change in
// a way that should re-render cached documents.
const summaryPDFTemplateVersion = "2"

// summaryPDFFromMarkdown renders a summary's Markdown body to a PDF via
// Cloudflare Browser Rendering. The Markdown — including any ```mermaid fenced
// blocks — is rendered client-side in Cloudflare's headless Chromium using
// marked + mermaid, so the resulting PDF carries real diagrams instead of raw
// fences. Returns errCloudflareNotConfigured when credentials are missing.
func summaryPDFFromMarkdown(ctx context.Context, env *config.Env, title, markdown string) ([]byte, error) {
	if env == nil || strings.TrimSpace(env.CloudflareAccountID) == "" || strings.TrimSpace(env.CloudflareAPIToken) == "" {
		return nil, errCloudflareNotConfigured
	}

	page, err := summaryPDFHTML(title, markdown)
	if err != nil {
		return nil, err
	}

	payload, err := json.Marshal(map[string]any{
		"html": page,
		// Wait until our render script signals completion (it appends #pdf-ready
		// in a finally block, so a mermaid parse error still releases the wait).
		"waitForSelector": map[string]any{
			"selector": "#pdf-ready",
			"timeout":  30000,
		},
		"gotoOptions": map[string]any{
			"waitUntil": "networkidle0",
			"timeout":   30000,
		},
	})
	if err != nil {
		return nil, err
	}

	endpoint := fmt.Sprintf("https://api.cloudflare.com/client/v4/accounts/%s/browser-rendering/pdf",
		strings.TrimSpace(env.CloudflareAccountID))

	reqCtx, cancel := context.WithTimeout(ctx, cloudflarePDFTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(env.CloudflareAPIToken))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cloudflare pdf request: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("cloudflare pdf read: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("cloudflare pdf: status %d: %s", resp.StatusCode, truncateText(string(data), 500))
	}
	// Success streams the raw PDF; an error surfaces as a JSON envelope even with
	// a 200, so reject anything that isn't a PDF body.
	if ct := resp.Header.Get("Content-Type"); strings.Contains(ct, "application/json") {
		return nil, fmt.Errorf("cloudflare pdf: unexpected response: %s", truncateText(string(data), 500))
	}
	if len(data) == 0 {
		return nil, errors.New("cloudflare pdf: empty response")
	}
	return data, nil
}

// summaryPDFScript renders the embedded Markdown (MD) into #content with marked,
// promotes ```mermaid code blocks to mermaid diagrams (using the global `mermaid`
// loaded via a classic <script>), then appends #pdf-ready so the capture knows
// rendering is finished.
//
// Robustness: marked/mermaid are optional globals — if either CDN fails to load
// we still render what we can. A hard-timeout fallback guarantees #pdf-ready is
// appended even if mermaid.run() hangs, so the Cloudflare waitForSelector never
// times out (the failure mode that previously returned 422). The earlier version
// used a top-level ESM `import` whose failure aborted the whole module before the
// ready marker was ever added.
const summaryPDFScript = `
(function () {
  var done = false;
  function markReady() {
    if (done) return;
    done = true;
    var marker = document.createElement('div');
    marker.id = 'pdf-ready';
    document.body.appendChild(marker);
  }
  // Belt-and-suspenders: never let a hung/missing renderer block the capture.
  setTimeout(markReady, 10000);
  (async function () {
    try {
      document.getElementById('content').innerHTML =
        (window.marked && marked.parse) ? marked.parse(MD) : MD;
      var blocks = document.querySelectorAll('code.language-mermaid');
      blocks.forEach(function (code) {
        var pre = code.closest('pre') || code;
        var div = document.createElement('div');
        div.className = 'mermaid';
        div.textContent = code.textContent;
        pre.replaceWith(div);
      });
      if (window.mermaid && blocks.length) {
        mermaid.initialize({ startOnLoad: false, securityLevel: 'loose' });
        await mermaid.run({ querySelector: '.mermaid' });
      }
    } catch (e) {
      // Leave whatever rendered; still signal ready below.
    } finally {
      markReady();
    }
  })();
})();
`

// summaryPDFCSS is print-oriented typography for the rendered summary.
const summaryPDFCSS = `
  :root { color-scheme: light; }
  /* Page margins must come from @page, not body padding — body padding is
     applied once to the whole flow, leaving page 2+ flush against the top edge.
     @page margins repeat on every printed page. */
  @page { margin: 40px 48px; }
  body {
    font-family: -apple-system, "Segoe UI", Roboto, Helvetica, Arial, sans-serif;
    color: #1a1a1a;
    line-height: 1.6;
    margin: 0;
    padding: 0;
    font-size: 14px;
  }
  main { max-width: 720px; margin: 0 auto; }
  h1, h2, h3, h4 { line-height: 1.25; margin: 1.4em 0 0.6em; font-weight: 700; }
  h1 { font-size: 1.9em; } h2 { font-size: 1.5em; } h3 { font-size: 1.2em; }
  p { margin: 0.6em 0; }
  ul, ol { padding-left: 1.4em; }
  a { color: #2563eb; text-decoration: none; word-break: break-word; }
  blockquote { margin: 0.8em 0; padding: 0.2em 1em; border-left: 3px solid #d1d5db; color: #4b5563; }
  code { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 0.9em; background: #f3f4f6; padding: 0.1em 0.3em; border-radius: 4px; }
  pre { background: #f6f8fa; padding: 12px 14px; border-radius: 8px; overflow-x: auto; }
  pre code { background: none; padding: 0; }
  table { border-collapse: collapse; width: 100%; margin: 0.8em 0; }
  th, td { border: 1px solid #e5e7eb; padding: 6px 10px; text-align: left; }
  .mermaid { margin: 1em 0; text-align: center; }
  .mermaid svg { max-width: 100%; height: auto; }
`

// summaryPDFHTML builds the self-contained page Cloudflare renders. The Markdown
// is embedded as a JSON-encoded JS string — Go's json.Marshal escapes '<' to
// <, so the body can never prematurely close the <script> element.
func summaryPDFHTML(title, markdown string) (string, error) {
	mdJSON, err := json.Marshal(markdown)
	if err != nil {
		return "", err
	}
	safeTitle := html.EscapeString(strings.TrimSpace(title))
	if safeTitle == "" {
		safeTitle = "Summary"
	}

	var b strings.Builder
	b.WriteString(`<!doctype html><html lang="en"><head><meta charset="utf-8">`)
	b.WriteString(`<meta name="viewport" content="width=device-width, initial-scale=1">`)
	b.WriteString(`<title>` + safeTitle + `</title>`)
	b.WriteString(`<style>` + summaryPDFCSS + `</style>`)
	b.WriteString(`</head><body><main id="content"></main>`)
	// Classic <script src> bundles expose window.marked / window.mermaid and run
	// in document order, so the inline render script below sees them already
	// loaded (and degrades gracefully if a CDN fetch fails).
	b.WriteString(`<script src="https://cdn.jsdelivr.net/npm/marked@12/marked.min.js"></script>`)
	b.WriteString(`<script src="https://cdn.jsdelivr.net/npm/mermaid@11/dist/mermaid.min.js"></script>`)
	b.WriteString(`<script>`)
	b.WriteString(`const MD = ` + string(mdJSON) + `;`)
	b.WriteString(summaryPDFScript)
	b.WriteString(`</script></body></html>`)
	return b.String(), nil
}
