---
slug: code/internal/tts
title: Package internal/tts
description: Auto-generated go doc reference for the internal/tts package.
---

# Package `internal/tts`

_Generated with `go doc -all ./internal/tts`. Regenerate with `scripts/gen_go_docs.sh`._

```text
package tts // import "github.com/sirily11/debate-bot/internal/tts"


CONSTANTS

const OutputFormatMP3_24k48 = "audio-24khz-48kbitrate-mono-mp3"
    OutputFormatMP3_24k48 is the chunked-streamable mp3 format used throughout
    the project. Same codec for every turn means ffmpeg concat -c copy works.


VARIABLES

var ErrSSMLUnsupported = errors.New("tts: provider does not support raw SSML")
    ErrSSMLUnsupported is returned by SynthesizeSSML on providers that don't
    accept raw SSML input. Callers MUST fall back to plain-text SynthesizeStream
    with a single voice when they see this — the multi-voice feature gracefully
    degrades instead of failing the turn.


FUNCTIONS

func BuildMultiVoiceSSML(parts []VoicePart, lang string) string
    BuildMultiVoiceSSML emits an Azure SSML envelope with one <voice> element
    per part. Adjacent parts with the same Voice (e.g. ["narrator said",
    "narrator", "well, ", "alice"] → narrator+narrator collapse) are merged.
    Empty-text parts are dropped. Empty Voice falls back to the Azure default.
    Empty parts list returns "".

func BuildSSML(voice, text, lang string) string
    BuildSSML produces an Azure TTS SSML envelope for one voice + plain text
    body. Lang defaults to en-US when empty.

func BuildSSMLNodes(voice string, nodes []SpeechNode, lang string) string
    BuildSSMLNodes produces an Azure TTS SSML envelope for one voice plus
    structured text/break nodes. Lang defaults to en-US when empty.


TYPES

type AzureClient struct {
	// Has unexported fields.
}
    AzureClient is an Azure TTS REST client.

func NewAzure(region, key string) *AzureClient
    NewAzure constructs an AzureClient.

func (c *AzureClient) FetchVoices(ctx context.Context, language string) ([]Voice, error)
    FetchVoices retrieves the full list of available Azure neural voices.
    The `language` argument is ignored — Azure exposes one global list and the
    agent voice picker filters by locale. It is part of the Provider interface
    for parity with ElevenLabs which uses the hint to tag returned voices.

func (c *AzureClient) SynthesizeSSML(ctx context.Context, ssml string) (io.ReadCloser, error)
    SynthesizeSSML POSTs a caller-supplied SSML envelope verbatim. Lets the
    pipeline emit Azure's multi-voice SSML (one <speak> with several <voice>
    elements) so a series episode's narrator + character lines render in a
    single TTS call.

func (c *AzureClient) SynthesizeStream(ctx context.Context, voice, text, lang string) (io.ReadCloser, error)
    SynthesizeStream POSTs SSML for `text` and returns the chunked MP3 body.
    The caller MUST Close the returned reader.

type ElevenLabsClient struct {
	// Has unexported fields.
}
    ElevenLabsClient is an ElevenLabs TTS REST client.

    Output is transcoded on the fly to Azure's audio-24khz-48kbitrate-mono-mp3
    format so the rest of the pipeline (LiveStream pacing, ConcatToMP3 with `-c
    copy`, AudioBytesPerSec subtitle alignment) keeps working without a branch
    per provider.

func NewElevenLabs(apiKey string) *ElevenLabsClient
    NewElevenLabs constructs an ElevenLabsClient.

func (c *ElevenLabsClient) FetchVoices(ctx context.Context, language string) ([]Voice, error)
    FetchVoices lists ElevenLabs voices. Returned voices have `Locale` set
    to the topic `language` because eleven_multilingual_v2 voices are not
    locale-bound; tagging them this way lets the existing voice picker treat
    them as eligible without provider-specific code.

func (c *ElevenLabsClient) SynthesizeSSML(_ context.Context, _ string) (io.ReadCloser, error)
    SynthesizeSSML is not supported by ElevenLabs — its REST API consumes
    plain text, not SSML. Callers wanting multi-voice synthesis must check for
    ErrSSMLUnsupported and fall back to single-voice SynthesizeStream.

func (c *ElevenLabsClient) SynthesizeStream(ctx context.Context, voiceID, text, lang string) (io.ReadCloser, error)
    SynthesizeStream POSTs `text` to the ElevenLabs streaming endpoint and
    returns a reader yielding MP3 bytes in Azure's 24kHz/48kbps/mono format.

    Internally:
     1. Request `output_format=pcm_24000` so we get raw 16-bit signed LE PCM at
        24 kHz mono — matching Azure's sample rate exactly.
     2. Pipe that PCM through ffmpeg to MP3 at 48 kbps mono. Single lossy
        encode (vs. mp3->mp3 transcode) and the final byte stream is bit-rate
        compatible with the rest of the pipeline.

type Provider interface {
	// FetchVoices lists voices the provider can render. The `language` hint
	// (e.g. "en-US", "zh-CN") may be used by the provider to tag returned
	// voices' Locale so the agent voice picker treats them as eligible —
	// useful for multilingual providers like ElevenLabs.
	FetchVoices(ctx context.Context, language string) ([]Voice, error)

	// SynthesizeStream returns a chunked MP3 reader for `text` rendered with
	// `voice` in `lang`. The caller MUST Close the returned reader.
	SynthesizeStream(ctx context.Context, voice, text, lang string) (io.ReadCloser, error)

	// SynthesizeSSML synthesises a fully-formed SSML envelope (caller is
	// responsible for building it — see BuildMultiVoiceSSML). Returns
	// ErrSSMLUnsupported on backends that don't expose raw SSML input.
	// The caller MUST Close the returned reader on success.
	SynthesizeSSML(ctx context.Context, ssml string) (io.ReadCloser, error)
}
    Provider is the abstraction every TTS backend (Azure, ElevenLabs, ...)
    satisfies. Implementations MUST return MP3 byte streams in the same format
    (audio-24khz-48kbitrate-mono-mp3) so the downstream LiveStream pacing,
    per-turn concat with `ffmpeg -c copy`, and AudioBytesPerSec subtitle
    alignment work without provider-specific branches.

type SpeechNode struct {
	Text    string
	BreakMS int
}
    SpeechNode is one renderable SSML child. Text nodes are XML-escaped; break
    nodes are emitted as Azure/W3C <break> tags. A VoicePart may use either Text
    or Nodes. Nodes are preferred when present.

type Voice struct {
	ShortName  string   `json:"ShortName"`
	Locale     string   `json:"Locale"`
	Gender     string   `json:"Gender"`
	VoiceType  string   `json:"VoiceType"`
	StyleList  []string `json:"StyleList,omitempty"`
	LocaleName string   `json:"LocaleName,omitempty"`
}
    Voice describes a single TTS voice exposed by a Provider. The shape is
    modelled on Azure's neural voice metadata; ElevenLabs voices are mapped
    onto the same struct (ShortName = voice_id, Locale set to the topic language
    since the multilingual model handles every locale).

type VoicePart struct {
	Voice string
	Text  string
	Nodes []SpeechNode
}
    VoicePart is one contiguous span of a multi-voice SSML utterance:
    the Azure neural voice ShortName and the plain-text body it should read.
    Adjacent parts with the same Voice are coalesced by BuildMultiVoiceSSML so a
    sentence with no voice switches collapses to a single <voice> element.
```
