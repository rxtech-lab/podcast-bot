package contentcreator

import (
	"reflect"
	"testing"
)

func TestStripImageRefMarkers_NoMarker(t *testing.T) {
	clean, lead, trail := stripImageRefMarkers("hello world.")
	if clean != "hello world." {
		t.Errorf("clean = %q, want unchanged", clean)
	}
	if len(lead) != 0 || len(trail) != 0 {
		t.Errorf("expected no markers; lead=%v trail=%v", lead, trail)
	}
}

func TestStripImageRefMarkers_Leading(t *testing.T) {
	clean, lead, trail := stripImageRefMarkers("<season-1-episode-3-image-7/> the alley was quiet.")
	if clean != "the alley was quiet." {
		t.Errorf("clean = %q", clean)
	}
	if !reflect.DeepEqual(lead, []string{"s1e3i7"}) {
		t.Errorf("lead = %v, want [s1e3i7]", lead)
	}
	if len(trail) != 0 {
		t.Errorf("trail should be empty; got %v", trail)
	}
}

func TestStripImageRefMarkers_Trailing(t *testing.T) {
	clean, lead, trail := stripImageRefMarkers("the rain returned. <season-2-episode-1-image-0/>")
	if clean != "the rain returned." {
		t.Errorf("clean = %q", clean)
	}
	if !reflect.DeepEqual(trail, []string{"s2e1i0"}) {
		t.Errorf("trail = %v, want [s2e1i0]", trail)
	}
	if len(lead) != 0 {
		t.Errorf("lead should be empty; got %v", lead)
	}
}

func TestStripImageRefMarkers_BracketedAndCaseInsensitive(t *testing.T) {
	clean, lead, _ := stripImageRefMarkers("[SEASON-1-EPISODE-2-IMAGE-3] mid sentence.")
	if clean != "mid sentence." {
		t.Errorf("clean = %q", clean)
	}
	if !reflect.DeepEqual(lead, []string{"s1e2i3"}) {
		t.Errorf("lead = %v, want [s1e2i3]", lead)
	}
}

func TestStripImageRefMarkers_MultipleLeading(t *testing.T) {
	clean, lead, _ := stripImageRefMarkers("<season-1-episode-1-image-0/> <season-1-episode-1-image-1/> opening.")
	if clean != "opening." {
		t.Errorf("clean = %q", clean)
	}
	if !reflect.DeepEqual(lead, []string{"s1e1i0", "s1e1i1"}) {
		t.Errorf("lead = %v", lead)
	}
}
