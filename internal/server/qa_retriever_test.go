package server

import "testing"

func TestQAPodcastInfoIncludesRenderableCover(t *testing.T) {
	discussion := &Discussion{
		ID:    "podcast-1",
		Title: "Covered episode",
		Cover: DiscussionCover{
			Type:          "image",
			ImageURL:      "https://signed.example/cover.webp",
			ImageKey:      "private/cover.webp",
			GradientStart: "#112233",
			GradientEnd:   "#445566",
			Prompt:        "private generation prompt",
		},
	}
	info := qaPodcastInfo(discussion)
	if info.Cover == nil || info.Cover.ImageURL != discussion.Cover.ImageURL {
		t.Fatalf("cover missing from Q&A podcast payload: %+v", info.Cover)
	}
	if info.Cover.Type != "image" || info.Cover.GradientStart != "#112233" || info.Cover.GradientEnd != "#445566" {
		t.Fatalf("cover presentation fields = %+v", info.Cover)
	}
}

func TestQAPodcastInfoOmitsInvalidCover(t *testing.T) {
	info := qaPodcastInfo(&Discussion{ID: "podcast-2", Title: "No cover"})
	if info.Cover != nil {
		t.Fatalf("invalid cover leaked into Q&A payload: %+v", info.Cover)
	}
}
