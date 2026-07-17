package server

import (
	"errors"
	"net/http"
	"strings"

	contentcreator "github.com/sirily11/debate-bot/internal/content_creator"
)

type agentDocumentListResponse struct {
	Documents []AgentDocument `json:"documents"`
	HasMore   bool            `json:"has_more,omitempty"`
}

func (s *Server) handleGlobalAgentDocuments(w http.ResponseWriter, r *http.Request) {
	user := s.requestUser(r)
	scope := strings.TrimSpace(r.URL.Query().Get("scope"))
	if scope == "all" {
		limit := atoiDefault(r.URL.Query().Get("limit"), 0)
		offset := atoiDefault(r.URL.Query().Get("offset"), 0)
		query := strings.TrimSpace(r.URL.Query().Get("q"))
		documents, hasMore, err := s.d.AgentDocuments.ListAllPage(r.Context(), user.ID, query, limit, offset)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, agentDocumentListResponse{Documents: documents, HasMore: hasMore})
		return
	}
	if scope != "" {
		http.Error(w, "invalid document scope", http.StatusBadRequest)
		return
	}
	documents, err := s.d.AgentDocuments.List(r.Context(), user.ID, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, agentDocumentListResponse{Documents: documents})
}

func (s *Server) handleAgentDocumentDelete(w http.ResponseWriter, r *http.Request) {
	ok, err := s.d.AgentDocuments.Delete(r.Context(), s.requestUser(r).ID,
		strings.TrimSpace(r.PathValue("id")))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDiscussionAgentDocuments(w http.ResponseWriter, r *http.Request) {
	user := s.requestUser(r)
	id := strings.TrimSpace(r.PathValue("id"))
	d, err := s.d.Discussions.Get(r.Context(), user.ID, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if d == nil {
		http.NotFound(w, r)
		return
	}
	documents, err := s.d.AgentDocuments.List(r.Context(), user.ID, &id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, agentDocumentListResponse{Documents: documents})
}

func (s *Server) handleAgentDocumentGet(w http.ResponseWriter, r *http.Request) {
	doc, ok := s.agentDocumentForRequest(w, r)
	if !ok {
		return
	}
	doc.Markdown = s.agentDocumentMarkdown(doc)
	writeJSON(w, doc)
}

func (s *Server) handleAgentDocumentUIActions(w http.ResponseWriter, r *http.Request) {
	doc, ok := s.agentDocumentForRequest(w, r)
	if !ok {
		return
	}
	lang := contentcreator.LangFromAcceptLanguage(r.Header.Get("Accept-Language"))
	items := []discussionUIActionItem{
		actionItem("download-pdf", phrase(lang, "Download PDF", "下载 PDF", "下載 PDF"),
			phrase(lang, "Preparing PDF", "正在准备 PDF", "正在準備 PDF"), "arrow.down.doc", "", true,
			"download", documentActionLink(doc.ID, "export", "pdf")),
		actionItem("download-markdown", phrase(lang, "Download Markdown", "下载 Markdown", "下載 Markdown"),
			"", "arrow.down.doc.fill", "", true,
			"download", documentActionLink(doc.ID, "export", "markdown")),
		actionItem("export-notion", phrase(lang, "Export to Notion", "导出到 Notion", "匯出到 Notion"),
			phrase(lang, "Exporting", "正在导出", "正在匯出"), "square.and.arrow.up.on.square", "", true,
			"open-sheet", documentActionLink(doc.ID, "sheet", "notion")),
	}
	writeJSON(w, discussionUIActionsResponse{
		ID:    doc.ID,
		Items: s.applyEntitlementsForUser(r, items),
	})
}

func (s *Server) handleAgentDocumentPDF(w http.ResponseWriter, r *http.Request) {
	doc, ok := s.agentDocumentForRequest(w, r)
	if !ok {
		return
	}
	pdf, err := summaryPDFFromMarkdown(r.Context(), s.d.Env, doc.Title, s.agentDocumentMarkdown(doc))
	if errors.Is(err, errCloudflareNotConfigured) {
		http.Error(w, "document PDF export is not configured", http.StatusServiceUnavailable)
		return
	}
	if err != nil {
		s.logger().Error("agent document pdf render failed", "document", doc.ID, "err", err)
		http.Error(w, "failed to render document PDF", http.StatusBadGateway)
		return
	}
	s.writeSummaryPDF(w, doc.Title, pdf)
}

func (s *Server) handleAgentDocumentNotion(w http.ResponseWriter, r *http.Request) {
	doc, ok := s.agentDocumentForRequest(w, r)
	if !ok {
		return
	}
	var req notionExportRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	conn, ok := s.requireNotionConnection(w, r)
	if !ok {
		return
	}
	pageURL, pageID, err := s.createNotionPage(r.Context(), conn.AccessToken,
		strings.TrimSpace(req.ParentPageID), doc.Title,
		markdownToNotionBlocks(s.agentDocumentMarkdown(doc)))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, notionExportResponse{URL: pageURL, PageID: pageID})
}

func (s *Server) agentDocumentForRequest(w http.ResponseWriter, r *http.Request) (*AgentDocument, bool) {
	doc, err := s.d.AgentDocuments.Get(r.Context(), s.requestUser(r).ID,
		strings.TrimSpace(r.PathValue("id")))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return nil, false
	}
	if doc == nil {
		http.NotFound(w, r)
		return nil, false
	}
	return doc, true
}

func (s *Server) agentDocumentMarkdown(doc *AgentDocument) string {
	if doc == nil || doc.DiscussionID == nil {
		if doc == nil {
			return ""
		}
		return doc.Markdown
	}
	return s.summaryMarkdownWithLink(*doc.DiscussionID, doc.Markdown)
}
