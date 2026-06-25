package server

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// notionExportRequest is the body of POST /api/discussions/{id}/summary/notion.
type notionExportRequest struct {
	ParentPageID string `json:"parent_page_id"`
	DocType      string `json:"doc_type,omitempty"`
}

// notionExportResponse returns the URL of the newly-created Notion page so the
// client can offer an "Open in Notion" action.
type notionExportResponse struct {
	URL    string `json:"url"`
	PageID string `json:"page_id"`
}

// handleExportSummaryToNotion writes a discussion's generated summary into the
// requester's connected Notion workspace. When parent_page_id is present, the
// summary is created below that page; otherwise it is created as a private
// root-level workspace page. The summary Markdown (with the embedded "listen
// again" link) is converted to Notion blocks. Same visibility gate as the other
// summary endpoints.
func (s *Server) handleExportSummaryToNotion(w http.ResponseWriter, r *http.Request) {
	user := s.requestUser(r)
	id := r.PathValue("id")

	var req notionExportRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	parentPageID := strings.TrimSpace(req.ParentPageID)

	// Visibility gate: only export summaries of discussions the user can see.
	visible, err := s.d.Discussions.GetVisible(r.Context(), user.ID, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if visible == nil {
		http.NotFound(w, r)
		return
	}

	conn, ok := s.requireNotionConnection(w, r)
	if !ok {
		return
	}

	docType := normalizeDocType(req.DocType)
	doc, err := s.d.Discussions.SummaryDocumentFor(r.Context(), id, docType)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if doc == nil || strings.TrimSpace(doc.Markdown) == "" {
		http.NotFound(w, r)
		return
	}

	title := strings.TrimSpace(visible.Title)
	if title == "" {
		title = strings.TrimSpace(visible.Topic)
	}
	if title == "" {
		title = "Podcast summary"
	}

	markdown := s.summaryMarkdownWithLink(id, doc.Markdown)
	blocks := markdownToNotionBlocks(markdown)

	pageURL, pageID, err := s.createNotionPage(r.Context(), conn.AccessToken, parentPageID, title, blocks)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, notionExportResponse{URL: pageURL, PageID: pageID})
}

// notionMaxChildrenPerRequest is Notion's cap on the number of child blocks
// accepted in a single page-create / append-children request.
const notionMaxChildrenPerRequest = 100

// createNotionPage creates a page under parentPageID, or at the workspace root
// when parentPageID is empty, with the given title and block children. It returns
// the new page's URL and id. Children beyond Notion's per-request cap are
// appended in follow-up batches.
func (s *Server) createNotionPage(ctx context.Context, token, parentPageID, title string, blocks []map[string]any) (string, string, error) {
	first := blocks
	var rest [][]map[string]any
	if len(blocks) > notionMaxChildrenPerRequest {
		first = blocks[:notionMaxChildrenPerRequest]
		for i := notionMaxChildrenPerRequest; i < len(blocks); i += notionMaxChildrenPerRequest {
			end := i + notionMaxChildrenPerRequest
			if end > len(blocks) {
				end = len(blocks)
			}
			rest = append(rest, blocks[i:end])
		}
	}

	body := map[string]any{
		"parent": notionPageParent(parentPageID),
		"properties": map[string]any{
			"title": map[string]any{
				"title": []map[string]any{
					{"type": "text", "text": map[string]any{"content": notionTruncate(title, 2000)}},
				},
			},
		},
		"children": first,
	}
	var created struct {
		ID  string `json:"id"`
		URL string `json:"url"`
	}
	if err := s.doNotionAPI(ctx, token, http.MethodPost, "/v1/pages", body, &created); err != nil {
		return "", "", err
	}

	for _, batch := range rest {
		appendBody := map[string]any{"children": batch}
		if err := s.doNotionAPI(ctx, token, http.MethodPatch,
			"/v1/blocks/"+url.PathEscape(created.ID)+"/children", appendBody, nil); err != nil {
			// The page exists with its first batch; surface the partial result.
			return created.URL, created.ID, err
		}
	}
	return created.URL, created.ID, nil
}

func notionPageParent(parentPageID string) map[string]any {
	parentPageID = strings.TrimSpace(parentPageID)
	if parentPageID == "" {
		return map[string]any{"type": "workspace", "workspace": true}
	}
	return map[string]any{"type": "page_id", "page_id": parentPageID}
}

var (
	notionNumberedRe = regexp.MustCompile(`^\d+\.\s+`)
	// notionInlineRe matches a [label](url) link or a **bold** span so they can
	// be rendered as Notion rich-text annotations rather than literal markdown.
	notionInlineRe = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)|\*\*([^*]+)\*\*`)
)

// markdownToNotionBlocks converts the subset of Markdown the summarizer emits
// (headings, paragraphs, bulleted/numbered lists, blockquotes, fenced code
// incl. ```mermaid, and dividers) into Notion block objects. It is the inverse
// of notionBlock.markdown(). A line-based parser is sufficient — no full
// CommonMark — because the summary body is machine-generated and regular.
func markdownToNotionBlocks(md string) []map[string]any {
	lines := strings.Split(md, "\n")
	blocks := make([]map[string]any, 0, len(lines))
	i := 0
	for i < len(lines) {
		trimmed := strings.TrimSpace(lines[i])

		// Fenced code block: collect until the closing fence.
		if strings.HasPrefix(trimmed, "```") {
			lang := strings.TrimSpace(strings.TrimPrefix(trimmed, "```"))
			i++
			var code []string
			for i < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[i]), "```") {
				code = append(code, lines[i])
				i++
			}
			if i < len(lines) {
				i++ // skip closing fence
			}
			blocks = append(blocks, notionCodeBlockObj(strings.Join(code, "\n"), lang))
			continue
		}

		if trimmed == "" {
			i++
			continue
		}
		if trimmed == "---" || trimmed == "***" || trimmed == "___" {
			blocks = append(blocks, map[string]any{"object": "block", "type": "divider", "divider": map[string]any{}})
			i++
			continue
		}

		switch {
		case strings.HasPrefix(trimmed, "### "):
			blocks = append(blocks, notionTextBlockObj("heading_3", strings.TrimPrefix(trimmed, "### ")))
		case strings.HasPrefix(trimmed, "## "):
			blocks = append(blocks, notionTextBlockObj("heading_2", strings.TrimPrefix(trimmed, "## ")))
		case strings.HasPrefix(trimmed, "# "):
			blocks = append(blocks, notionTextBlockObj("heading_1", strings.TrimPrefix(trimmed, "# ")))
		case strings.HasPrefix(trimmed, "> "):
			blocks = append(blocks, notionTextBlockObj("quote", strings.TrimPrefix(trimmed, "> ")))
		case strings.HasPrefix(trimmed, "- "), strings.HasPrefix(trimmed, "* "):
			blocks = append(blocks, notionTextBlockObj("bulleted_list_item", trimmed[2:]))
		case notionNumberedRe.MatchString(trimmed):
			blocks = append(blocks, notionTextBlockObj("numbered_list_item", notionNumberedRe.ReplaceAllString(trimmed, "")))
		default:
			blocks = append(blocks, notionTextBlockObj("paragraph", trimmed))
		}
		i++
	}

	// Notion rejects a page create with an empty children array.
	if len(blocks) == 0 {
		blocks = append(blocks, notionTextBlockObj("paragraph", ""))
	}
	return blocks
}

// notionTextBlockObj builds a rich-text-bearing block (heading, paragraph,
// list item, quote) with inline links/bold parsed from the Markdown text.
func notionTextBlockObj(blockType, text string) map[string]any {
	return map[string]any{
		"object":  "block",
		"type":    blockType,
		blockType: map[string]any{"rich_text": notionRichText(strings.TrimSpace(text))},
	}
}

// notionCodeBlockObj builds a code block. Code content is emitted verbatim (no
// inline markdown parsing); the language is mapped to a value Notion accepts.
func notionCodeBlockObj(code, lang string) map[string]any {
	return map[string]any{
		"object": "block",
		"type":   "code",
		"code": map[string]any{
			"rich_text": notionPlainRichText(code),
			"language":  notionCodeLanguage(lang),
		},
	}
}

// notionRichText parses a subset of inline Markdown ([label](url) links and
// **bold**) into Notion rich-text objects, chunking each run to Notion's
// 2000-character-per-text-object limit.
func notionRichText(text string) []map[string]any {
	out := []map[string]any{}
	idx := 0
	for _, m := range notionInlineRe.FindAllStringSubmatchIndex(text, -1) {
		if m[0] > idx {
			out = appendNotionRich(out, text[idx:m[0]], false, "")
		}
		switch {
		case m[2] >= 0: // [label](url)
			out = appendNotionRich(out, text[m[2]:m[3]], false, text[m[4]:m[5]])
		case m[6] >= 0: // **bold**
			out = appendNotionRich(out, text[m[6]:m[7]], true, "")
		}
		idx = m[1]
	}
	if idx < len(text) {
		out = appendNotionRich(out, text[idx:], false, "")
	}
	if len(out) == 0 {
		out = appendNotionRich(out, "", false, "")
	}
	return out
}

// notionPlainRichText emits text as rich-text without inline parsing (for code).
func notionPlainRichText(text string) []map[string]any {
	out := appendNotionRich([]map[string]any{}, text, false, "")
	if len(out) == 0 {
		out = appendNotionRich(out, "", false, "")
	}
	return out
}

// appendNotionRich appends one or more rich-text objects for content, splitting
// on the 2000-char limit and applying an optional link/bold annotation.
func appendNotionRich(out []map[string]any, content string, bold bool, href string) []map[string]any {
	const limit = 2000
	runes := []rune(content)
	for len(runes) > 0 {
		n := len(runes)
		if n > limit {
			n = limit
		}
		chunk := string(runes[:n])
		runes = runes[n:]
		textPayload := map[string]any{"content": chunk}
		if strings.TrimSpace(href) != "" {
			textPayload["link"] = map[string]any{"url": strings.TrimSpace(href)}
		}
		obj := map[string]any{"type": "text", "text": textPayload}
		if bold {
			obj["annotations"] = map[string]any{"bold": true}
		}
		out = append(out, obj)
	}
	return out
}

// notionCodeLanguages is the small allow-list of languages Notion accepts that
// the summarizer is likely to emit; anything else falls back to "plain text".
var notionCodeLanguages = map[string]string{
	"mermaid":    "mermaid",
	"bash":       "bash",
	"shell":      "shell",
	"sh":         "shell",
	"json":       "json",
	"yaml":       "yaml",
	"javascript": "javascript",
	"js":         "javascript",
	"typescript": "typescript",
	"ts":         "typescript",
	"python":     "python",
	"py":         "python",
	"go":         "go",
	"sql":        "sql",
	"html":       "html",
	"css":        "css",
	"markdown":   "markdown",
}

func notionCodeLanguage(lang string) string {
	if v, ok := notionCodeLanguages[strings.ToLower(strings.TrimSpace(lang))]; ok {
		return v
	}
	return "plain text"
}

func notionTruncate(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max])
}
