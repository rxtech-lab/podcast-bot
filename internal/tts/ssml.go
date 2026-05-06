package tts

import (
	"encoding/xml"
	"fmt"
	"strings"
)

// BuildSSML produces an Azure TTS SSML envelope for one voice + plain text body.
// Lang defaults to en-US when empty.
func BuildSSML(voice, text, lang string) string {
	return BuildSSMLNodes(voice, []SpeechNode{{Text: text}}, lang)
}

// BuildSSMLNodes produces an Azure TTS SSML envelope for one voice plus
// structured text/break nodes. Lang defaults to en-US when empty.
func BuildSSMLNodes(voice string, nodes []SpeechNode, lang string) string {
	if lang == "" {
		lang = "en-US"
	}
	if voice == "" {
		voice = "en-US-JennyNeural"
	}
	rendered := renderSpeechNodes(nodes)
	if rendered == "" {
		return ""
	}
	return fmt.Sprintf(
		`<speak version="1.0" xmlns="http://www.w3.org/2001/10/synthesis" xml:lang="%s"><voice name="%s">%s</voice></speak>`,
		lang, voice, rendered,
	)
}

// VoicePart is one contiguous span of a multi-voice SSML utterance: the
// Azure neural voice ShortName and the plain-text body it should read.
// Adjacent parts with the same Voice are coalesced by BuildMultiVoiceSSML
// so a sentence with no voice switches collapses to a single <voice>
// element.
type VoicePart struct {
	Voice string
	Text  string
	Nodes []SpeechNode
}

// SpeechNode is one renderable SSML child. Text nodes are XML-escaped;
// break nodes are emitted as Azure/W3C <break> tags. A VoicePart may use
// either Text or Nodes. Nodes are preferred when present.
type SpeechNode struct {
	Text    string
	BreakMS int
}

// BuildMultiVoiceSSML emits an Azure SSML envelope with one <voice>
// element per part. Adjacent parts with the same Voice (e.g. ["narrator
// said", "narrator", "well, ", "alice"] → narrator+narrator collapse)
// are merged. Empty-text parts are dropped. Empty Voice falls back to
// the Azure default. Empty parts list returns "".
func BuildMultiVoiceSSML(parts []VoicePart, lang string) string {
	if lang == "" {
		lang = "en-US"
	}
	merged := make([]VoicePart, 0, len(parts))
	for _, p := range parts {
		if voicePartEmpty(p) {
			continue
		}
		if len(merged) > 0 && merged[len(merged)-1].Voice == p.Voice {
			merged[len(merged)-1].Nodes = append(
				voicePartNodes(merged[len(merged)-1]),
				voicePartNodes(p)...,
			)
			merged[len(merged)-1].Text = ""
			continue
		}
		merged = append(merged, p)
	}
	if len(merged) == 0 {
		return ""
	}
	var body strings.Builder
	for _, p := range merged {
		voice := p.Voice
		if voice == "" {
			voice = "en-US-JennyNeural"
		}
		rendered := renderSpeechNodes(voicePartNodes(p))
		if rendered == "" {
			return ""
		}
		fmt.Fprintf(&body, `<voice name="%s">%s</voice>`, voice, rendered)
	}
	return fmt.Sprintf(
		`<speak version="1.0" xmlns="http://www.w3.org/2001/10/synthesis" xml:lang="%s">%s</speak>`,
		lang, body.String(),
	)
}

func voicePartEmpty(p VoicePart) bool {
	for _, n := range voicePartNodes(p) {
		if n.Text != "" || n.BreakMS > 0 {
			return false
		}
	}
	return true
}

func voicePartNodes(p VoicePart) []SpeechNode {
	if len(p.Nodes) > 0 {
		return p.Nodes
	}
	if p.Text == "" {
		return nil
	}
	return []SpeechNode{{Text: p.Text}}
}

func renderSpeechNodes(nodes []SpeechNode) string {
	var body strings.Builder
	for _, n := range nodes {
		if n.Text != "" {
			var escaped strings.Builder
			if err := xml.EscapeText(&escaped, []byte(n.Text)); err != nil {
				return ""
			}
			body.WriteString(escaped.String())
		}
		if n.BreakMS > 0 {
			fmt.Fprintf(&body, `<break time="%dms"/>`, n.BreakMS)
		}
	}
	return body.String()
}
