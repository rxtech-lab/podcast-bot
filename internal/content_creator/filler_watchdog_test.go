package contentcreator

import "testing"

func TestFillerWatchdogTripsOnSignOffLoop(t *testing.T) {
	w := &fillerWatchdog{}
	if w.Observe("盖尔走出法庭时，感觉到了一种前所未有的沉重，但也有一丝微弱的希望。") {
		t.Fatalf("normal narration tripped the watchdog")
	}
	loop := []string{
		"导演旁白：本集结束。",
		"导演旁白：感谢收听。",
		"导演旁白：我们下集再见。",
		"导演旁白：制作完成。",
		"导演旁白：再见。",
		"导演旁白：本章节结束。",
	}
	for i, line := range loop {
		tripped := w.Observe(line)
		if i < fillerRunLimit-1 && tripped {
			t.Fatalf("watchdog tripped after only %d filler lines", i+1)
		}
		if i == fillerRunLimit-1 && !tripped {
			t.Fatalf("watchdog did not trip after %d consecutive sign-off lines", fillerRunLimit)
		}
	}
}

func TestFillerWatchdogResetsOnRealNarration(t *testing.T) {
	w := &fillerWatchdog{}
	for i := 0; i < fillerRunLimit-1; i++ {
		if w.Observe("导演旁白：本集结束。") && i < fillerRunLimit-2 {
			t.Fatalf("tripped before the limit")
		}
	}
	if w.Observe("谢顿在审判台上表现得波澜不惊，他挺直了脊梁，用真理向腐朽的政权发起最后挑战，旁听席上的众人屏住呼吸，静静注视着这位老人的身影。") {
		t.Fatalf("long narration counted as filler")
	}
	if w.Observe("导演旁白：再见。") {
		t.Fatalf("run did not reset after real narration")
	}
}

func TestFillerWatchdogTripsOnDuplicateShortLines(t *testing.T) {
	w := &fillerWatchdog{}
	tripped := false
	for i := 0; i < fillerRunLimit+1; i++ {
		if w.Observe("他沉默地凝视着远方的一切事物") {
			tripped = true
			break
		}
	}
	if !tripped {
		t.Fatalf("repeated identical short sentences did not trip the watchdog")
	}
}

func TestFillerWatchdogIgnoresMarkupOnlyChunks(t *testing.T) {
	w := &fillerWatchdog{}
	for i := 0; i < fillerRunLimit*2; i++ {
		if w.Observe(`<pause time="2000ms"/>`) {
			t.Fatalf("markup-only chunk counted as filler")
		}
	}
	var nilW *fillerWatchdog
	if nilW.Observe("导演旁白：本集结束。") {
		t.Fatalf("nil watchdog must never trip")
	}
}
