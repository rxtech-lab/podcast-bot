package server

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sirily11/debate-bot/internal/content_creator"
)

type discussionUIActionsResponse struct {
	ID    string                   `json:"id"`
	Items []discussionUIActionItem `json:"items"`
}

type discussionUIActionItem struct {
	ID           string             `json:"id"`
	Title        string             `json:"title"`
	LoadingTitle string             `json:"loading_title,omitempty"`
	SystemImage  string             `json:"system_image,omitempty"`
	Role         string             `json:"role,omitempty"`
	Enabled      bool               `json:"enabled"`
	Action       discussionUIAction `json:"action"`
}

type discussionUIAction struct {
	Type string `json:"type"`
	Link string `json:"link"`
}

func (s *Server) handleDiscussionUIActions(w http.ResponseWriter, r *http.Request) {
	user := s.requestUser(r)
	id := r.PathValue("id")
	d, err := s.d.Discussions.GetVisible(r.Context(), user.ID, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if d == nil {
		http.NotFound(w, r)
		return
	}
	s.applyDiscussionJobStatus(r, d, true)
	s.applyDiscussionProgress(r.Context(), d)
	s.applyDiscussionSummaryMeta(r.Context(), d)
	s.applyDiscussionShareURL(d)

	lang := contentcreator.LangFromAcceptLanguage(r.Header.Get("Accept-Language"))
	surface := strings.TrimSpace(r.URL.Query().Get("surface"))
	resp := discussionUIActionsResponse{ID: surface}
	switch surface {
	case "podcast-documents":
		resp.Items = s.podcastDocumentActions(r, d, lang)
	case "podcast-actions":
		resp.Items = s.podcastMenuActions(r, d, lang)
	case "summary-actions":
		resp.Items = s.summaryMenuActions(r, d, lang)
	default:
		http.Error(w, "invalid surface", http.StatusBadRequest)
		return
	}
	writeJSON(w, resp)
}

func (s *Server) podcastDocumentActions(r *http.Request, d *Discussion, lang contentcreator.Lang) []discussionUIActionItem {
	items := []discussionUIActionItem{
		actionItem("open-plan", phrase(lang, "Plan", "计划", "計劃"), "", "doc.text", "", true, "open-sheet", discussionActionLink(d.ID, "sheet", "plan")),
	}
	// Audiobook video belongs with the generated documents, next to Plan/Text,
	// rather than in the generic podcast actions menu.
	if item, ok := s.audioBookVideoAction(r, d, lang); ok {
		items = append(items, item)
	}
	if discussionIsAudioBook(d) {
		// Audiobooks expose the "text-based content" book document instead of
		// the discussion summary. It is generated after the audio finishes.
		textMeta, _ := s.d.Discussions.SummaryMetaFor(r.Context(), d.ID, SummaryDocTypeText)
		switch {
		case textMeta != nil && textMeta.Available:
			items = append(items, actionItem("open-text", phrase(lang, "Text", "文字版", "文字版"), "", "book", "", true, "open-sheet", discussionActionLink(d.ID, "sheet", "text")))
		case textMeta != nil && textMeta.Pending:
			items = append(items, actionItem("text-pending", phrase(lang, "Generating text", "正在生成文字版", "正在產生文字版"), "", "hourglass", "", false, "none", discussionActionLink(d.ID, "text", "pending")))
		}
		return items
	}
	if d.Summary != nil && d.Summary.Available {
		items = append(items, actionItem("open-summary", phrase(lang, "Summary", "总结", "摘要"), "", "doc.richtext", "", true, "open-sheet", discussionActionLink(d.ID, "sheet", "summary")))
	} else if d.Summary != nil && d.Summary.Pending {
		items = append(items, actionItem("summary-pending", phrase(lang, "Generating summary", "正在生成总结", "正在產生摘要"), "", "hourglass", "", false, "none", discussionActionLink(d.ID, "summary", "pending")))
	} else if d.Summary != nil && d.Summary.Generation {
		items = append(items, actionItem("generate-summary", phrase(lang, "Generate summary", "生成总结", "產生摘要"), phrase(lang, "Generating summary", "正在生成总结", "正在產生摘要"), "sparkles", "", true, "request", discussionActionLink(d.ID, "action", "summary-generate")))
	} else {
		items = append(items, actionItem("summary-unavailable", phrase(lang, "Summary", "总结", "摘要"), "", "doc.richtext", "", false, "none", discussionActionLink(d.ID, "summary", "unavailable")))
	}
	return items
}

func (s *Server) audioBookVideoAction(r *http.Request, d *Discussion, lang contentcreator.Lang) (discussionUIActionItem, bool) {
	if !discussionIsAudioBook(d) || s.d.Uploader == nil || !s.d.Uploader.Enabled() {
		return discussionUIActionItem{}, false
	}
	videoKey := s.discussionVideoKey(r, d)
	if strings.TrimSpace(videoKey) != "" {
		if url, err := s.d.Uploader.DownloadURL(r.Context(), videoKey, time.Hour); err == nil && strings.TrimSpace(url) != "" {
			return actionItem("view-video", phrase(lang, "View Video", "查看视频", "查看影片"), "", "film", "", true, "play-video", url), true
		}
	}
	if s.audioBookVideoRendering(d) {
		return actionItem("video-rendering", phrase(lang, "Generating Video", "正在生成视频", "正在產生影片"), "", "hourglass", "", false, "none", discussionActionLink(d.ID, "video", "rendering")), true
	}
	if d.IsOwner && d.Status == DiscussionReady && strings.TrimSpace(d.JobID) != "" && s.d.Jobs != nil && s.d.UploadRoot != "" {
		return actionItem("generate-video", phrase(lang, "Generate Video", "生成视频", "產生影片"), phrase(lang, "Generating Video", "正在生成视频", "正在產生影片"), "film.badge.plus", "", true, "request", discussionActionLink(d.ID, "action", "video-generate")), true
	}
	return discussionUIActionItem{}, false
}

func (s *Server) discussionVideoKey(r *http.Request, d *Discussion) string {
	videoKey, _ := s.d.Discussions.VideoKeyFor(r.Context(), d.ID)
	if strings.TrimSpace(videoKey) != "" || strings.TrimSpace(d.JobID) == "" || s.d.Uploader == nil || !s.d.Uploader.Enabled() {
		return videoKey
	}
	repairKey := s.d.Uploader.Key(d.JobID + "-video.mp4")
	if info, err := s.d.Uploader.Head(r.Context(), repairKey); err == nil && info.ContentLength > 0 {
		if err := s.d.Discussions.SetVideoKey(r.Context(), d.ID, repairKey); err != nil {
			s.logger().Warn("discussion video key repair failed", "discussion", d.ID, "job", d.JobID, "key", repairKey, "err", err)
		} else {
			s.logger().Info("discussion video key repaired from storage", "discussion", d.ID, "job", d.JobID, "key", repairKey)
			videoKey = repairKey
		}
	}
	return videoKey
}

func (s *Server) audioBookVideoRendering(d *Discussion) bool {
	if s.d.Jobs == nil || strings.TrimSpace(d.JobID) == "" {
		return false
	}
	j := s.d.Jobs.Get(d.JobID)
	if j == nil {
		return false
	}
	switch strings.TrimSpace(j.Phase) {
	case "video-queued", "video-rendering", "video-uploading":
		return !j.HasVideo
	default:
		return false
	}
}

func (s *Server) podcastMenuActions(r *http.Request, d *Discussion, lang contentcreator.Lang) []discussionUIActionItem {
	items := make([]discussionUIActionItem, 0, 8)
	if queryBool(r, "supports_points") {
		items = append(items, actionItem("points", phrase(lang, "Points", "点数", "點數"), "", "sparkles", "", true, "open-sheet", discussionActionLink(d.ID, "sheet", "points")))
	}
	pendingChapters := queryBool(r, "supports_chapter_batches") && d.IsOwner && discussionIsAudioBook(d) && d.Status == DiscussionReady && s.hasPendingChapters(r, d)
	if pendingChapters {
		items = append(items, actionItem("generate-more-chapters", phrase(lang, "Generate More Chapters", "继续生成章节", "繼續產生章節"), "", "text.badge.plus", "", true, "open-sheet", discussionActionLink(d.ID, "sheet", "generate-chapters")))
	}
	// While an audiobook still has ungenerated chapters, continuing the book
	// is the "Generate More Chapters" flow — offering a follow-up alongside it
	// would fork the story before it's finished.
	if queryBool(r, "supports_follow_up") && !pendingChapters {
		items = append(items, actionItem("create-follow-up", phrase(lang, "Create Follow-up", "创建后续节目", "建立後續節目"), "", "arrow.triangle.branch", "", true, "open-sheet", discussionActionLink(d.ID, "sheet", "follow-up")))
	}
	if queryBool(r, "supports_albums") {
		if strings.TrimSpace(d.AlbumID) != "" {
			items = append(items, actionItem("view-album", phrase(lang, "View Album", "查看专辑", "查看專輯"), "", "rectangle.stack", "", true, "open-sheet", discussionActionLink(d.ID, "sheet", "album")))
		} else if d.IsOwner {
			items = append(items, actionItem("add-to-album", phrase(lang, "Add to Album", "加入专辑", "加入專輯"), "", "plus.rectangle.on.folder", "", true, "open-sheet", discussionActionLink(d.ID, "sheet", "add-to-album")))
		}
	}
	if queryBool(r, "supports_create_from_plan") {
		items = append(items, actionItem("create-from-plan", phrase(lang, "Create from Plan", "从计划创建", "從計劃建立"), phrase(lang, "Creating", "正在创建", "正在建立"), "plus.circle", "", true, "request", discussionActionLink(d.ID, "action", "create-from-plan")))
	}
	if d.IsOwner {
		items = append(items, actionItem("edit-cover", phrase(lang, "Edit Cover", "编辑封面", "編輯封面"), "", "photo.badge.plus", "", true, "open-sheet", discussionActionLink(d.ID, "sheet", "cover")))
		if d.Visibility == DiscussionPublic {
			items = append(items, actionItem("make-private", phrase(lang, "Make Private", "设为私密", "設為私密"), "", "lock", "destructive", true, "request", discussionActionLink(d.ID, "action", "make-private")))
		} else {
			items = append(items, actionItem("publish", phrase(lang, "Publish to Market", "发布到市场", "發佈到市場"), "", "globe", "", true, "open-sheet", discussionActionLink(d.ID, "sheet", "publish")))
		}
	}
	if d.Visibility == DiscussionPublic && strings.TrimSpace(d.ShareURL) != "" {
		items = append(items, actionItem("share-public", phrase(lang, "Share", "分享", "分享"), "", "square.and.arrow.up", "", true, "share-link", d.ShareURL))
	} else if d.IsOwner {
		items = append(items, actionItem("share-private", phrase(lang, "Share Link", "分享链接", "分享連結"), "", "square.and.arrow.up", "", true, "open-sheet", discussionActionLink(d.ID, "sheet", "share")))
	}
	if d.Status == DiscussionReady && (strings.TrimSpace(d.DownloadURL) != "" || strings.TrimSpace(d.JobID) != "") {
		items = append(items, actionItem("download-podcast", phrase(lang, "Download Station", "下载电台", "下載電台"), phrase(lang, "Downloading", "正在下载", "正在下載"), "arrow.down.circle", "", true, "download", discussionActionLink(d.ID, "action", "download-podcast")))
	} else if d.IsOwner && d.Status == DiscussionGenerating && strings.TrimSpace(d.JobID) != "" {
		items = append(items, actionItem("force-stop", phrase(lang, "Force Stop", "强制停止", "強制停止"), phrase(lang, "Finalising", "正在完成", "正在完成"), "stop.fill", "destructive", true, "request", discussionActionLink(d.ID, "action", "force-stop")))
	}
	if queryBool(r, "supports_sign_out") {
		items = append(items, actionItem("sign-out", phrase(lang, "Sign Out", "退出登录", "登出"), "", "rectangle.portrait.and.arrow.right", "destructive", true, "request", discussionActionLink(d.ID, "action", "sign-out")))
	}
	return items
}

func (s *Server) summaryMenuActions(r *http.Request, d *Discussion, lang contentcreator.Lang) []discussionUIActionItem {
	docType := strings.TrimSpace(r.URL.Query().Get("doc_type"))
	if docType == "" {
		docType = SummaryDocTypeSummary
	}
	items := []discussionUIActionItem{
		actionItem("summary-document", phrase(lang, "Summary document", "总结文档", "摘要文件"), "", "doc.richtext", "", true, "select", discussionActionLink(d.ID, "summary", "select", SummaryDocTypeSummary)),
	}
	pptReady := false
	if doc, err := s.d.Discussions.SummaryDocumentFor(r.Context(), d.ID, SummaryDocTypePPT); err == nil && doc != nil && doc.Status == SummaryReadyState {
		pptReady = strings.TrimSpace(doc.Markdown) != ""
	}
	if pptReady || docType == SummaryDocTypePPT {
		items = append(items, actionItem("ppt-document", "PPTX", "", "rectangle.on.rectangle", "", pptReady, "select", discussionActionLink(d.ID, "summary", "select", SummaryDocTypePPT)))
	}

	canExportSummary := d.Summary != nil && d.Summary.Available
	canExportSlides := canExportSummary || (docType == SummaryDocTypePPT && pptReady)
	pptTitle := phrase(lang, "Generate PPTX", "生成 PPTX", "產生 PPTX")
	pptPDFTitle := phrase(lang, "Generate slides PDF", "生成幻灯片 PDF", "產生投影片 PDF")
	if pptReady {
		pptTitle = phrase(lang, "Download PPTX", "下载 PPTX", "下載 PPTX")
		pptPDFTitle = phrase(lang, "Download slides PDF", "下载幻灯片 PDF", "下載投影片 PDF")
	}
	items = append(items,
		actionItem("download-pptx", pptTitle, phrase(lang, "Preparing PPTX", "正在准备 PPTX", "正在準備 PPTX"), "rectangle.on.rectangle", "", canExportSlides, "download", discussionActionLink(d.ID, "summary", "export", "pptx")),
		actionItem("download-slides-pdf", pptPDFTitle, phrase(lang, "Preparing slides PDF", "正在准备幻灯片 PDF", "正在準備投影片 PDF"), "rectangle.stack", "", canExportSlides, "download", discussionActionLink(d.ID, "summary", "export", "slides-pdf")),
		actionItem("download-pdf", phrase(lang, "Download PDF", "下载 PDF", "下載 PDF"), phrase(lang, "Preparing PDF", "正在准备 PDF", "正在準備 PDF"), "arrow.down.doc", "", canExportSummary && docType == SummaryDocTypeSummary, "download", discussionActionLink(d.ID, "summary", "export", "pdf")),
		actionItem("download-markdown", phrase(lang, "Download Markdown", "下载 Markdown", "下載 Markdown"), "", "arrow.down.doc.fill", "", canExportSummary && docType == SummaryDocTypeSummary, "download", discussionActionLink(d.ID, "summary", "export", "markdown")),
		actionItem("export-notion", phrase(lang, "Export to Notion", "导出到 Notion", "匯出到 Notion"), phrase(lang, "Exporting", "正在导出", "正在匯出"), "square.and.arrow.up.on.square", "", canExportSummary && docType == SummaryDocTypeSummary, "open-sheet", discussionActionLink(d.ID, "summary", "sheet", "notion")),
	)
	return items
}

func actionItem(id, title, loadingTitle, systemImage, role string, enabled bool, actionType, link string) discussionUIActionItem {
	return discussionUIActionItem{
		ID:           id,
		Title:        title,
		LoadingTitle: loadingTitle,
		SystemImage:  systemImage,
		Role:         role,
		Enabled:      enabled,
		Action: discussionUIAction{
			Type: actionType,
			Link: link,
		},
	}
}

func discussionActionLink(id string, parts ...string) string {
	escaped := make([]string, 0, len(parts)+1)
	escaped = append(escaped, url.PathEscape(id))
	for _, part := range parts {
		escaped = append(escaped, url.PathEscape(part))
	}
	return "debatepod://discussion/" + strings.Join(escaped, "/")
}

func queryBool(r *http.Request, key string) bool {
	switch strings.ToLower(strings.TrimSpace(r.URL.Query().Get(key))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
