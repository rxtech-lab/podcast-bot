package server

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sirily11/debate-bot/internal/config"
	"github.com/sirily11/debate-bot/internal/content_creator"
)

type discussionUIActionsResponse struct {
	ID       string                   `json:"id"`
	Items    []discussionUIActionItem `json:"items"`
	Toolbars []discussionUIActionItem `json:"toolbars,omitempty"`
}

type discussionUIActionItem struct {
	ID           string                   `json:"id"`
	Title        string                   `json:"title"`
	LoadingTitle string                   `json:"loading_title,omitempty"`
	SystemImage  string                   `json:"system_image,omitempty"`
	Role         string                   `json:"role,omitempty"`
	Placement    string                   `json:"placement,omitempty"`
	Enabled      bool                     `json:"enabled"`
	Action       discussionUIAction       `json:"action"`
	Children     []discussionUIActionItem `json:"children,omitempty"`
}

type discussionUIAction struct {
	Type string `json:"type"`
	Link string `json:"link"`
}

func (s *Server) handleHomeUIActions(w http.ResponseWriter, r *http.Request) {
	lang := contentcreator.LangFromAcceptLanguage(r.Header.Get("Accept-Language"))
	writeJSON(w, discussionUIActionsResponse{
		ID:       "home-toolbar",
		Toolbars: s.applyEntitlementsForUser(r, s.homeToolbarActions(r, lang)),
	})
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
	s.applyDiscussionMindmapMeta(r.Context(), d)
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
	resp.Items = s.applyEntitlementsForUser(r, resp.Items)
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
	// The mindmap exists only for discussion-type podcasts (other types have
	// no back-and-forth to map). No terminal "unavailable" item: absence of
	// meta means the type doesn't support it or the podcast isn't ready yet.
	if discussionSupportsMindmap(d) {
		switch {
		case d.Mindmap != nil && d.Mindmap.Available:
			items = append(items, actionItem("open-mindmap", phrase(lang, "Mindmap", "思维导图", "心智圖"), "", "point.3.connected.trianglepath.dotted", "", true, "open-sheet", discussionActionLink(d.ID, "sheet", "mindmap")))
		case d.Mindmap != nil && d.Mindmap.Pending:
			items = append(items, actionItem("mindmap-pending", phrase(lang, "Generating mindmap", "正在生成思维导图", "正在產生心智圖"), "", "hourglass", "", false, "none", discussionActionLink(d.ID, "mindmap", "pending")))
		case d.Mindmap != nil && d.Mindmap.Generation:
			items = append(items, actionItem("generate-mindmap", phrase(lang, "Generate mindmap", "生成思维导图", "產生心智圖"), phrase(lang, "Generating mindmap", "正在生成思维导图", "正在產生心智圖"), "sparkles", "", true, "request", discussionActionLink(d.ID, "action", "mindmap-generate")))
		}
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
		return actionItem("generate-video", phrase(lang, "Generate Video", "生成视频", "產生影片"), phrase(lang, "Generating Video", "正在生成视频", "正在產生影片"), "video.badge.plus", "", true, "request", discussionActionLink(d.ID, "action", "video-generate")), true
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
		if d.Status == DiscussionReady {
			items = append(items, actionItem("translate-podcast", phrase(lang, "Translate Podcast", "翻译播客", "翻譯 Podcast"), "", "globe", "", true, "open-sheet", discussionActionLink(d.ID, "sheet", "translation")))
		}
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
	if d.Status == DiscussionReady && strings.TrimSpace(d.JobID) != "" {
		items = append(items, actionItem("download-captions", phrase(lang, "Download Captions", "下载字幕", "下載字幕"), "", "captions.bubble", "", true, "open-sheet", discussionActionLink(d.ID, "sheet", "captions")))
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

func (s *Server) homeToolbarActions(r *http.Request, lang contentcreator.Lang) []discussionUIActionItem {
	visibility := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("visibility")))
	switch visibility {
	case "public", "private":
	default:
		visibility = "all"
	}
	contentType := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("type")))
	switch contentType {
	case config.ContentTypeDiscussion, config.ContentTypeAudioBook:
	default:
		contentType = "all"
	}
	filterItems := []discussionUIActionItem{
		actionItem("filter-all", phrase(lang, "All", "全部", "全部"), "", filterSystemImage(visibility, "all", "tray.full"), "",
			true, "select", homeActionLink("filter", "all")),
		actionItem("filter-public", phrase(lang, "Public", "公开", "公開"), "", filterSystemImage(visibility, "public", "globe"), "",
			true, "select", homeActionLink("filter", "public")),
		actionItem("filter-private", phrase(lang, "Private", "私密", "私密"), "", filterSystemImage(visibility, "private", "lock.fill"), "",
			true, "select", homeActionLink("filter", "private")),
		dividerItem("filter-type-divider"),
		actionItem("type-all", phrase(lang, "All Types", "全部类型", "全部類型"), "", filterSystemImage(contentType, "all", "square.grid.2x2"), "",
			true, "select", homeActionLink("type", "all")),
		actionItem("type-discussion", phrase(lang, "Discussion", "讨论", "討論"), "", filterSystemImage(contentType, config.ContentTypeDiscussion, "person.2.wave.2"), "",
			true, "select", homeActionLink("type", config.ContentTypeDiscussion)),
		actionItem("type-audio-book", phrase(lang, "Audio Book", "有声书", "有聲書"), "", filterSystemImage(contentType, config.ContentTypeAudioBook, "book.closed"), "",
			true, "select", homeActionLink("type", config.ContentTypeAudioBook)),
	}
	accountItems := make([]discussionUIActionItem, 0, 5)
	if queryBool(r, "supports_points") {
		accountItems = append(accountItems, actionItem("points",
			phrase(lang, "Points", "点数", "點數"), "", "sparkles", "",
			true, "open-sheet", homeActionLink("sheet", "points")))
	}
	accountItems = append(accountItems,
		actionItem("settings", phrase(lang, "Settings", "设置", "設定"), "", "gearshape", "",
			true, "open-sheet", homeActionLink("sheet", "settings")),
		actionItem("whats-new", phrase(lang, "What's New", "新功能", "新功能"), "", "sparkles.rectangle.stack", "",
			true, "open-sheet", homeActionLink("sheet", "whats-new")),
		actionItem("refresh", phrase(lang, "Refresh", "刷新", "重新整理"), "", "arrow.clockwise", "",
			true, "request", homeActionLink("action", "refresh")),
		actionItem("sign-out", phrase(lang, "Sign Out", "退出登录", "登出"), "", "rectangle.portrait.and.arrow.right", "destructive",
			true, "request", homeActionLink("action", "sign-out")),
	)

	// Like every other create action, upload-own-audio is always listed; the
	// per-tier entitlement gating (allowsAction → canUploadOwnAudio) grays it
	// out for tiers without the permission.
	createItems := []discussionUIActionItem{
		actionItem("new-station", phrase(lang, "New Station", "新建频道", "新增頻道"), "", "waveform", "",
			true, "open-sheet", homeActionLink("sheet", "new-station")),
		actionItem("new-album", phrase(lang, "New Album", "新建专辑", "新增專輯"), "", "rectangle.stack.badge.plus", "",
			true, "open-sheet", homeActionLink("sheet", "new-album")),
		actionItem("upload-audio", phrase(lang, "Upload Own Audio", "上传音频", "上傳音訊"), "", "waveform.badge.plus", "",
			true, "open-sheet", homeActionLink("sheet", "upload-audio")),
	}
	return []discussionUIActionItem{
		actionItem("account", phrase(lang, "Account", "账号", "帳號"), "", "person.crop.circle", "",
			true, "none", homeActionLink("toolbar", "account")).withPlacement("topBarLeading").withChildren(accountItems),
		actionItem("filter", phrase(lang, "Filter", "筛选", "篩選"), "", "line.3.horizontal.decrease.circle", "",
			true, "none", homeActionLink("toolbar", "filter")).withPlacement("topBarTrailing").withChildren(filterItems),
		actionItem("market", phrase(lang, "Market", "市场", "市場"), "", "square.grid.2x2.fill", "",
			true, "open-sheet", homeActionLink("sheet", "market")).withPlacement("topBarTrailing"),
		actionItem("create", phrase(lang, "Create", "创建", "建立"), "", "plus", "",
			true, "none", homeActionLink("toolbar", "create")).withPlacement("topBarTrailing").withChildren(createItems),
	}
}

func filterSystemImage(selected, value, fallback string) string {
	if selected == value {
		return "checkmark"
	}
	return fallback
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

func dividerItem(id string) discussionUIActionItem {
	return actionItem(id, "", "", "", "", false, "divider", "")
}

func (item discussionUIActionItem) withPlacement(placement string) discussionUIActionItem {
	item.Placement = placement
	return item
}

func (item discussionUIActionItem) withChildren(children []discussionUIActionItem) discussionUIActionItem {
	item.Children = children
	return item
}

func discussionActionLink(id string, parts ...string) string {
	escaped := make([]string, 0, len(parts)+1)
	escaped = append(escaped, url.PathEscape(id))
	for _, part := range parts {
		escaped = append(escaped, url.PathEscape(part))
	}
	return "debatepod://discussion/" + strings.Join(escaped, "/")
}

func homeActionLink(parts ...string) string {
	escaped := make([]string, 0, len(parts))
	for _, part := range parts {
		escaped = append(escaped, url.PathEscape(part))
	}
	return "debatepod://home/" + strings.Join(escaped, "/")
}

// allowsAction reports whether a UI-action id is subject to entitlement gating
// and, if so, whether the resolved permissions allow it. Non-gated ids (the
// bulk of navigation/utility actions) always return allowed. This is the single
// source of truth mapping action ids → subscription features/studios.
func (p Permissions) allowsAction(id string) (gated bool, allowed bool) {
	switch id {
	case "generate-summary":
		return true, p.Features.CanGenerateSummary
	case "generate-mindmap":
		return true, p.Features.CanGenerateMindmap
	case "generate-video":
		return true, p.Features.CanGenerateVideo
	case "download-pptx", "download-slides-pdf", "ppt-document":
		return true, p.Features.CanGeneratePPT
	case "export-notion":
		return true, p.Features.CanExportToNotion
	case "share-private":
		return true, p.Features.CanSharePodcastPrivately
	case "publish", "publish-album":
		return true, p.Features.CanPublishPodcast
	case "edit-cover":
		return true, p.Features.CanGenerateCoverWithAI
	case "translate-podcast":
		return true, p.Features.CanTranslatePodcast
	case "new-album":
		return true, p.Studios.Album
	case "new-station":
		return true, p.Studios.Discussion || p.Studios.AudioBook
	case "upload-audio":
		return true, p.Features.CanUploadOwnAudio
	default:
		return false, true
	}
}

// applyEntitlements grays out (Enabled=false) any gated action the resolved
// permissions disallow, walking nested children. Items stay visible so the app
// renders them disabled rather than hiding them. It only ever disables; it
// never enables an item the builder left disabled.
func applyEntitlements(items []discussionUIActionItem, ent Permissions) []discussionUIActionItem {
	for i := range items {
		if gated, allowed := ent.allowsAction(items[i].ID); gated && !allowed {
			items[i].Enabled = false
		}
		if len(items[i].Children) > 0 {
			items[i].Children = applyEntitlements(items[i].Children, ent)
		}
	}
	return items
}

// applyEntitlementsForUser resolves the caller's permissions and grays out the
// gated actions they lack. On a resolve error it fails open (returns the items
// unchanged) so a cache/DB hiccup never blanks the app's menus — the gating
// here is advisory UI, and the generation endpoints remain the enforcement
// boundary.
func (s *Server) applyEntitlementsForUser(r *http.Request, items []discussionUIActionItem) []discussionUIActionItem {
	// Hermetic E2E runs configure no subscription_permissions, so gating here
	// would blanket-disable every server-driven action and break the UI tests
	// that exercise those flows. Client-side native gating is tested separately
	// via the E2E_NO_PERMISSION launch flag.
	if s.d.Env != nil && s.d.Env.E2EMode {
		return items
	}
	ent, err := s.resolveEntitlements(r.Context(), s.requestUser(r).ID)
	if err != nil {
		s.logger().Warn("resolve entitlements for ui-actions", "err", err)
		return items
	}
	return applyEntitlements(items, ent)
}

func queryBool(r *http.Request, key string) bool {
	switch strings.ToLower(strings.TrimSpace(r.URL.Query().Get(key))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
