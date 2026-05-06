package tts

import (
	"encoding/xml"
	"fmt"
	"strings"
)

// BuildSSML produces an Azure TTS SSML envelope for one voice + plain text body.
// Lang defaults to en-US when empty.
func BuildSSML(voice, text, lang string) string {
	if lang == "" {
		lang = "en-US"
	}
	if voice == "" {
		voice = "en-US-JennyNeural"
	}
	var escaped strings.Builder
	if err := xml.EscapeText(&escaped, []byte(text)); err != nil {
		// xml.EscapeText only fails on writer errors; strings.Builder never does.
		return ""
	}
	return fmt.Sprintf(
		`<speak version="1.0" xmlns="http://www.w3.org/2001/10/synthesis" xml:lang="%s"><voice name="%s">%s</voice></speak>`,
		lang, voice, escaped.String(),
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
		if p.Text == "" {
			continue
		}
		if len(merged) > 0 && merged[len(merged)-1].Voice == p.Voice {
			merged[len(merged)-1].Text += p.Text
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
		var escaped strings.Builder
		if err := xml.EscapeText(&escaped, []byte(p.Text)); err != nil {
			return ""
		}
		fmt.Fprintf(&body, `<voice name="%s">%s</voice>`, voice, escaped.String())
	}
	return fmt.Sprintf(
		`<speak version="1.0" xmlns="http://www.w3.org/2001/10/synthesis" xml:lang="%s">%s</speak>`,
		lang, body.String(),
	)
}
