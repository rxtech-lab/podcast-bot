package tts

import (
	"strings"
	"testing"
)

func TestBuildSSMLNodesEscapesTextAndEmitsBreak(t *testing.T) {
	got := BuildSSMLNodes("zh-CN-YunxiNeural", []SpeechNode{
		{Text: "門開了，風停住了。"},
		{BreakMS: 500},
		{Text: "他說：<別回頭> & 走。"},
	}, "zh-CN")

	if !strings.Contains(got, `門開了，<break time="120ms"/>風停住了。`) {
		t.Fatalf("punctuated text missing from ssml: %s", got)
	}
	if !strings.Contains(got, `<prosody rate="-10%">`) {
		t.Fatalf("prosody rate missing from ssml: %s", got)
	}
	if !strings.Contains(got, `<break time="500ms"/>`) {
		t.Fatalf("break tag missing from ssml: %s", got)
	}
	if !strings.Contains(got, `他說：<break time="120ms"/>&lt;別回頭&gt; &amp; 走。`) {
		t.Fatalf("text was not XML-escaped: %s", got)
	}
}

func TestBuildMultiVoiceSSMLNodes(t *testing.T) {
	got := BuildMultiVoiceSSML([]VoicePart{
		{
			Voice: "zh-CN-YunxiNeural",
			Nodes: []SpeechNode{
				{Text: "旁白。"},
				{BreakMS: 300},
			},
		},
		{
			Voice: "zh-CN-XiaoxiaoNeural",
			Nodes: []SpeechNode{
				{Text: "我知道。"},
			},
		},
	}, "zh-CN")

	for _, want := range []string{
		`<voice name="zh-CN-YunxiNeural"><prosody rate="-10%">旁白。<break time="300ms"/></prosody></voice>`,
		`<voice name="zh-CN-XiaoxiaoNeural"><prosody rate="-10%">我知道。<break time="220ms"/></prosody></voice>`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("ssml missing %q: %s", want, got)
		}
	}
}

func TestBuildSSMLNodesAddsAutomaticPacing(t *testing.T) {
	got := BuildSSMLNodes("zh-CN-YunxiNeural", []SpeechNode{
		{Text: "第一句。\n\n第二句。"},
	}, "zh-CN")

	for _, want := range []string{
		`第一句。<break time="220ms"/>`,
		`<break time="500ms"/>第二句。<break time="220ms"/>`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("ssml missing automatic pacing %q: %s", want, got)
		}
	}
}

func TestBuildSSMLNodesAddsClausePacing(t *testing.T) {
	got := BuildSSMLNodes("zh-CN-YunxiNeural", []SpeechNode{
		{Text: "别人都在门后生活，他却在白色的寂静里穿行，把信送到门前。"},
	}, "zh-CN")

	for _, want := range []string{
		`别人都在门后生活，<break time="120ms"/>他却在白色的寂静里穿行，<break time="120ms"/>把信送到门前。<break time="220ms"/>`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("ssml missing clause pacing %q: %s", want, got)
		}
	}
}
