package server

import (
	"net/http"

	"github.com/sirily11/debate-bot/internal/config"
	"github.com/sirily11/debate-bot/internal/content_creator"
	"github.com/sirily11/debate-bot/internal/planner"
)

type precheckResponse struct {
	NewDiscussion precheckNewDiscussion `json:"new_discussion"`
}

type precheckNewDiscussion struct {
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
	writeJSON(w, precheckResponse{
		NewDiscussion: precheckNewDiscussion{
			Form: newDiscussionPrecheckForm(lang),
		},
	})
}

func newDiscussionPrecheckForm(lang contentcreator.Lang) precheckForm {
	templates := planner.TemplatesByType(config.ContentTypeDiscussion)
	templateIDs := make([]any, 0, len(templates))
	templateOptions := make([]any, 0, len(templates))
	templateLabels := make([]any, 0, len(templates))
	for _, tmpl := range templates {
		label := templateLabel(tmpl.ID, lang)
		templateIDs = append(templateIDs, tmpl.ID)
		templateLabels = append(templateLabels, label)
		templateOptions = append(templateOptions, map[string]any{
			"id":          tmpl.ID,
			"label":       label,
			"description": templateDescription(tmpl.ID, lang),
		})
	}

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

	return precheckForm{
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
				"settings": map[string]any{
					"type":                 "object",
					"title":                phrase(lang, "Settings", "设置", "設定"),
					"additionalProperties": false,
					"properties": map[string]any{
						"type": map[string]any{
							"type":        "string",
							"title":       phrase(lang, "Type", "类型", "類型"),
							"description": phrase(lang, "The kind of station to plan.", "要规划的频道类型。", "要規劃的頻道類型。"),
							"enum":        []any{config.ContentTypeDiscussion},
							"default":     config.ContentTypeDiscussion,
							"x-options": []any{
								map[string]any{"id": config.ContentTypeDiscussion, "label": phrase(lang, "Discussion", "讨论", "討論")},
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
						"discussants": map[string]any{
							"type":        "integer",
							"title":       phrase(lang, "Panelists", "嘉宾", "來賓"),
							"description": phrase(lang, "How many people should join the conversation.", "参与讨论的人数。", "參與討論的人數。"),
							"minimum":     2,
							"maximum":     6,
							"default":     3,
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
					"required": []any{"type", "template", "discussants", "language"},
				},
			},
			"required": []any{"prompt", "settings"},
		},
		UISchema: map[string]any{
			"ui:order": []any{"prompt", "reference", "settings"},
			"prompt": map[string]any{
				"ui:order": []any{"topic"},
				"topic": map[string]any{
					"ui:widget": "glassText",
					"ui:options": map[string]any{
						"placeholder": phrase(lang, "e.g. The future of AI in education", "例如：人工智能在教育中的未来", "例如：人工智慧在教育中的未來"),
					},
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
					"ui:enumNames": []any{phrase(lang, "Discussion", "讨论", "討論")},
					"ui:options":   map[string]any{"icon": "bubble.left.and.bubble.right.fill"},
				},
				"template": map[string]any{
					"ui:widget":    "glassMenu",
					"ui:enumNames": templateLabels,
					"ui:options":   map[string]any{"icon": "square.grid.2x2"},
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
	default:
		return phrase(lang, "Default", "默认", "預設")
	}
}

func templateDescription(id string, lang contentcreator.Lang) string {
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
