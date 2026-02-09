package jobs

import (
	_ "embed"
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

//go:embed schema/structured_output_v1.json
var structuredOutputSchemaV1 []byte

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
	b.WriteString("Contexto de salida estructurada: schema ")
	b.WriteString(strings.TrimSpace(schemaVersion))
	b.WriteString(".\n")
	if retry {
		b.WriteString("Intento de correccion: la salida anterior no fue parseable. Devuelve una respuesta final coherente y completa.\n")
	}
	b.WriteString("Objetivo:\n")
	b.WriteString(strings.TrimSpace(userPrompt))
	if strings.TrimSpace(lastInput) != "" {
		b.WriteString("\nContexto adicional:\n")
		b.WriteString(strings.TrimSpace(lastInput))
	}
	return b.String()
}

func outputSchemaForVersion(version string) ([]byte, error) {
	switch strings.TrimSpace(version) {
	case "", "v1":
		return structuredOutputSchemaV1, nil
	default:
		return nil, fmt.Errorf("unsupported schema version: %s", version)
	}
}

func parseStructuredOutput(raw string) (structuredOutput, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return structuredOutput{}, fmt.Errorf("empty output")
	}
	var out structuredOutput
	if err := json.Unmarshal([]byte(raw), &out); err == nil {
		out = normalizeStructuredOutput(out)
		if vErr := validateStructuredOutput(out); vErr == nil {
			return out, nil
		}
	}
	candidates := extractJSONObjects(raw)
	if len(candidates) == 0 {
		return structuredOutput{}, fmt.Errorf("no json object found")
	}
	var lastErr error
	for i := len(candidates) - 1; i >= 0; i-- {
		if err := json.Unmarshal([]byte(candidates[i]), &out); err != nil {
			lastErr = err
			continue
		}
		out = normalizeStructuredOutput(out)
		if err := validateStructuredOutput(out); err != nil {
			lastErr = err
			continue
		}
		return out, nil
	}
	if lastErr != nil {
		return structuredOutput{}, fmt.Errorf("no valid json candidate among %d objects: %w", len(candidates), lastErr)
	}
	return structuredOutput{}, fmt.Errorf("no valid json candidate among %d objects", len(candidates))
}

func validateStructuredOutput(v structuredOutput) error {
	if strings.TrimSpace(v.TaskSummary) == "" {
		return fmt.Errorf("missing task_summary")
	}
	if len(v.KeyFindings) == 0 {
		return fmt.Errorf("missing key_findings")
	}
	return nil
}

func normalizeStructuredOutput(v structuredOutput) structuredOutput {
	if strings.TrimSpace(v.Version) == "" {
		v.Version = "v1"
	}
	if v.Artifacts == nil {
		v.Artifacts = make([]struct {
			Path        string `json:"path"`
			Description string `json:"description"`
		}, 0)
	}
	if v.Risks == nil {
		v.Risks = []string{}
	}
	if v.NextSteps == nil {
		v.NextSteps = []string{}
	}
	if strings.TrimSpace(v.FullReportMarkdown) == "" {
		var b strings.Builder
		b.WriteString("## Resumen\n")
		b.WriteString(strings.TrimSpace(v.TaskSummary))
		if len(v.KeyFindings) > 0 {
			b.WriteString("\n\n## Hallazgos clave")
			for _, item := range v.KeyFindings {
				item = strings.TrimSpace(item)
				if item == "" {
					continue
				}
				b.WriteString("\n- ")
				b.WriteString(item)
			}
		}
		v.FullReportMarkdown = strings.TrimSpace(b.String())
	}
	return v
}

func extractJSONObjects(s string) []string {
	objects := make([]string, 0)
	depth := 0
	inString := false
	escaped := false
	start := -1
	for i := 0; i < len(s); i++ {
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
			if depth == 0 {
				start = i
			}
			depth++
			continue
		}
		if ch == '}' {
			depth--
			if depth == 0 && start >= 0 {
				objects = append(objects, s[start:i+1])
				start = -1
			}
		}
	}
	return objects
}

func countJSONCandidates(s string) int {
	return len(extractJSONObjects(s))
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

func classifyStructuredParseFailure(err error, output string) (code, userMsg string) {
	raw := strings.TrimSpace(output)
	if raw == "" {
		return "salida_vacia", "salida vacia"
	}
	if err == nil {
		return "formato_desconocido", "formato no reconocido"
	}
	e := strings.ToLower(strings.TrimSpace(err.Error()))
	switch {
	case strings.Contains(e, "no valid json candidate") || strings.Contains(e, "no json object found"):
		return "json_final_no_valido", "respuesta final no parseable"
	case strings.Contains(e, "missing ") || strings.Contains(e, "required"):
		return "campos_requeridos_faltantes", "faltan campos requeridos"
	case strings.Contains(e, "unsupported schema") || strings.Contains(e, "schema"):
		return "schema_no_cumplido", "no cumple schema"
	case strings.Contains(e, "invalid") || strings.Contains(e, "cannot") || strings.Contains(e, "json"):
		return "json_invalido", "json invalido"
	default:
		return "formato_desconocido", "formato no reconocido"
	}
}
