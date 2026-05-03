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
