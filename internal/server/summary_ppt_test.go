package server

import "testing"

func TestSummaryExportFilename(t *testing.T) {
	got := summaryExportFilename("A/B: C?", ".pptx")
	if got != "A-B- C-.pptx" {
		t.Fatalf("summaryExportFilename = %q", got)
	}
}

func TestSummaryExportFilenameFallback(t *testing.T) {
	got := summaryExportFilename("   ", "pdf")
	if got != "Summary.pdf" {
		t.Fatalf("summaryExportFilename fallback = %q", got)
	}
}
