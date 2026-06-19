package main

import "testing"

func TestServerCmdRejectsInvalidMaxConcurrency(t *testing.T) {
	if code := serverCmd([]string{"--mode=video", "--max-concurrency=0"}); code != 2 {
		t.Fatalf("serverCmd exit = %d, want 2", code)
	}
}
