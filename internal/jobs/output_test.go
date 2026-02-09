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

func TestParseStructuredOutput_SelectsLastValidCandidate(t *testing.T) {
	raw := `{"hello":"world"}` + "\n" +
		`{"version":"v1","task_summary":"final","key_findings":["x"],"artifacts":[],"risks":[],"next_steps":[],"full_report_markdown":"ok"}`
	out, err := parseStructuredOutput(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.TaskSummary != "final" {
		t.Fatalf("expected final candidate, got %q", out.TaskSummary)
	}
}

func TestParseStructuredOutput_FillsOptionalDefaults(t *testing.T) {
	raw := `{"task_summary":"ok","key_findings":["a"]}`
	out, err := parseStructuredOutput(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Version != "v1" {
		t.Fatalf("expected default version v1, got %q", out.Version)
	}
	if out.FullReportMarkdown == "" {
		t.Fatalf("expected full_report_markdown default to be filled")
	}
	if out.Risks == nil || out.NextSteps == nil || out.Artifacts == nil {
		t.Fatalf("expected optional collections to be initialized")
	}
}

func TestCountJSONCandidates(t *testing.T) {
	raw := `{"a":1}` + "\n" + `{"b":2}`
	if got := countJSONCandidates(raw); got != 2 {
		t.Fatalf("expected 2 candidates, got %d", got)
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

func TestClassifyStructuredParseFailure(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		output string
		want   string
	}{
		{name: "empty output", err: nil, output: "   ", want: "salida_vacia"},
		{name: "missing fields", err: errString("missing key_findings"), output: "{}", want: "campos_requeridos_faltantes"},
		{name: "invalid json", err: errString("invalid character"), output: "not json", want: "json_invalido"},
		{name: "no valid candidate", err: errString("no valid json candidate among 2 objects"), output: "{\"a\":1}", want: "json_final_no_valido"},
		{name: "schema", err: errString("schema mismatch"), output: "{}", want: "schema_no_cumplido"},
		{name: "unknown", err: errString("whatever"), output: "x", want: "formato_desconocido"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := classifyStructuredParseFailure(tt.err, tt.output)
			if got != tt.want {
				t.Fatalf("expected %q got %q", tt.want, got)
			}
		})
	}
}

type errString string

func (e errString) Error() string { return string(e) }
