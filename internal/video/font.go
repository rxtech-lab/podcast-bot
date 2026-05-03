package video

import (
	"fmt"
	"os"
	"strings"
)

// candidateFonts is the search order for a TTF/TTC drawtext can render. Bold
// faces look crisper at the sizes we use, but a regular face is fine.
var candidateFonts = []string{
	// macOS
	"/System/Library/Fonts/Helvetica.ttc",
	"/System/Library/Fonts/Supplemental/Arial Bold.ttf",
	"/System/Library/Fonts/Supplemental/Arial.ttf",
	"/Library/Fonts/Arial Bold.ttf",
	"/Library/Fonts/Arial.ttf",
	// Linux
	"/usr/share/fonts/truetype/dejavu/DejaVuSans-Bold.ttf",
	"/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf",
	"/usr/share/fonts/TTF/DejaVuSans-Bold.ttf",
	"/usr/share/fonts/TTF/DejaVuSans.ttf",
	"/usr/share/fonts/dejavu/DejaVuSans-Bold.ttf",
	"/usr/share/fonts/liberation/LiberationSans-Bold.ttf",
	"/usr/share/fonts/liberation-sans/LiberationSans-Bold.ttf",
	"/usr/share/fonts/google-noto/NotoSans-Regular.ttf",
	// Windows
	`C:\Windows\Fonts\arialbd.ttf`,
	`C:\Windows\Fonts\arial.ttf`,
}

// findFont returns the first usable system font. DEBATE_BOT_FONT can override.
func findFont() (string, error) {
	if env := strings.TrimSpace(os.Getenv("DEBATE_BOT_FONT")); env != "" {
		if _, err := os.Stat(env); err == nil {
			return env, nil
		}
		return "", fmt.Errorf("DEBATE_BOT_FONT=%q does not exist", env)
	}
	for _, p := range candidateFonts {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("no system font found; set DEBATE_BOT_FONT to a .ttf or .ttc path")
}
