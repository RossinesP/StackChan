/*
SPDX-FileCopyrightText: 2026 M5Stack Technology CO LTD
SPDX-License-Identifier: MIT
*/

package mistral_gateway

import "encoding/json"

// MistralTool is the OpenAI-shaped function tool spec that Mistral's
// chat completions API accepts in the `tools` array.
//
// The mapping from MCP is direct:
//   MCP.name        → function.name        (kept verbatim, including dots)
//   MCP.description → function.description
//   MCP.inputSchema → function.parameters  (raw JSON, no re-marshal)
type MistralTool struct {
	Type     string              `json:"type"` // always "function"
	Function MistralToolFunction `json:"function"`
}

type MistralToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// MapMCPToolsToMistral converts the device's discovered MCP tools
// into the format Mistral chat completions expects in `tools`.
//
// Tools with empty inputSchema get a permissive empty-object schema
// inserted ({"type":"object","properties":{}}) — Mistral rejects
// function defs without a parameters object even when the function
// takes no arguments.
func MapMCPToolsToMistral(tools []Tool) []MistralTool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]MistralTool, 0, len(tools))
	for _, t := range tools {
		params := t.InputSchema
		if len(params) == 0 {
			params = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		out = append(out, MistralTool{
			Type: "function",
			Function: MistralToolFunction{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  params,
			},
		})
	}
	return out
}

// FilterTools drops any MCP tool whose name appears in `blocked`.
// Used to hide tools the gateway can't fully support yet — see
// Config.ChatToolBlocklist for the rationale.
func FilterTools(tools []Tool, blocked []string) []Tool {
	if len(tools) == 0 || len(blocked) == 0 {
		return tools
	}
	blockSet := make(map[string]struct{}, len(blocked))
	for _, n := range blocked {
		blockSet[n] = struct{}{}
	}
	out := make([]Tool, 0, len(tools))
	for _, t := range tools {
		if _, drop := blockSet[t.Name]; drop {
			continue
		}
		out = append(out, t)
	}
	return out
}
