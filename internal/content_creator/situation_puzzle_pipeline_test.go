package contentcreator

import "testing"

func TestStripSceneMarkers(t *testing.T) {
	cases := []struct {
		name         string
		in           string
		wantText     string
		wantLeading  int
		wantTrailing int
	}{
		{"none", "今晚的海龜湯題目是。", "今晚的海龜湯題目是。", 0, 0},
		{"leading", "<scene/>有一位男子走進餐廳。", "有一位男子走進餐廳。", 1, 0},
		// Marker at the end of a sentence — should be trailing so the
		// image only advances once this sentence's audio has played.
		{"trailing", "前情提要結束。<scene/>", "前情提要結束。", 0, 1},
		{"trailing closing form", "前情提要結束。</scene>", "前情提要結束。", 0, 1},
		// Marker mid-sentence (against the prompt rules) — folded into
		// the leading bucket so it still fires.
		{"middle", "前段話。<scene/>後段話。", "前段話。後段話。", 1, 0},
		{"bracketed leading", "[scene]三天後他回到了家。", "三天後他回到了家。", 1, 0},
		{"case insensitive trailing", "大字場景。<SCENE/>", "大字場景。", 0, 1},
		{"both ends", "<scene/>中段。<scene/>", "中段。", 1, 1},
		{"multiple leading", "<scene/><scene/>第一段。", "第一段。", 2, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotText, gotLead, gotTrail := stripSceneMarkers(c.in)
			if gotText != c.wantText {
				t.Errorf("text = %q, want %q", gotText, c.wantText)
			}
			if gotLead != c.wantLeading {
				t.Errorf("leading = %d, want %d", gotLead, c.wantLeading)
			}
			if gotTrail != c.wantTrailing {
				t.Errorf("trailing = %d, want %d", gotTrail, c.wantTrailing)
			}
		})
	}
}
