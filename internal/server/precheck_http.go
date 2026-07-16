package server

import (
	"net/http"

	"github.com/sirily11/debate-bot/internal/config"
	"github.com/sirily11/debate-bot/internal/content_creator"
	"github.com/sirily11/debate-bot/internal/planner"
)

type precheckResponse struct {
	NewDiscussion precheckNewDiscussion `json:"new_discussion"`
	NewAlbum      precheckNewAlbum      `json:"new_album"`
	// UploadAudio is present only when the upload-own-audio feature is globally
	// enabled AND the caller's subscription tier grants it.
	UploadAudio *precheckUploadAudio `json:"upload_audio,omitempty"`
	// Maintenance is present while a maintenance window is active (Active=true)
	// or upcoming (Active=false), so the client can pause with a message or warn
	// users ahead of a scheduled pause.
	Maintenance *maintenanceInfo `json:"maintenance,omitempty"`
}

type precheckNewDiscussion struct {
	Form precheckForm `json:"form"`
}

type precheckNewAlbum struct {
	Form precheckForm `json:"form"`
}

type precheckUploadAudio struct {
	Form precheckForm `json:"form"`
}

type precheckForm struct {
	Title        string               `json:"title"`
	Description  string               `json:"description,omitempty"`
	SubmitTitle  string               `json:"submit_title"`
	CancelTitle  string               `json:"cancel_title"`
	LoadingTitle string               `json:"loading_title"`
	Schema       map[string]any       `json:"schema"`
	UISchema     map[string]any       `json:"ui_schema,omitempty"`
	InitialData  map[string]any       `json:"initial_data,omitempty"`
	Actions      []precheckFormAction `json:"actions,omitempty"`
}

type precheckFormAction struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	SystemImage string `json:"system_image,omitempty"`
	DeepLink    string `json:"deep_link,omitempty"`
}

type precheckOption struct {
	ID    string
	Label string
}

func (s *Server) handlePrecheck(w http.ResponseWriter, r *http.Request) {
	lang := contentcreator.LangFromAcceptLanguage(r.Header.Get("Accept-Language"))
	shareExtension := r.URL.Query().Get("surface") == "share-extension"
	resp := precheckResponse{
		NewDiscussion: precheckNewDiscussion{
			Form: newDiscussionPrecheckFormForSurface(lang, shareExtension),
		},
		NewAlbum: precheckNewAlbum{
			Form: newAlbumPrecheckForm(lang),
		},
		Maintenance: s.relevantMaintenance(r),
	}
	user := s.requestUser(r)
	if s.uploadAudioAllowedForUser(r.Context(), user.ID) {
		resp.UploadAudio = &precheckUploadAudio{
			Form: uploadAudioPrecheckForm(lang, s.uploadAudioCapBytes(r.Context(), user.ID)),
		}
	}
	writeJSON(w, resp)
}

// uploadAudioPrecheckForm is the server-owned "Upload Own Audio" form. The
// client uploads the audio through the audioPicker widget (presign →
// direct-to-S3 PUT → complete, kind=podcast-audio) and then posts the form
// values verbatim to POST /api/discussions/upload-audio.
func uploadAudioPrecheckForm(lang contentcreator.Lang, capBytes int64) precheckForm {
	return precheckForm{
		Title:        phrase(lang, "Upload Own Audio", "上传音频", "上傳音訊"),
		Description:  phrase(lang, "Turn your own recording into a station with transcript and captions.", "把你自己的录音变成带文稿和字幕的频道。", "把你自己的錄音變成帶文稿和字幕的頻道。"),
		SubmitTitle:  phrase(lang, "Transcribe", "转写", "轉寫"),
		CancelTitle:  phrase(lang, "Cancel", "取消", "取消"),
		LoadingTitle: phrase(lang, "Starting transcription...", "正在开始转写…", "正在開始轉寫…"),
		Schema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"audio": map[string]any{
					"type":        "object",
					"title":       phrase(lang, "Audio file", "音频文件", "音訊檔案"),
					"description": phrase(lang, "Pick the recording to transcribe.", "选择要转写的录音。", "選擇要轉寫的錄音。"),
					"properties": map[string]any{
						"key":        map[string]any{"type": "string", "minLength": 1},
						"filename":   map[string]any{"type": "string"},
						"mime_type":  map[string]any{"type": "string"},
						"size_bytes": map[string]any{"type": "integer"},
					},
					"required": []any{"key"},
				},
				"settings": map[string]any{
					"type":                 "object",
					"title":                phrase(lang, "Settings", "设置", "設定"),
					"additionalProperties": false,
					"properties": map[string]any{
						"max_speakers": map[string]any{
							"type":        "integer",
							"title":       phrase(lang, "Max speakers", "最多说话人数", "最多說話人數"),
							"description": phrase(lang, "The most voices the transcriber should tell apart.", "转写时最多区分的说话人数。", "轉寫時最多區分的說話人數。"),
							"minimum":     2,
							"maximum":     35,
							"default":     2,
						},
					},
					"required": []any{"max_speakers"},
				},
			},
			"required": []any{"audio", "settings"},
		},
		UISchema: map[string]any{
			"ui:order": []any{"audio", "settings"},
			"audio": map[string]any{
				"ui:widget": "audioPicker",
				"ui:options": map[string]any{
					"icon":      "waveform",
					"max_bytes": capBytes,
				},
			},
			"settings": map[string]any{
				"ui:objectTemplate": "card",
				"ui:order":          []any{"max_speakers"},
				"max_speakers": map[string]any{
					"ui:widget":  "glassStepper",
					"ui:options": map[string]any{"icon": "person.2.fill"},
				},
			},
		},
		InitialData: map[string]any{
			"audio": map[string]any{},
			"settings": map[string]any{
				"max_speakers": 2,
			},
		},
	}
}

// newAlbumPrecheckForm is the server-owned "New Album" form. The client posts
// the form values verbatim to POST /api/albums; with no discussion_ids the
// server creates an empty album episodes are added to later.
func newAlbumPrecheckForm(lang contentcreator.Lang) precheckForm {
	return precheckForm{
		Title:        phrase(lang, "New Album", "新建专辑", "新增專輯"),
		Description:  phrase(lang, "Group stations into one collection.", "把频道整理成一个合集。", "把頻道整理成一個合集。"),
		SubmitTitle:  phrase(lang, "Create", "创建", "建立"),
		CancelTitle:  phrase(lang, "Cancel", "取消", "取消"),
		LoadingTitle: phrase(lang, "Creating album...", "正在创建专辑…", "正在建立專輯…"),
		Schema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"title": map[string]any{
					"type":        "string",
					"title":       phrase(lang, "Name", "名称", "名稱"),
					"description": phrase(lang, "You can add episodes from the album page after it is created.", "创建后可在专辑页面添加剧集。", "建立後可在專輯頁面新增劇集。"),
					"minLength":   1,
					"default":     "",
				},
			},
			"required": []any{"title"},
		},
		UISchema: map[string]any{
			"ui:order": []any{"title"},
			"title": map[string]any{
				"ui:widget": "glassText",
				"ui:options": map[string]any{
					"placeholder":      phrase(lang, "e.g. World History Series", "例如：世界历史系列", "例如：世界歷史系列"),
					"multiline":        false,
					"accessibility_id": "newAlbum.title",
				},
			},
		},
		InitialData: map[string]any{
			"title": "",
		},
	}
}

func newDiscussionPrecheckForm(lang contentcreator.Lang) precheckForm {
	return newDiscussionPrecheckFormForSurface(lang, false)
}

// newDiscussionPrecheckFormForSurface keeps the same backend-owned creation
// settings on every client surface. The share extension already owns the
// incoming files/URLs, so it must not render a second "add attachments" picker.
func newDiscussionPrecheckFormForSurface(lang contentcreator.Lang, shareExtension bool) precheckForm {
	templateIDs, templateOptions, templateLabels := templateMetadata(config.ContentTypeDiscussion, lang)
	audioBookTemplateIDs, audioBookTemplateOptions, _ := templateMetadata(config.ContentTypeAudioBook, lang)

	languages := []precheckOption{
		{ID: "en-US", Label: phrase(lang, "English", "英语", "英語")},
		{ID: "zh-CN", Label: phrase(lang, "Chinese (Simplified)", "中文（简体）", "中文（簡體）")},
		{ID: "zh-TW", Label: phrase(lang, "Chinese (Traditional)", "中文（繁体）", "中文（繁體）")},
		{ID: "ja-JP", Label: phrase(lang, "Japanese", "日语", "日語")},
		{ID: "ko-KR", Label: phrase(lang, "Korean", "韩语", "韓語")},
		{ID: "es-ES", Label: phrase(lang, "Spanish", "西班牙语", "西班牙語")},
		{ID: "fr-FR", Label: phrase(lang, "French", "法语", "法語")},
		{ID: "de-DE", Label: phrase(lang, "German", "德语", "德語")},
	}
	languageIDs := make([]any, 0, len(languages))
	languageLabels := make([]any, 0, len(languages))
	languageOptions := make([]any, 0, len(languages))
	for _, opt := range languages {
		languageIDs = append(languageIDs, opt.ID)
		languageLabels = append(languageLabels, opt.Label)
		languageOptions = append(languageOptions, map[string]any{"id": opt.ID, "label": opt.Label})
	}
	settingsSchema := newDiscussionSettingsSchema(lang, templateIDs, templateOptions, languageIDs, languageOptions)
	templateOptionsByType := map[string]any{
		config.ContentTypeDiscussion: templateOptions,
		config.ContentTypeAudioBook:  audioBookTemplateOptions,
	}
	templateIDsByType := map[string]any{
		config.ContentTypeDiscussion: templateIDs,
		config.ContentTypeAudioBook:  audioBookTemplateIDs,
	}
	settingsProps := settingsSchema["properties"].(map[string]any)
	templateSchema := settingsProps["template"].(map[string]any)
	templateSchema["x-enum-by-type"] = templateIDsByType
	templateSchema["x-options-by-type"] = templateOptionsByType

	form := precheckForm{
		Title:        phrase(lang, "New Station", "新建频道", "新增頻道"),
		Description:  phrase(lang, "Tell the planner what the conversation should explore.", "告诉规划器这场讨论要探索什么。", "告訴規劃器這場討論要探索什麼。"),
		SubmitTitle:  phrase(lang, "Plan", "规划", "規劃"),
		CancelTitle:  phrase(lang, "Cancel", "取消", "取消"),
		LoadingTitle: phrase(lang, "Creating station...", "正在创建频道…", "正在建立頻道…"),
		Schema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"prompt": map[string]any{
					"type":                 "object",
					"title":                phrase(lang, "Prompt", "提示", "提示"),
					"additionalProperties": false,
					"properties": map[string]any{
						"topic": map[string]any{
							"type":        "string",
							"title":       phrase(lang, "Topic", "主题", "主題"),
							"description": phrase(lang, "Paste a link or describe what the agent should plan.", "粘贴链接，或描述希望智能体规划的内容。", "貼上連結，或描述希望智能體規劃的內容。"),
							"minLength":   1,
							"default":     "",
						},
					},
					"required": []any{"topic"},
				},
				"attachments": map[string]any{
					"type":        "array",
					"title":       phrase(lang, "Attachments", "附件", "附件"),
					"description": phrase(lang, "Add Notion pages, images, or documents to ground the plan.", "添加 Notion 页面、图片或文档作为规划依据。", "新增 Notion 頁面、圖片或文件作為規劃依據。"),
					"default":     []any{},
					// Each item is a server Attachment (filename + markdown/url +
					// mime_type) filled in by the client's attachmentsPicker widget
					// after upload; the server reads them back as planner.Attachment.
					"items": map[string]any{
						"type":                 "object",
						"additionalProperties": true,
					},
				},
				"reference": map[string]any{
					"type":                 "object",
					"title":                phrase(lang, "Parent Discussion", "父讨论", "父討論"),
					"additionalProperties": false,
					"properties": map[string]any{
						"discussion_id": map[string]any{
							"type":        "string",
							"title":       phrase(lang, "Parent Discussion", "父讨论", "父討論"),
							"description": phrase(lang, "Continue from an existing station as context.", "以现有频道作为上下文继续。", "以現有頻道作為上下文繼續。"),
							"default":     "",
						},
					},
				},
				"settings": settingsSchema,
			},
			"required": []any{"prompt", "settings"},
		},
		UISchema: map[string]any{
			"ui:order": []any{"prompt", "attachments", "reference", "settings"},
			"prompt": map[string]any{
				"ui:order": []any{"topic"},
				"topic": map[string]any{
					"ui:widget": "glassText",
					"ui:options": map[string]any{
						"placeholder": phrase(lang, "e.g. The future of AI in education", "例如：人工智能在教育中的未来", "例如：人工智慧在教育中的未來"),
					},
				},
			},
			"attachments": map[string]any{
				"ui:widget": "attachmentsPicker",
				"ui:options": map[string]any{
					"icon":      "paperclip",
					"deep_link": "debatepod://attachment-picker",
				},
			},
			"reference": map[string]any{
				"ui:objectTemplate": "card",
				"ui:order":          []any{"discussion_id"},
				"discussion_id": map[string]any{
					"ui:widget": "discussionPicker",
					"ui:options": map[string]any{
						"icon":      "rectangle.stack.badge.play",
						"deep_link": "debatepod://discussion-picker",
					},
				},
			},
			"settings": map[string]any{
				"ui:objectTemplate": "card",
				"ui:order":          []any{"type", "template", "discussants", "language", "generate_cover"},
				"type": map[string]any{
					"ui:widget":    "glassMenu",
					"ui:enumNames": []any{phrase(lang, "Discussion", "讨论", "討論"), phrase(lang, "Audio Book", "有声书", "有聲書")},
					"ui:options":   map[string]any{"icon": "book.pages.fill"},
				},
				"template": map[string]any{
					"ui:widget":    "glassMenu",
					"ui:enumNames": templateLabels,
					"ui:options": map[string]any{
						"icon":            "square.grid.2x2",
						"options_by_type": templateOptionsByType,
					},
				},
				"discussants": map[string]any{
					"ui:widget":  "glassStepper",
					"ui:options": map[string]any{"icon": "person.2.fill"},
				},
				"language": map[string]any{
					"ui:widget":    "glassMenu",
					"ui:enumNames": languageLabels,
					"ui:options":   map[string]any{"icon": "globe"},
				},
				"generate_cover": map[string]any{
					"ui:widget":  "glassToggle",
					"ui:options": map[string]any{"icon": "photo.badge.plus"},
				},
			},
		},
		InitialData: map[string]any{
			"prompt": map[string]any{
				"topic": "",
			},
			"attachments": []any{},
			"reference": map[string]any{
				"discussion_id": "",
			},
			"settings": map[string]any{
				"type":           config.ContentTypeDiscussion,
				"template":       planner.DefaultTemplateID,
				"discussants":    3,
				"language":       "en-US",
				"generate_cover": false,
			},
		},
	}
	if shareExtension {
		properties := form.Schema["properties"].(map[string]any)
		delete(properties, "attachments")
		uiOrder := form.UISchema["ui:order"].([]any)
		filteredOrder := make([]any, 0, len(uiOrder)-1)
		for _, field := range uiOrder {
			if field != "attachments" {
				filteredOrder = append(filteredOrder, field)
			}
		}
		form.UISchema["ui:order"] = filteredOrder
		delete(form.UISchema, "attachments")
		delete(form.InitialData, "attachments")
	}
	return form
}

func templateMetadata(contentType string, lang contentcreator.Lang) (ids, options, labels []any) {
	templates := planner.TemplatesByType(contentType)
	ids = make([]any, 0, len(templates))
	options = make([]any, 0, len(templates))
	labels = make([]any, 0, len(templates))
	for _, tmpl := range templates {
		label := templateLabel(tmpl.ID, lang)
		ids = append(ids, tmpl.ID)
		labels = append(labels, label)
		options = append(options, map[string]any{
			"id":          tmpl.ID,
			"label":       label,
			"description": templateDescription(contentType, tmpl.ID, lang),
		})
	}
	return ids, options, labels
}

func newDiscussionSettingsSchema(lang contentcreator.Lang, templateIDs, templateOptions, languageIDs, languageOptions []any) map[string]any {
	return map[string]any{
		"type":                 "object",
		"title":                phrase(lang, "Settings", "设置", "設定"),
		"additionalProperties": false,
		"properties": map[string]any{
			"type": map[string]any{
				"type":        "string",
				"title":       phrase(lang, "Type", "类型", "類型"),
				"description": phrase(lang, "The kind of station to plan.", "要规划的频道类型。", "要規劃的頻道類型。"),
				"enum":        []any{config.ContentTypeDiscussion, config.ContentTypeAudioBook},
				"default":     config.ContentTypeDiscussion,
				"x-options": []any{
					map[string]any{"id": config.ContentTypeDiscussion, "label": phrase(lang, "Discussion", "讨论", "討論")},
					map[string]any{"id": config.ContentTypeAudioBook, "label": phrase(lang, "Audio Book", "有声书", "有聲書")},
				},
			},
			"template": map[string]any{
				"type":        "string",
				"title":       phrase(lang, "Template", "模板", "範本"),
				"description": phrase(lang, "Choose how the planner should structure the discussion.", "选择规划器组织讨论的方式。", "選擇規劃器組織討論的方式。"),
				"enum":        templateIDs,
				"default":     planner.DefaultTemplateID,
				"x-options":   templateOptions,
			},
			"language": map[string]any{
				"type":        "string",
				"title":       phrase(lang, "Language", "语言", "語言"),
				"description": phrase(lang, "The language used for planning and generation.", "规划和生成使用的语言。", "規劃和生成使用的語言。"),
				"enum":        languageIDs,
				"default":     "en-US",
				"x-options":   languageOptions,
			},
			"generate_cover": map[string]any{
				"type":        "boolean",
				"title":       phrase(lang, "Generate cover", "生成封面", "生成封面"),
				"description": phrase(lang, "Create AI cover art in the background after the station is created.", "频道创建后在后台生成 AI 封面。", "頻道建立後在背景生成 AI 封面。"),
				"default":     false,
			},
		},
		"required": []any{"type", "template", "language"},
		// Panelists only make sense for discussions, so the field lives in a
		// standard JSON Schema if/then conditional keyed on the selected type;
		// the iOS form renderer shows/hides the row as the type changes.
		// Note: additionalProperties=false above plus a then-only property is
		// technically strict-draft-07-unfriendly (additionalProperties can't
		// see then-branch properties), but nothing in the stack validates
		// submissions against this schema — it drives rendering only.
		"if": map[string]any{
			"properties": map[string]any{
				"type": map[string]any{"const": config.ContentTypeDiscussion},
			},
			"required": []any{"type"},
		},
		"then": map[string]any{
			"properties": map[string]any{
				"discussants": map[string]any{
					"type":        "integer",
					"title":       phrase(lang, "Panelists", "嘉宾", "來賓"),
					"description": phrase(lang, "How many people should join the conversation.", "参与讨论的人数。", "參與討論的人數。"),
					"minimum":     2,
					"maximum":     6,
					"default":     3,
				},
			},
			"required": []any{"discussants"},
		},
	}
}

func phrase(lang contentcreator.Lang, en, hans, hant string) string {
	switch lang {
	case contentcreator.LangHans:
		return hans
	case contentcreator.LangHant:
		return hant
	default:
		return en
	}
}

func templateLabel(id string, lang contentcreator.Lang) string {
	switch id {
	case planner.ResearchTemplateID:
		return phrase(lang, "Research", "研究", "研究")
	case planner.AudioBookNewsTemplateID:
		return phrase(lang, "News", "新闻", "新聞")
	case planner.AudioBookConversationalTemplateID:
		return phrase(lang, "Conversational", "对话", "對話")
	case planner.AudioBookAudioBookTemplateID:
		return phrase(lang, "Audiobook", "有声书", "有聲書")
	case planner.AudioBookPodcastTemplateID:
		return phrase(lang, "Podcast", "播客", "Podcast")
	case planner.AudioBookMeetingTemplateID:
		return phrase(lang, "Meeting", "会议", "會議")
	default:
		return phrase(lang, "Auto", "自动", "自動")
	}
}

func templateDescription(contentType, id string, lang contentcreator.Lang) string {
	if contentType == config.ContentTypeAudioBook {
		switch id {
		case planner.AudioBookNewsTemplateID:
			return phrase(lang, "A news-style audiobook with a main presenter and supporting voices.", "新闻风格有声书，包含主讲人和辅助声音。", "新聞風格有聲書，包含主講人和輔助聲音。")
		case planner.AudioBookConversationalTemplateID:
			return phrase(lang, "A conversational audiobook with one main voice and question-asking guests.", "对话式有声书，一个主讲声音，其他人提问或补充。", "對話式有聲書，一個主講聲音，其他人提問或補充。")
		case planner.AudioBookAudioBookTemplateID:
			return phrase(lang, "A classic narrated audiobook with light character or quote voices.", "经典旁白有声书，可少量加入角色或引用声音。", "經典旁白有聲書，可少量加入角色或引用聲音。")
		case planner.AudioBookPodcastTemplateID:
			return phrase(lang, "A podcast-style audiobook led by a host with supporting speakers.", "播客风格有声书，由主持人主导并加入辅助说话人。", "Podcast 風格有聲書，由主持人主導並加入輔助說話人。")
		case planner.AudioBookMeetingTemplateID:
			return phrase(lang, "A meeting-style audiobook with a facilitator and participant questions.", "会议风格有声书，包含主持人和参会者提问。", "會議風格有聲書，包含主持人和參與者提問。")
		default:
			return phrase(lang, "Let the agent choose the best audiobook style for the source.", "让智能体根据素材选择最合适的有声书风格。", "讓智能體根據素材選擇最合適的有聲書風格。")
		}
	}
	switch id {
	case planner.ResearchTemplateID:
		return phrase(lang,
			"A school-style discussion grounded in research papers and cited evidence.",
			"基于研究论文和引用证据的课堂式讨论。",
			"基於研究論文和引用證據的課堂式討論。")
	default:
		return phrase(lang,
			"A balanced panel discussion with a host, background, and discussants.",
			"包含主持人、背景和嘉宾的均衡圆桌讨论。",
			"包含主持人、背景和來賓的均衡圓桌討論。")
	}
}
