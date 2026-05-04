package contentcreator

import "testing"

func TestStripSceneMarkers(t *testing.T) {
	cases := []struct {
		name      string
		in        string
		wantText  string
		wantCount int
	}{
		{"none", "今晚的海龜湯題目是。", "今晚的海龜湯題目是。", 0},
		{"single canonical", "<scene/>有一位男子走進餐廳。", "有一位男子走進餐廳。", 1},
		{"with whitespace", "有一位男子走進餐廳。<scene />他點了一碗湯。", "有一位男子走進餐廳。他點了一碗湯。", 1},
		{"closing form", "前情提要結束。</scene>新的場景開始。", "前情提要結束。新的場景開始。", 1},
		{"bracketed form", "三天後。[scene]他回到了家。", "三天後。他回到了家。", 1},
		{"case insensitive", "<SCENE/>大字場景。", "大字場景。", 1},
		{"multiple in one sentence", "<scene/>第一段。<scene/>第二段。", "第一段。第二段。", 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotText, gotCount := stripSceneMarkers(c.in)
			if gotText != c.wantText {
				t.Errorf("text = %q, want %q", gotText, c.wantText)
			}
			if gotCount != c.wantCount {
				t.Errorf("count = %d, want %d", gotCount, c.wantCount)
			}
		})
	}
}
