# Style-test font (not committed)

`TestStyleGolden` (see `../../style_golden_test.go`) renders its golden frames
with a pinned CJK font so the snapshots are byte-for-byte reproducible across
machines. The font binary is **not** committed — it's an 8 MB blob fetched on
demand by `scripts/fetch-style-font.sh` (run automatically by `make style-test`
and `make style-font`) and ignored by git.

| | |
|---|---|
| File | `NotoSansSC-Regular.otf` |
| Source | Noto Sans CJK SC, region subset OTF |
| Pinned tag | `Sans2.004` ([googlefonts/noto-cjk](https://github.com/googlefonts/noto-cjk)) |
| SHA-256 | `faa6c9df652116dde789d351359f3d7e5d2285a2b2a1f04a2d7244df706d5ea9` |
| License | SIL Open Font License 1.1 ([LICENSE](https://github.com/googlefonts/noto-cjk/blob/Sans2.004/Sans/LICENSE)) |

To change the font: update the tag + SHA-256 in `scripts/fetch-style-font.sh`,
then regenerate the goldens with `make style-golden` and review the diff.
