package contentcreator

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/sirily11/debate-bot/internal/agent"
	"github.com/sirily11/debate-bot/internal/config"
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

func TestTranscriptSpansFromNaturalChunksExcludePauseMarkers(t *testing.T) {
	_, chunks, hadPause, _ := splitNaturalSpeech([]charSpan{{
		idx: -1,
		text: `<pause time="800ms"/>
這條消息發出來的瞬間，群突然停滯了。`,
	}})
	if !hadPause {
		t.Fatal("expected pause marker")
	}
	spans := transcriptSpansFromNaturalChunks(chunks)
	if len(spans) != 1 {
		t.Fatalf("spans len = %d, want 1", len(spans))
	}
	if got, want := strings.TrimSpace(spans[0].text), "這條消息發出來的瞬間，群突然停滯了。"; got != want {
		t.Fatalf("span text = %q, want %q", got, want)
	}
	if strings.Contains(spans[0].text, "<pause") {
		t.Fatalf("pause marker leaked into transcript span: %q", spans[0].text)
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

func TestAudioBookNaturalBreathDoesNotWriteAudibleAsset(t *testing.T) {
	chunks := []naturalChunk{{breathAfter: true}}
	var sink bytes.Buffer
	p := NewPipeline(Deps{ContentType: config.ContentTypeAudioBook})

	n, err := p.synthNaturalChunks(nil, nil, "", chunks, false, &sink)
	if err != nil {
		t.Fatalf("synthNaturalChunks: %v", err)
	}
	if n != 0 {
		t.Fatalf("bytes = %d, want 0", n)
	}
	if sink.Len() != 0 {
		t.Fatalf("audiobook breath should not write audible bytes, got %d", sink.Len())
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

func TestAudioBookSynthRetriesDetectedDropoutBeforeWriting(t *testing.T) {
	provider := &retryTTS{responses: []string{"bad-audio", "clean-audio"}}
	base := agent.NewBase("Narrator", agent.RoleSeriesHost, nil, nil, nil, nil, nil)
	base.SetVoice(tts.Voice{ShortName: "narrator"})
	host := agent.NewSeriesHost(base, "Show", 1, 1, "", "", nil, nil, nil, nil, nil)
	p := NewPipeline(Deps{
		ContentType: config.ContentTypeAudioBook,
		TTS:         provider,
		Language:    "zh-CN",
	})

	oldInspect := inspectSynthAudioDropout
	t.Cleanup(func() { inspectSynthAudioDropout = oldInspect })
	var inspected int
	inspectSynthAudioDropout = func(_ context.Context, data []byte, _ bool) (synthAudioDropout, error) {
		inspected++
		if string(data) == "bad-audio" {
			return synthAudioDropout{Found: true, Start: 300 * time.Millisecond, End: 680 * time.Millisecond}, nil
		}
		return synthAudioDropout{}, nil
	}

	chunks := []naturalChunk{{spans: []charSpan{{idx: -1, text: "正当张博士准备抛出他的最终结论。"}}}}
	var sink bytes.Buffer
	n, err := p.synthNaturalChunks(context.Background(), &Turn{ID: 7, Speaker: host}, naturalChunkText(chunks[0]), chunks, false, &sink)
	if err != nil {
		t.Fatalf("synthNaturalChunks: %v", err)
	}
	if provider.calls != 2 {
		t.Fatalf("tts calls = %d, want retry", provider.calls)
	}
	if inspected != 2 {
		t.Fatalf("inspect calls = %d, want 2", inspected)
	}
	if got, want := sink.String(), "clean-audio"; got != want {
		t.Fatalf("sink = %q, want %q", got, want)
	}
	if got, want := n, int64(len("clean-audio")); got != want {
		t.Fatalf("bytes = %d, want %d", got, want)
	}
}

func TestNonAudioBookSynthDoesNotBufferForDropoutInspection(t *testing.T) {
	provider := &retryTTS{responses: []string{"bad-audio", "clean-audio"}}
	base := agent.NewBase("Narrator", agent.RoleSeriesHost, nil, nil, nil, nil, nil)
	base.SetVoice(tts.Voice{ShortName: "narrator"})
	host := agent.NewSeriesHost(base, "Show", 1, 1, "", "", nil, nil, nil, nil, nil)
	p := NewPipeline(Deps{
		ContentType: config.ContentTypeSeries,
		TTS:         provider,
		Language:    "zh-CN",
	})

	oldInspect := inspectSynthAudioDropout
	t.Cleanup(func() { inspectSynthAudioDropout = oldInspect })
	inspectSynthAudioDropout = func(context.Context, []byte, bool) (synthAudioDropout, error) {
		t.Fatal("non-audiobook synthesis should not inspect buffered audio")
		return synthAudioDropout{}, nil
	}

	chunks := []naturalChunk{{spans: []charSpan{{idx: -1, text: "普通旁白。"}}}}
	var sink bytes.Buffer
	if _, err := p.synthNaturalChunks(context.Background(), &Turn{ID: 8, Speaker: host}, naturalChunkText(chunks[0]), chunks, false, &sink); err != nil {
		t.Fatalf("synthNaturalChunks: %v", err)
	}
	if provider.calls != 1 {
		t.Fatalf("tts calls = %d, want 1", provider.calls)
	}
	if got, want := sink.String(), "bad-audio"; got != want {
		t.Fatalf("sink = %q, want %q", got, want)
	}
}

func TestDetectPCMZeroDropoutFlagsMidChunkGap(t *testing.T) {
	pcm := pcmFixture(
		300*time.Millisecond,
		390*time.Millisecond,
		400*time.Millisecond,
	)
	got := detectPCMZeroDropout(pcm, audiobookDropoutSampleRate, false)
	if !got.Found {
		t.Fatal("dropout not detected")
	}
	if got.Duration() < audiobookDropoutMinDuration {
		t.Fatalf("duration = %v, want >= %v", got.Duration(), audiobookDropoutMinDuration)
	}
}

func TestDetectPCMZeroDropoutIgnoresExpectedPauses(t *testing.T) {
	leading := pcmFixture(
		0,
		800*time.Millisecond,
		500*time.Millisecond,
	)
	if got := detectPCMZeroDropout(leading, audiobookDropoutSampleRate, false); got.Found {
		t.Fatalf("leading silence detected as dropout: %+v", got)
	}

	explicitBreak := pcmFixture(
		300*time.Millisecond,
		500*time.Millisecond,
		400*time.Millisecond,
	)
	if got := detectPCMZeroDropout(explicitBreak, audiobookDropoutSampleRate, true); got.Found {
		t.Fatalf("explicit break detected as dropout: %+v", got)
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

type retryTTS struct {
	responses []string
	calls     int
}

func (f *retryTTS) FetchVoices(context.Context, string) ([]tts.Voice, error) {
	return nil, nil
}

func (f *retryTTS) SynthesizeStream(context.Context, string, string, string) (io.ReadCloser, error) {
	if f.calls >= len(f.responses) {
		f.calls++
		return io.NopCloser(strings.NewReader(f.responses[len(f.responses)-1])), nil
	}
	out := f.responses[f.calls]
	f.calls++
	return io.NopCloser(strings.NewReader(out)), nil
}

func (f *retryTTS) SynthesizeSSML(context.Context, string) (io.ReadCloser, error) {
	return nil, tts.ErrSSMLUnsupported
}

func pcmFixture(audioBefore, silence, audioAfter time.Duration) []byte {
	var out bytes.Buffer
	writePCMFixtureTone(&out, audioBefore)
	writePCMFixtureSilence(&out, silence)
	writePCMFixtureTone(&out, audioAfter)
	return out.Bytes()
}

func writePCMFixtureTone(out *bytes.Buffer, dur time.Duration) {
	samples := int(dur * time.Duration(audiobookDropoutSampleRate) / time.Second)
	for i := 0; i < samples; i++ {
		v := int16(1000)
		_ = out.WriteByte(byte(v))
		_ = out.WriteByte(byte(uint16(v) >> 8))
	}
}

func writePCMFixtureSilence(out *bytes.Buffer, dur time.Duration) {
	samples := int(dur * time.Duration(audiobookDropoutSampleRate) / time.Second)
	for i := 0; i < samples; i++ {
		_ = out.WriteByte(0)
		_ = out.WriteByte(0)
	}
}
