package contentcreator

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/sirily11/debate-bot/internal/agent"
	"github.com/sirily11/debate-bot/internal/tts"
)

func TestSplitNaturalSpeechStripsMarkersAndKeepsPunctuation(t *testing.T) {
	clean, chunks, hadPause, hadBreath := splitNaturalSpeech([]charSpan{{
		idx:  -1,
		text: `門開了，風停住了。<pause time="50ms"/>他吸了一口氣。<breath/>別回頭。<pause time="2s"/>`,
	}})

	if clean != "門開了，風停住了。他吸了一口氣。別回頭。" {
		t.Fatalf("clean = %q", clean)
	}
	if strings.Contains(clean, "<pause") || strings.Contains(clean, "<breath") {
		t.Fatalf("markers leaked into clean text: %q", clean)
	}
	if !hadPause || !hadBreath {
		t.Fatalf("hadPause=%v hadBreath=%v, want both true", hadPause, hadBreath)
	}
	if len(chunks) != 2 {
		t.Fatalf("chunks len = %d, want 2", len(chunks))
	}

	var breaks []int
	for _, c := range chunks {
		for _, s := range c.spans {
			for _, n := range speechNodesForSpan(s) {
				if n.BreakMS > 0 {
					breaks = append(breaks, n.BreakMS)
				}
			}
		}
	}
	if len(breaks) != 2 || breaks[0] != minPauseMS || breaks[1] != maxPauseMS {
		t.Fatalf("breaks = %v, want [%d %d]", breaks, minPauseMS, maxPauseMS)
	}
	if !chunks[0].breathAfter {
		t.Fatalf("first chunk should inject breath after speech")
	}
}

func TestNaturalBreathWritesAudioWithoutSpeech(t *testing.T) {
	chunks := []naturalChunk{{breathAfter: true}}
	var sink bytes.Buffer
	p := NewPipeline(Deps{})

	n, err := p.synthNaturalChunks(nil, nil, "", chunks, false, &sink)
	if err != nil {
		t.Fatalf("synthNaturalChunks: %v", err)
	}
	if n != int64(len(seriesBreathMP3)) {
		t.Fatalf("bytes = %d, want %d", n, len(seriesBreathMP3))
	}
	if !bytes.Equal(sink.Bytes(), seriesBreathMP3) {
		t.Fatalf("sink did not receive breath asset bytes")
	}
	if naturalChunkText(chunks[0]) != "" {
		t.Fatalf("breath-only chunk should not create visible text")
	}
}

func TestNaturalPauseFallbackProviderDoesNotSpeakMarkers(t *testing.T) {
	provider := &fallbackTTS{}
	base := agent.NewBase("Narrator", agent.RoleSeriesHost, nil, nil, nil, nil, nil)
	base.SetVoice(tts.Voice{ShortName: "narrator"})
	host := agent.NewSeriesHost(base, "Show", 1, 1, "", "", nil, nil, nil, nil, nil)
	p := NewPipeline(Deps{TTS: provider, Language: "zh-CN"})

	clean, chunks, hadPause, _ := splitNaturalSpeech([]charSpan{{
		idx:  -1,
		text: `他停下來。<pause time="500ms"/>門還開著。`,
	}})
	if clean != "他停下來。門還開著。" || !hadPause {
		t.Fatalf("clean=%q hadPause=%v", clean, hadPause)
	}

	var sink bytes.Buffer
	if _, err := p.synthNaturalChunks(context.Background(), &Turn{Speaker: host}, clean, chunks, false, &sink); err != nil {
		t.Fatalf("synthNaturalChunks: %v", err)
	}
	if provider.plainText != clean {
		t.Fatalf("plain fallback text = %q, want %q", provider.plainText, clean)
	}
	if strings.Contains(provider.plainText, "<pause") || strings.Contains(provider.plainText, "<breath") {
		t.Fatalf("marker leaked to fallback provider: %q", provider.plainText)
	}
}

func TestBuildSeriesVoicePartsCarriesPauseNodes(t *testing.T) {
	parts := buildSeriesVoiceParts("narrator", []agent.SeriesCharacter{
		{Name: "A", AzureVoice: "character"},
	}, []charSpan{
		{
			idx:  -1,
			text: "旁白。",
			nodes: []tts.SpeechNode{
				{Text: "旁白。"},
				{BreakMS: 500},
			},
		},
		{
			idx:  0,
			text: "我知道。",
			nodes: []tts.SpeechNode{
				{Text: "我知道。"},
			},
		},
	})
	if len(parts) != 2 {
		t.Fatalf("parts len = %d, want 2", len(parts))
	}
	if len(parts[0].Nodes) != 2 || parts[0].Nodes[1].BreakMS != 500 {
		t.Fatalf("pause node missing from narrator part: %#v", parts[0].Nodes)
	}
}

type fallbackTTS struct {
	plainText string
}

func (f *fallbackTTS) FetchVoices(context.Context, string) ([]tts.Voice, error) {
	return nil, nil
}

func (f *fallbackTTS) SynthesizeStream(_ context.Context, _, text, _ string) (io.ReadCloser, error) {
	f.plainText = text
	return io.NopCloser(strings.NewReader(text)), nil
}

func (f *fallbackTTS) SynthesizeSSML(context.Context, string) (io.ReadCloser, error) {
	return nil, tts.ErrSSMLUnsupported
}
