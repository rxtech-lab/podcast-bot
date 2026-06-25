package server

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const notionVersion = "2026-03-11"

type notionStatusResponse struct {
	Connected     bool   `json:"connected"`
	WorkspaceID   string `json:"workspace_id,omitempty"`
	WorkspaceName string `json:"workspace_name,omitempty"`
	WorkspaceIcon string `json:"workspace_icon,omitempty"`
}

type notionAuthURLResponse struct {
	AuthURL string `json:"auth_url"`
}

type notionPageDTO struct {
	ID             string `json:"id"`
	Title          string `json:"title"`
	URL            string `json:"url,omitempty"`
	LastEditedTime string `json:"last_edited_time,omitempty"`
}

type notionPageSearchRequest struct {
	Query    string `json:"query"`
	PageSize int    `json:"page_size,omitempty"`
}

type notionPageSearchResponse struct {
	Pages []notionPageDTO `json:"pages"`
}

type notionPageAttachmentRequest struct {
	PageID string `json:"page_id"`
}

type notionOAuthState struct {
	UserID string `json:"user_id"`
	Nonce  string `json:"nonce"`
	Exp    int64  `json:"exp"`
}

type notionTokenResponse struct {
	AccessToken   string `json:"access_token"`
	RefreshToken  string `json:"refresh_token"`
	BotID         string `json:"bot_id"`
	WorkspaceID   string `json:"workspace_id"`
	WorkspaceName string `json:"workspace_name"`
	WorkspaceIcon string `json:"workspace_icon"`
}

func (s *Server) handleNotionStatus(w http.ResponseWriter, r *http.Request) {
	conn, err := s.notionConnection(r.Context(), s.requestUser(r).ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if conn == nil {
		writeJSON(w, notionStatusResponse{Connected: false})
		return
	}
	writeJSON(w, notionStatusResponse{
		Connected:     true,
		WorkspaceID:   conn.WorkspaceID,
		WorkspaceName: conn.WorkspaceName,
		WorkspaceIcon: conn.WorkspaceIcon,
	})
}

func (s *Server) handleNotionAuthURL(w http.ResponseWriter, r *http.Request) {
	if !s.notionConfigured() {
		http.Error(w, "notion oauth is not configured", http.StatusServiceUnavailable)
		return
	}
	state, err := s.newNotionOAuthState(s.requestUser(r).ID)
	if err != nil {
		http.Error(w, "create notion oauth state", http.StatusInternalServerError)
		return
	}
	u, err := url.Parse(s.d.Env.NotionAPIBaseURL + "/v1/oauth/authorize")
	if err != nil {
		http.Error(w, "invalid notion api base url", http.StatusInternalServerError)
		return
	}
	q := u.Query()
	q.Set("owner", "user")
	q.Set("client_id", s.d.Env.NotionOAuthClientID)
	q.Set("redirect_uri", s.d.Env.NotionOAuthRedirectURI)
	q.Set("response_type", "code")
	q.Set("state", state)
	u.RawQuery = q.Encode()
	writeJSON(w, notionAuthURLResponse{AuthURL: u.String()})
}

func (s *Server) handleNotionOAuthCallback(w http.ResponseWriter, r *http.Request) {
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	state := strings.TrimSpace(r.URL.Query().Get("state"))
	oauthErr := strings.TrimSpace(r.URL.Query().Get("error"))
	fmt.Printf("notion oauth callback code=%q state=%q error=%q\n", code, state, oauthErr)

	callback := s.notionAppCallbackURL()
	if oauthErr != "" {
		redirectWithQuery(w, r, callback, map[string]string{"error": oauthErr})
		return
	}
	if code == "" || state == "" {
		http.Error(w, "missing notion code or state", http.StatusBadRequest)
		return
	}
	parsed, err := s.parseNotionOAuthState(state)
	if err != nil {
		http.Error(w, "invalid notion oauth state", http.StatusBadRequest)
		return
	}
	token, err := s.exchangeNotionCode(r.Context(), code)
	if err != nil {
		s.logger().Warn("notion oauth exchange failed", "err", err)
		redirectWithQuery(w, r, callback, map[string]string{"error": "exchange_failed"})
		return
	}
	if err := s.d.Discussions.SaveNotionConnection(r.Context(), NotionConnection{
		UserID:        parsed.UserID,
		AccessToken:   token.AccessToken,
		RefreshToken:  token.RefreshToken,
		BotID:         token.BotID,
		WorkspaceID:   token.WorkspaceID,
		WorkspaceName: token.WorkspaceName,
		WorkspaceIcon: token.WorkspaceIcon,
	}); err != nil {
		s.logger().Warn("notion oauth save failed", "err", err)
		redirectWithQuery(w, r, callback, map[string]string{"error": "save_failed"})
		return
	}
	redirectWithQuery(w, r, callback, map[string]string{"connected": "1"})
}

func (s *Server) handleNotionSearchPages(w http.ResponseWriter, r *http.Request) {
	conn, ok := s.requireNotionConnection(w, r)
	if !ok {
		return
	}
	var req notionPageSearchRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	pages, err := s.searchNotionPages(r.Context(), conn.AccessToken, req.Query, req.PageSize)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, notionPageSearchResponse{Pages: pages})
}

func (s *Server) handleNotionPageAttachment(w http.ResponseWriter, r *http.Request) {
	conn, ok := s.requireNotionConnection(w, r)
	if !ok {
		return
	}
	var req notionPageAttachmentRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	pageID := strings.TrimSpace(req.PageID)
	if pageID == "" {
		http.Error(w, "page_id is required", http.StatusBadRequest)
		return
	}
	page, markdown, err := s.fetchNotionPageMarkdown(r.Context(), conn.AccessToken, pageID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	title := page.Title
	if title == "" {
		title = "Notion page"
	}
	if strings.TrimSpace(markdown) == "" {
		markdown = "# " + title + "\n\n"
	}
	writeJSON(w, uploadResponse{
		Filename: title + ".md",
		Markdown: markdown,
		URL:      page.URL,
		MIMEType: "text/markdown+notion",
	})
}

func (s *Server) requireNotionConnection(w http.ResponseWriter, r *http.Request) (*NotionConnection, bool) {
	conn, err := s.notionConnection(r.Context(), s.requestUser(r).ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return nil, false
	}
	if conn == nil {
		http.Error(w, "notion is not connected", http.StatusUnauthorized)
		return nil, false
	}
	return conn, true
}

func (s *Server) notionConnection(ctx context.Context, userID string) (*NotionConnection, error) {
	if s.d.Discussions == nil {
		return nil, errors.New("discussion store is not configured")
	}
	return s.d.Discussions.NotionConnection(ctx, userID)
}

func (s *Server) notionConfigured() bool {
	return s.d.Env != nil &&
		strings.TrimSpace(s.d.Env.NotionOAuthClientID) != "" &&
		strings.TrimSpace(s.d.Env.NotionOAuthClientSecret) != "" &&
		strings.TrimSpace(s.d.Env.NotionOAuthRedirectURI) != ""
}

func (s *Server) notionAppCallbackURL() string {
	if s.d.Env != nil && strings.TrimSpace(s.d.Env.NotionAppCallbackURL) != "" {
		return s.d.Env.NotionAppCallbackURL
	}
	return "debatepod://notion-callback"
}

func (s *Server) newNotionOAuthState(userID string) (string, error) {
	var nonceBytes [16]byte
	if _, err := rand.Read(nonceBytes[:]); err != nil {
		return "", err
	}
	payload := notionOAuthState{
		UserID: userID,
		Nonce:  hex.EncodeToString(nonceBytes[:]),
		Exp:    time.Now().Add(10 * time.Minute).Unix(),
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	body := base64.RawURLEncoding.EncodeToString(b)
	sig := s.signNotionOAuthState(body)
	return body + "." + sig, nil
}

func (s *Server) parseNotionOAuthState(state string) (notionOAuthState, error) {
	body, sig, ok := strings.Cut(state, ".")
	if !ok || body == "" || sig == "" {
		return notionOAuthState{}, errors.New("malformed state")
	}
	if !hmac.Equal([]byte(sig), []byte(s.signNotionOAuthState(body))) {
		return notionOAuthState{}, errors.New("state signature mismatch")
	}
	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return notionOAuthState{}, err
	}
	var payload notionOAuthState
	if err := json.Unmarshal(raw, &payload); err != nil {
		return notionOAuthState{}, err
	}
	if strings.TrimSpace(payload.UserID) == "" || payload.Exp < time.Now().Unix() {
		return notionOAuthState{}, errors.New("state expired")
	}
	return payload, nil
}

func (s *Server) signNotionOAuthState(body string) string {
	secret := ""
	if s.d.Env != nil {
		secret = s.d.Env.NotionOAuthClientSecret
		if secret == "" {
			secret = s.d.Env.OpenAIKey
		}
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *Server) exchangeNotionCode(ctx context.Context, code string) (notionTokenResponse, error) {
	body := map[string]string{
		"grant_type":   "authorization_code",
		"code":         code,
		"redirect_uri": s.d.Env.NotionOAuthRedirectURI,
	}
	payload, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.d.Env.NotionAPIBaseURL+"/v1/oauth/token", bytes.NewReader(payload))
	if err != nil {
		return notionTokenResponse{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(s.d.Env.NotionOAuthClientID+":"+s.d.Env.NotionOAuthClientSecret)))
	var out notionTokenResponse
	if err := doNotionJSON(req, &out); err != nil {
		return out, err
	}
	if strings.TrimSpace(out.AccessToken) == "" {
		return out, errors.New("notion token response missing access_token")
	}
	return out, nil
}

func (s *Server) searchNotionPages(ctx context.Context, token, query string, pageSize int) ([]notionPageDTO, error) {
	if pageSize <= 0 || pageSize > 50 {
		pageSize = 25
	}
	body := map[string]any{
		"query":     strings.TrimSpace(query),
		"page_size": pageSize,
		"filter": map[string]string{
			"property": "object",
			"value":    "page",
		},
		"sort": map[string]string{
			"direction": "descending",
			"timestamp": "last_edited_time",
		},
	}
	var resp struct {
		Results []json.RawMessage `json:"results"`
	}
	if err := s.doNotionAPI(ctx, token, http.MethodPost, "/v1/search", body, &resp); err != nil {
		return nil, err
	}
	pages := make([]notionPageDTO, 0, len(resp.Results))
	for _, raw := range resp.Results {
		var p notionRawPage
		if err := json.Unmarshal(raw, &p); err != nil || p.Object != "page" {
			continue
		}
		pages = append(pages, notionPageDTO{
			ID:             p.ID,
			Title:          p.title(),
			URL:            p.URL,
			LastEditedTime: p.LastEditedTime,
		})
	}
	return pages, nil
}

func (s *Server) fetchNotionPageMarkdown(ctx context.Context, token, pageID string) (notionPageDTO, string, error) {
	var rawPage notionRawPage
	if err := s.doNotionAPI(ctx, token, http.MethodGet, "/v1/pages/"+url.PathEscape(pageID), nil, &rawPage); err != nil {
		return notionPageDTO{}, "", err
	}
	page := notionPageDTO{ID: rawPage.ID, Title: rawPage.title(), URL: rawPage.URL, LastEditedTime: rawPage.LastEditedTime}
	if page.Title == "" {
		page.Title = "Notion page"
	}
	blocks, err := s.notionBlockMarkdown(ctx, token, pageID, 0)
	if err != nil {
		return page, "", err
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "# %s\n\nSource: %s\n\n", page.Title, page.URL)
	sb.WriteString(strings.TrimSpace(blocks))
	if !strings.HasSuffix(sb.String(), "\n") {
		sb.WriteString("\n")
	}
	return page, sb.String(), nil
}

func (s *Server) notionBlockMarkdown(ctx context.Context, token, blockID string, depth int) (string, error) {
	if depth > 3 {
		return "", nil
	}
	var cursor string
	var sb strings.Builder
	for {
		endpoint := "/v1/blocks/" + url.PathEscape(blockID) + "/children?page_size=100"
		if cursor != "" {
			endpoint += "&start_cursor=" + url.QueryEscape(cursor)
		}
		var resp struct {
			Results    []notionBlock `json:"results"`
			NextCursor string        `json:"next_cursor"`
			HasMore    bool          `json:"has_more"`
		}
		if err := s.doNotionAPI(ctx, token, http.MethodGet, endpoint, nil, &resp); err != nil {
			return "", err
		}
		for _, block := range resp.Results {
			text := block.markdown()
			if text != "" {
				sb.WriteString(text)
				if !strings.HasSuffix(text, "\n") {
					sb.WriteString("\n")
				}
			}
			if block.HasChildren {
				child, err := s.notionBlockMarkdown(ctx, token, block.ID, depth+1)
				if err != nil {
					return "", err
				}
				if strings.TrimSpace(child) != "" {
					sb.WriteString(child)
					if !strings.HasSuffix(child, "\n") {
						sb.WriteString("\n")
					}
				}
			}
			if sb.Len() > 60000 {
				sb.WriteString("\n[Notion page truncated]\n")
				return sb.String(), nil
			}
		}
		if !resp.HasMore || resp.NextCursor == "" {
			break
		}
		cursor = resp.NextCursor
	}
	return sb.String(), nil
}

func (s *Server) doNotionAPI(ctx context.Context, token, method, endpoint string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(payload)
	}
	base := s.d.Env.NotionAPIBaseURL
	u := base + endpoint
	if strings.HasPrefix(endpoint, "http://") || strings.HasPrefix(endpoint, "https://") {
		u = endpoint
	}
	req, err := http.NewRequestWithContext(ctx, method, u, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Notion-Version", notionVersion)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return doNotionJSON(req, out)
}

func doNotionJSON(req *http.Request, out any) error {
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("notion request failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}

func redirectWithQuery(w http.ResponseWriter, r *http.Request, raw string, vals map[string]string) {
	u, err := url.Parse(raw)
	if err != nil {
		http.Error(w, "invalid callback url", http.StatusInternalServerError)
		return
	}
	q := u.Query()
	for k, v := range vals {
		q.Set(k, v)
	}
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

type notionRawPage struct {
	Object         string                    `json:"object"`
	ID             string                    `json:"id"`
	URL            string                    `json:"url"`
	LastEditedTime string                    `json:"last_edited_time"`
	Properties     map[string]notionProperty `json:"properties"`
}

type notionProperty struct {
	Type     string       `json:"type"`
	Title    []notionText `json:"title"`
	RichText []notionText `json:"rich_text"`
}

type notionText struct {
	PlainText string `json:"plain_text"`
	Href      string `json:"href"`
}

func (p notionRawPage) title() string {
	for _, prop := range p.Properties {
		if prop.Type == "title" {
			if title := notionTextsPlain(prop.Title); title != "" {
				return title
			}
		}
	}
	for _, prop := range p.Properties {
		if title := notionTextsPlain(prop.Title); title != "" {
			return title
		}
	}
	return "Untitled"
}

type notionBlock struct {
	Object      string          `json:"object"`
	ID          string          `json:"id"`
	Type        string          `json:"type"`
	HasChildren bool            `json:"has_children"`
	Paragraph   notionTextBlock `json:"paragraph"`
	Heading1    notionTextBlock `json:"heading_1"`
	Heading2    notionTextBlock `json:"heading_2"`
	Heading3    notionTextBlock `json:"heading_3"`
	Bulleted    notionTextBlock `json:"bulleted_list_item"`
	Numbered    notionTextBlock `json:"numbered_list_item"`
	Quote       notionTextBlock `json:"quote"`
	Callout     notionTextBlock `json:"callout"`
	ToDo        notionToDoBlock `json:"to_do"`
	Code        notionCodeBlock `json:"code"`
	ChildPage   struct {
		Title string `json:"title"`
	} `json:"child_page"`
	Bookmark struct {
		URL string `json:"url"`
	} `json:"bookmark"`
}

type notionTextBlock struct {
	RichText []notionText `json:"rich_text"`
}

type notionToDoBlock struct {
	RichText []notionText `json:"rich_text"`
	Checked  bool         `json:"checked"`
}

type notionCodeBlock struct {
	RichText []notionText `json:"rich_text"`
	Language string       `json:"language"`
}

func (b notionBlock) markdown() string {
	switch b.Type {
	case "paragraph":
		return notionTextsPlain(b.Paragraph.RichText)
	case "heading_1":
		return "# " + notionTextsPlain(b.Heading1.RichText)
	case "heading_2":
		return "## " + notionTextsPlain(b.Heading2.RichText)
	case "heading_3":
		return "### " + notionTextsPlain(b.Heading3.RichText)
	case "bulleted_list_item":
		return "- " + notionTextsPlain(b.Bulleted.RichText)
	case "numbered_list_item":
		return "1. " + notionTextsPlain(b.Numbered.RichText)
	case "quote":
		return "> " + notionTextsPlain(b.Quote.RichText)
	case "callout":
		return "> " + notionTextsPlain(b.Callout.RichText)
	case "to_do":
		mark := " "
		if b.ToDo.Checked {
			mark = "x"
		}
		return "- [" + mark + "] " + notionTextsPlain(b.ToDo.RichText)
	case "code":
		lang := strings.TrimSpace(b.Code.Language)
		return "```" + lang + "\n" + notionTextsPlain(b.Code.RichText) + "\n```"
	case "child_page":
		return "## " + strings.TrimSpace(b.ChildPage.Title)
	case "bookmark":
		if strings.TrimSpace(b.Bookmark.URL) != "" {
			return "<" + strings.TrimSpace(b.Bookmark.URL) + ">"
		}
	}
	return ""
}

func notionTextsPlain(texts []notionText) string {
	var sb strings.Builder
	for _, t := range texts {
		sb.WriteString(t.PlainText)
	}
	return strings.TrimSpace(sb.String())
}
