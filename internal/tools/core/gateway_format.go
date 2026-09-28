package core

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Formatting methods
//
// conduit-31jg.71: these used to type-assert shapes the gateway never
// produces (uptime as time.Duration, channel info as a map, config
// "providers" count only, ...), so Content came out as little more than a
// heading and the model had to read the Data JSON. They now render the real
// shapes returned by internal/gateway/status_ops.go.

func (t *GatewayTool) formatGatewayStatus(status map[string]interface{}) string {
	var builder strings.Builder
	builder.WriteString("Gateway Status:\n")
	writeKeyValues(&builder, status, "  ", 0)
	return builder.String()
}

// formatChannelStatus renders map[channelID]channels.ChannelStatus (a
// struct: status, message, details, timestamp) or plain maps of the same
// fields; values are normalized through JSON so either shape works.
func (t *GatewayTool) formatChannelStatus(channels map[string]interface{}) string {
	if len(channels) == 0 {
		return "No channels configured."
	}

	var builder strings.Builder
	builder.WriteString(fmt.Sprintf("Channel Status (%d):\n", len(channels)))

	for _, channelID := range sortedKeys(channels) {
		info := toGenericMap(channels[channelID])
		line := fmt.Sprintf("- %s", channelID)
		if st := scalarString(info["status"]); st != "" {
			line += ": " + st
		}
		if msg := scalarString(info["message"]); msg != "" {
			line += " (" + msg + ")"
		}
		builder.WriteString(line + "\n")
		if details, ok := info["details"].(map[string]interface{}); ok && len(details) > 0 {
			writeKeyValues(&builder, details, "    ", 1)
		}
	}

	return builder.String()
}

// formatConfiguration renders the redacted config (conduit-31jg.56:
// {"ai": ..., "workspace": ...} after config.Redacted). It must only ever be
// given redacted data: after a summary it appends the full redacted JSON so
// the model does not need Data for details.
func (t *GatewayTool) formatConfiguration(config map[string]interface{}) string {
	var builder strings.Builder
	builder.WriteString("Gateway Configuration (secrets redacted):\n")

	if ai, ok := config["ai"].(map[string]interface{}); ok {
		builder.WriteString("AI:\n")
		if v := scalarString(ai["default_provider"]); v != "" {
			builder.WriteString(fmt.Sprintf("  Default provider: %s\n", v))
		}
		if providers, ok := ai["providers"].([]interface{}); ok {
			builder.WriteString(fmt.Sprintf("  Providers (%d):\n", len(providers)))
			for _, p := range providers {
				pm, ok := p.(map[string]interface{})
				if !ok {
					continue
				}
				line := fmt.Sprintf("    - %s", scalarString(pm["name"]))
				if typ := scalarString(pm["type"]); typ != "" {
					line += " (" + typ + ")"
				}
				if model := scalarString(pm["model"]); model != "" {
					line += " model=" + model
				}
				if fb := scalarString(pm["fallback_model"]); fb != "" {
					line += " fallback=" + fb
				}
				if cw := scalarString(pm["context_window"]); cw != "" && cw != "0" {
					line += " context_window=" + cw
				}
				if auth, ok := pm["auth"].(map[string]interface{}); ok {
					if at := scalarString(auth["type"]); at != "" {
						line += " auth=" + at
					}
				}
				builder.WriteString(line + "\n")
			}
		}
		if v := scalarString(ai["subagent_default_model"]); v != "" {
			builder.WriteString(fmt.Sprintf("  Sub-agent default model: %s\n", v))
		}
		if v := scalarString(ai["max_tokens"]); v != "" && v != "0" {
			builder.WriteString(fmt.Sprintf("  Max output tokens: %s\n", v))
		}
		if aliases, ok := ai["model_aliases"].(map[string]interface{}); ok && len(aliases) > 0 {
			parts := make([]string, 0, len(aliases))
			for _, k := range sortedKeys(aliases) {
				parts = append(parts, k+"="+scalarString(aliases[k]))
			}
			builder.WriteString(fmt.Sprintf("  Model aliases: %s\n", strings.Join(parts, ", ")))
		}
	}

	if ws, ok := config["workspace"].(map[string]interface{}); ok {
		builder.WriteString("Workspace:\n")
		if v := scalarString(ws["context_dir"]); v != "" {
			builder.WriteString(fmt.Sprintf("  Context dir: %s\n", v))
		}
	}

	if raw, err := json.Marshal(config); err == nil {
		builder.WriteString("\nFull configuration (redacted JSON): ")
		builder.Write(raw)
		builder.WriteString("\n")
	}

	return builder.String()
}

func (t *GatewayTool) formatMetrics(metrics map[string]interface{}) string {
	var builder strings.Builder
	builder.WriteString("Gateway Metrics:\n")
	if len(metrics) == 0 {
		builder.WriteString("  (none reported)\n")
		return builder.String()
	}
	writeKeyValues(&builder, metrics, "  ", 0)
	return builder.String()
}

// writeKeyValues writes m as sorted "key: value" lines. Nested maps are
// indented (up to maxNestedDepth); lists of scalars are joined, longer or
// structured lists are summarized by length. conduit-31jg.71
func writeKeyValues(b *strings.Builder, m map[string]interface{}, indent string, depth int) {
	const maxNestedDepth = 2
	for _, k := range sortedKeys(m) {
		v := m[k]
		switch val := normalizeValue(v).(type) {
		case map[string]interface{}:
			if depth >= maxNestedDepth || len(val) == 0 {
				b.WriteString(fmt.Sprintf("%s%s: {%d fields}\n", indent, k, len(val)))
				continue
			}
			b.WriteString(fmt.Sprintf("%s%s:\n", indent, k))
			writeKeyValues(b, val, indent+"  ", depth+1)
		case []interface{}:
			b.WriteString(fmt.Sprintf("%s%s: %s\n", indent, k, summarizeList(val)))
		default:
			b.WriteString(fmt.Sprintf("%s%s: %s\n", indent, k, scalarString(val)))
		}
	}
}

// normalizeValue converts structs and typed maps/slices to the generic
// map/slice shapes via JSON; scalars and time values pass through.
func normalizeValue(v interface{}) interface{} {
	switch v.(type) {
	case nil, string, bool, int, int32, int64, uint, uint32, uint64, float32, float64,
		time.Time, time.Duration, map[string]interface{}, []interface{}:
		return v
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	var out interface{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return string(raw)
	}
	return out
}

// toGenericMap returns v as map[string]interface{} (via JSON for structs),
// or nil.
func toGenericMap(v interface{}) map[string]interface{} {
	m, _ := normalizeValue(v).(map[string]interface{})
	return m
}

func summarizeList(list []interface{}) string {
	const maxItems = 10
	parts := make([]string, 0, len(list))
	for i, item := range list {
		switch item.(type) {
		case map[string]interface{}, []interface{}:
			return fmt.Sprintf("[%d items]", len(list))
		}
		if i == maxItems {
			parts = append(parts, fmt.Sprintf("... (+%d more)", len(list)-maxItems))
			break
		}
		parts = append(parts, scalarString(item))
	}
	return strings.Join(parts, ", ")
}

// scalarString formats a scalar for display; JSON numbers that are whole
// render without a decimal point.
func scalarString(v interface{}) string {
	switch val := v.(type) {
	case nil:
		return ""
	case string:
		return val
	case float64:
		if val == float64(int64(val)) {
			return fmt.Sprintf("%d", int64(val))
		}
		return fmt.Sprintf("%g", val)
	case time.Time:
		if val.IsZero() {
			return ""
		}
		return val.Format(time.RFC3339)
	default:
		return fmt.Sprintf("%v", val)
	}
}

func sortedKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func (t *GatewayTool) formatPromptDebug(data map[string]interface{}) string {
	var builder strings.Builder
	builder.WriteString("System Prompt Debug:\n\n")

	if totalChars, ok := data["total_chars"].(int); ok {
		builder.WriteString(fmt.Sprintf("Total chars:        %d\n", totalChars))
	}
	if estTokens, ok := data["estimated_tokens"].(int); ok {
		builder.WriteString(fmt.Sprintf("Estimated tokens:   %d\n", estTokens))
	}
	if ctxWindow, ok := data["context_window"].(int); ok {
		builder.WriteString(fmt.Sprintf("Context window:     %d\n", ctxWindow))
	}
	if budgetChars, ok := data["budget_chars"].(int); ok {
		builder.WriteString(fmt.Sprintf("Budget (chars):     %d\n", budgetChars))
	}
	if constrained, ok := data["budget_constrained"].(bool); ok {
		builder.WriteString(fmt.Sprintf("Budget constrained: %v\n", constrained))
	}

	builder.WriteString("\nSections:\n")
	builder.WriteString(fmt.Sprintf("  %-25s %4s %7s %s\n", "Name", "Pri", "Chars", "Status"))
	builder.WriteString(fmt.Sprintf("  %-25s %4s %7s %s\n", "----", "---", "-----", "------"))

	if sections, ok := data["sections"].([]map[string]interface{}); ok {
		for _, s := range sections {
			name, _ := s["name"].(string)
			priority, _ := s["priority"].(int)
			chars, _ := s["chars"].(int)
			included, _ := s["included"].(bool)
			status := "included"
			if !included {
				status = "DROPPED"
			}
			builder.WriteString(fmt.Sprintf("  %-25s P%-3d %7d %s\n", name, priority, chars, status))
		}
	}

	if dropped, ok := data["dropped_sections"].([]string); ok && len(dropped) > 0 {
		builder.WriteString(fmt.Sprintf("\nDropped sections: %s\n", strings.Join(dropped, ", ")))
	}
	// The full prompt_text stays in Data only (it can be tens of KB and is
	// not needed to judge the budget). conduit-31jg.71

	return builder.String()
}
