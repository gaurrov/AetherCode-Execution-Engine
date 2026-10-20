package worker

import (
	"strings"
	"testing"

	"github.com/stjosephsplacements/AetherCode-Execution-Engine/internal/model"
)

func TestVerdictResults_AlwaysIncludesFullOutput(t *testing.T) {
	out := strings.Repeat("o", 300)
	got := verdictResults([]model.TestResult{{
		TestIndex:     0,
		Verdict:       model.VerdictAccepted,
		StdoutPreview: out,
		StderrPreview: "warning",
	}})
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	e := got[0]
	if s, _ := e["stdout"].(string); len(s) != 300 {
		t.Errorf("stdout length = %d, want 300", len(s))
	}
	if s, _ := e["stdout_preview"].(string); len(s) != 256 {
		t.Errorf("stdout_preview length = %d, want 256", len(s))
	}
	if s, _ := e["stderr"].(string); s != "warning" {
		t.Errorf("stderr = %q, want warning", s)
	}
	if s, _ := e["stderr_preview"].(string); s != "warning" {
		t.Errorf("stderr_preview = %q, want warning", s)
	}
}
