package contentcreator

import (
	"reflect"
	"testing"
)

func TestStripSceneMarkers(t *testing.T) {
	cases := []struct {
		name         string
		in           string
		wantText     string
		wantLeading  []int
		wantTrailing []int
	}{
		{"none", "今晚的海龜湯題目是。", "今晚的海龜湯題目是。", nil, nil},
		// Unnumbered legacy form: -1 sentinel = "advance one".
		{"leading legacy", "<scene/>有一位男子走進餐廳。", "有一位男子走進餐廳。", []int{-1}, nil},
		// Numbered form — the index is the absolute frame the host is
		// about to start narrating.
		{"leading numbered", "<scene 3/>有一位男子走進餐廳。", "有一位男子走進餐廳。", []int{3}, nil},
		{"leading bracketed numbered", "[scene 7]他走出小鎮。", "他走出小鎮。", []int{7}, nil},
		// Marker at the end of a sentence — should be trailing so the
		// image only advances once this sentence's audio has played.
		{"trailing legacy", "前情提要結束。<scene/>", "前情提要結束。", nil, []int{-1}},
		{"trailing numbered", "前情提要結束。<scene 5/>", "前情提要結束。", nil, []int{5}},
		{"trailing closing form", "前情提要結束。</scene>", "前情提要結束。", nil, []int{-1}},
		// Marker mid-sentence (against the prompt rules) — folded into
		// the leading bucket so it still fires.
		{"middle", "前段話。<scene 2/>後段話。", "前段話。後段話。", []int{2}, nil},
		{"bracketed leading", "[scene]三天後他回到了家。", "三天後他回到了家。", []int{-1}, nil},
		{"case insensitive trailing", "大字場景。<SCENE 4/>", "大字場景。", nil, []int{4}},
		{"both ends", "<scene 1/>中段。<scene 2/>", "中段。", []int{1}, []int{2}},
		{"multiple leading", "<scene 1/><scene 2/>第一段。", "第一段。", []int{1, 2}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotText, gotLead, gotTrail := stripSceneMarkers(c.in)
			if gotText != c.wantText {
				t.Errorf("text = %q, want %q", gotText, c.wantText)
			}
			if !reflect.DeepEqual(gotLead, c.wantLeading) {
				t.Errorf("leading = %v, want %v", gotLead, c.wantLeading)
			}
			if !reflect.DeepEqual(gotTrail, c.wantTrailing) {
				t.Errorf("trailing = %v, want %v", gotTrail, c.wantTrailing)
			}
		})
	}
}
