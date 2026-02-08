package jobs

import "testing"

func TestParseStructuredOutput_DirectJSON(t *testing.T) {
	raw := `{"version":"v1","task_summary":"ok","key_findings":["a"],"artifacts":[{"path":"README.md","description":"updated"}],"risks":[],"next_steps":["review"],"full_report_markdown":"# Report"}`
	out, err := parseStructuredOutput(raw)
	if err != nil {
		t.Fatalf("parseStructuredOutput returned error: %v", err)
	}
	if out.Version != "v1" {
		t.Fatalf("expected version v1, got %q", out.Version)
	}
	if out.TaskSummary != "ok" {
		t.Fatalf("unexpected task_summary: %q", out.TaskSummary)
	}
}

func TestParseStructuredOutput_ExtractJSONFromNoise(t *testing.T) {
	raw := "header noise\n" +
		`{"version":"v1","task_summary":"ok","key_findings":["a"],"artifacts":[],"risks":[],"next_steps":[],"full_report_markdown":"report"}` +
		"\nfooter noise"
	out, err := parseStructuredOutput(raw)
	if err != nil {
		t.Fatalf("parseStructuredOutput returned error: %v", err)
	}
	if out.TaskSummary != "ok" {
		t.Fatalf("unexpected task_summary: %q", out.TaskSummary)
	}
}

func TestParseStructuredOutput_MissingRequiredField(t *testing.T) {
	raw := `{"version":"v1","task_summary":"","key_findings":["a"],"artifacts":[],"risks":[],"next_steps":[],"full_report_markdown":"report"}`
	if _, err := parseStructuredOutput(raw); err == nil {
		t.Fatalf("expected validation error")
	}
}

func TestSanitizeOutputForSlack(t *testing.T) {
	in := "[stderr] one\n[stderr]   \n[stderr] two\n\n\nthree"
	got := sanitizeOutputForSlack(in, 0)
	want := "one\n\ntwo\n\nthree"
	if got != want {
		t.Fatalf("unexpected sanitize output.\nwant: %q\ngot:  %q", want, got)
	}
}

func TestExtractErrorTail(t *testing.T) {
	in := "a\nb\nc\nd"
	got := extractErrorTail(in, 2, 0)
	if got != "c\nd" {
		t.Fatalf("expected tail lines, got %q", got)
	}
}
