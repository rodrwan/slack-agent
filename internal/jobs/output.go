package jobs

import (
	"encoding/json"
	"fmt"
	"strings"
)

const (
	OutputModeStructured = "structured"
	OutputModeRaw        = "raw"

	LogModeSummary = "summary"
	LogModeStream  = "stream"
)

type structuredOutput struct {
	Version     string   `json:"version"`
	TaskSummary string   `json:"task_summary"`
	KeyFindings []string `json:"key_findings"`
	Artifacts   []struct {
		Path        string `json:"path"`
		Description string `json:"description"`
	} `json:"artifacts"`
	Risks              []string `json:"risks"`
	NextSteps          []string `json:"next_steps"`
	FullReportMarkdown string   `json:"full_report_markdown"`
}

func normalizeOutputMode(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case OutputModeRaw:
		return OutputModeRaw
	default:
		return OutputModeStructured
	}
}

func normalizeLogMode(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case LogModeStream:
		return LogModeStream
	default:
		return LogModeSummary
	}
}

func buildCodexPrompt(userPrompt, lastInput, schemaVersion string, retry bool) string {
	var b strings.Builder
	b.WriteString("Responde solo con JSON valido UTF-8, sin markdown y sin texto fuera del JSON.\n")
	b.WriteString("Schema version: ")
	b.WriteString(strings.TrimSpace(schemaVersion))
	b.WriteString(".\n")
	b.WriteString("Campos requeridos: version, task_summary, key_findings, artifacts, risks, next_steps, full_report_markdown.\n")
	b.WriteString("Reglas: task_summary corto; key_findings/risk/next_steps como arreglos de strings; artifacts como arreglo de {path, description}.\n")
	if retry {
		b.WriteString("Intento de correccion: la salida anterior no cumplio el contrato. Devuelve JSON estricto.\n")
	}
	b.WriteString("Objetivo:\n")
	b.WriteString(strings.TrimSpace(userPrompt))
	if strings.TrimSpace(lastInput) != "" {
		b.WriteString("\nContexto adicional:\n")
		b.WriteString(strings.TrimSpace(lastInput))
	}
	return b.String()
}

func parseStructuredOutput(raw string) (structuredOutput, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return structuredOutput{}, fmt.Errorf("empty output")
	}
	var out structuredOutput
	if err := json.Unmarshal([]byte(raw), &out); err == nil {
		return out, validateStructuredOutput(out)
	}
	jsonBlob := extractJSONObject(raw)
	if jsonBlob == "" {
		return structuredOutput{}, fmt.Errorf("no json object found")
	}
	if err := json.Unmarshal([]byte(jsonBlob), &out); err != nil {
		return structuredOutput{}, err
	}
	return out, validateStructuredOutput(out)
}

func validateStructuredOutput(v structuredOutput) error {
	if strings.TrimSpace(v.Version) == "" {
		return fmt.Errorf("missing version")
	}
	if strings.TrimSpace(v.TaskSummary) == "" {
		return fmt.Errorf("missing task_summary")
	}
	if strings.TrimSpace(v.FullReportMarkdown) == "" {
		return fmt.Errorf("missing full_report_markdown")
	}
	if len(v.KeyFindings) == 0 {
		return fmt.Errorf("missing key_findings")
	}
	return nil
}

func extractJSONObject(s string) string {
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return ""
	}
	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(s); i++ {
		ch := s[i]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == '"' {
				inString = false
			}
			continue
		}
		if ch == '"' {
			inString = true
			continue
		}
		if ch == '{' {
			depth++
			continue
		}
		if ch == '}' {
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return ""
}

func sanitizeOutputForSlack(v string, limit int) string {
	v = strings.ReplaceAll(v, "\r\n", "\n")
	lines := strings.Split(v, "\n")
	out := make([]string, 0, len(lines))
	lastBlank := false
	for _, line := range lines {
		line = strings.TrimSpace(strings.TrimPrefix(line, "[stderr]"))
		if line == "" {
			if !lastBlank {
				out = append(out, "")
			}
			lastBlank = true
			continue
		}
		lastBlank = false
		out = append(out, line)
	}
	joined := strings.TrimSpace(strings.Join(out, "\n"))
	if limit > 0 && len(joined) > limit {
		return strings.TrimSpace(joined[:limit]) + "\n..."
	}
	return joined
}

func formatStructuredSummary(v structuredOutput) string {
	var b strings.Builder
	b.WriteString("*Resumen de Codex*\n")
	b.WriteString(v.TaskSummary)
	if len(v.KeyFindings) > 0 {
		b.WriteString("\n\n*Hallazgos clave*")
		for _, item := range v.KeyFindings {
			item = strings.TrimSpace(item)
			if item == "" {
				continue
			}
			b.WriteString("\n- ")
			b.WriteString(item)
		}
	}
	if len(v.Risks) > 0 {
		b.WriteString("\n\n*Riesgos*")
		for _, item := range v.Risks {
			item = strings.TrimSpace(item)
			if item == "" {
				continue
			}
			b.WriteString("\n- ")
			b.WriteString(item)
		}
	}
	if len(v.NextSteps) > 0 {
		b.WriteString("\n\n*Siguientes pasos*")
		for _, item := range v.NextSteps {
			item = strings.TrimSpace(item)
			if item == "" {
				continue
			}
			b.WriteString("\n- ")
			b.WriteString(item)
		}
	}
	return b.String()
}

func extractErrorTail(v string, lines int, limit int) string {
	clean := sanitizeOutputForSlack(v, 0)
	if clean == "" {
		return ""
	}
	parts := strings.Split(clean, "\n")
	if lines > 0 && len(parts) > lines {
		parts = parts[len(parts)-lines:]
	}
	out := strings.TrimSpace(strings.Join(parts, "\n"))
	if limit > 0 && len(out) > limit {
		return strings.TrimSpace(out[:limit]) + "\n..."
	}
	return out
}
