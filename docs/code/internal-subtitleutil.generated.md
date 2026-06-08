---
slug: code/internal/subtitleutil
title: Package internal/subtitleutil
description: Auto-generated go doc reference for the internal/subtitleutil package.
---

# Package `internal/subtitleutil`

_Generated with `go doc -all ./internal/subtitleutil`. Regenerate with `scripts/gen_go_docs.sh`._

```text
package subtitleutil // import "github.com/sirily11/debate-bot/internal/subtitleutil"

Package subtitleutil holds rendering-agnostic helpers shared between the
in-frame caption renderer (internal/video) and the sidecar WebVTT writer
(internal/content_creator). Living in its own leaf package avoids the cycle that
would arise if either of those imported the other.

FUNCTIONS

func IsWordRune(r rune) bool
    IsWordRune is the exported form of isWordRune. Re-exported because
    internal/video's caption layout uses the same predicate to compute per-line
    "weight" (count of content runes) for weighted scrolling, and that path
    needs to stay byte-identical with the strip pass to keep the rendered
    caption layout aligned with the stripped text.

func StripPunct(s string) string
    StripPunct removes punctuation, pause indicators and stray symbols from
    a subtitle body so the rendered caption shows only the readable words.
    Targets:
      - CJK fullstop / comma / pauses: 。 ， 、 ； ： ！ ？ 「 」 『 』 （ ）《 》 【 】
      - CJK pause/ellipsis sequences: …… —— ···
      - Latin punctuation: . , ; : ! ? — - … " ' ( ) [ ] { }

    Stripping happens before line wrapping so a residue line that would
    otherwise contain only "。" or ", " disappears from the display entirely.
    Letters / digits / CJK glyphs are kept verbatim. Whitespace is collapsed to
    a single space so wrappers still have word boundaries for Latin text.

func WrapByRunes(text string, maxRunes int) []string
    WrapByRunes splits text into chunks of at most maxRunes runes, preferring a
    break at the last whitespace seen on the current chunk (matching wrapLines'
    Latin-friendly behavior in internal/video). When no whitespace is available
    — typical for CJK passages whose punctuation has already been stripped
    to spaces by StripPunct, then trimmed — the split falls back to a hard
    rune-count cut so a single long Chinese sentence still gets broken into
    chunks instead of overflowing one cue.

    Used by the WebVTT writer to mirror what the burned-in caption renderer does
    (one wrapped line at a time, scrolled in lockstep with the spoken audio):
    each chunk becomes its own cue with a slice of the total audio duration
    weighted by its rune count.
```
