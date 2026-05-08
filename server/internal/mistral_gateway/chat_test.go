/*
SPDX-FileCopyrightText: 2026 M5Stack Technology CO LTD
SPDX-License-Identifier: MIT
*/

package mistral_gateway

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestTruncateHistoryEmpty(t *testing.T) {
	if got := truncateHistory(nil, 6); got != nil {
		t.Errorf("nil hist → %v, want nil", got)
	}
	if got := truncateHistory([]ChatMessage{}, 6); len(got) != 0 {
		t.Errorf("empty hist → len=%d, want 0", len(got))
	}
}

func TestTruncateHistoryUnderLimit(t *testing.T) {
	hist := []ChatMessage{
		{Role: RoleUser, Content: "a"},
		{Role: RoleAssistant, Content: "b"},
	}
	got := truncateHistory(hist, 6)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2 (under limit, no truncation)", len(got))
	}
	if got[0].Content != "a" || got[1].Content != "b" {
		t.Errorf("got %+v, want [a, b]", got)
	}
}

func TestTruncateHistoryOverLimit(t *testing.T) {
	// 8 messages, limit 4 → keep last 4 (latest 2 turns).
	hist := []ChatMessage{
		{Role: RoleUser, Content: "u1"},
		{Role: RoleAssistant, Content: "a1"},
		{Role: RoleUser, Content: "u2"},
		{Role: RoleAssistant, Content: "a2"},
		{Role: RoleUser, Content: "u3"},
		{Role: RoleAssistant, Content: "a3"},
		{Role: RoleUser, Content: "u4"},
		{Role: RoleAssistant, Content: "a4"},
	}
	got := truncateHistory(hist, 4)
	if len(got) != 4 {
		t.Fatalf("len = %d, want 4", len(got))
	}
	want := []string{"u3", "a3", "u4", "a4"}
	for i, w := range want {
		if got[i].Content != w {
			t.Errorf("[%d] got %q, want %q", i, got[i].Content, w)
		}
	}
}

func TestTruncateHistoryZeroLimit(t *testing.T) {
	hist := []ChatMessage{
		{Role: RoleUser, Content: "x"},
		{Role: RoleAssistant, Content: "y"},
	}
	if got := truncateHistory(hist, 0); got != nil {
		t.Errorf("limit=0 → %v, want nil (history disabled)", got)
	}
}

// TestSessionAppendTurnTruncates exercises the full turn-recording
// flow that runs after every chat reply. We can't easily call
// AppendTurn directly without configured Get(), so we drive
// truncateHistory through a session-shaped slice instead.
func TestAppendTurnShapeUserAssistantPair(t *testing.T) {
	// Simulate three turns then truncate to keep last 2 messages.
	var hist []ChatMessage
	turns := [][2]string{
		{"hi", "hello!"},
		{"how are you", "great, thanks"},
		{"goodbye", "see you"},
	}
	for _, p := range turns {
		hist = append(hist,
			ChatMessage{Role: RoleUser, Content: p[0]},
			ChatMessage{Role: RoleAssistant, Content: p[1]},
		)
		hist = truncateHistory(hist, 2)
	}
	if len(hist) != 2 {
		t.Fatalf("len = %d, want 2", len(hist))
	}
	if hist[0].Content != "goodbye" || hist[1].Content != "see you" {
		t.Errorf("got [%q, %q], want [goodbye, see you]",
			hist[0].Content, hist[1].Content)
	}
}

// stubExecutor records calls and returns canned responses. Used to
// drive GenerateReplyWithTools without a live MCP device.
type stubExecutor struct {
	calls   []stubCall
	results map[string]stubResult
}

type stubCall struct {
	Name string
	Args map[string]any
}

type stubResult struct {
	Text    string
	IsError bool
	Err     error
}

func (s *stubExecutor) Execute(_ context.Context, name string, args map[string]any) (string, bool, error) {
	s.calls = append(s.calls, stubCall{Name: name, Args: args})
	if r, ok := s.results[name]; ok {
		return r.Text, r.IsError, r.Err
	}
	return "ok", false, nil
}

func TestMapMCPToolsToMistralBasic(t *testing.T) {
	tools := []Tool{
		{
			Name:        "self.robot.set_head_angles",
			Description: "Adjust head",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"yaw":{"type":"integer"}}}`),
		},
	}
	out := MapMCPToolsToMistral(tools)
	if len(out) != 1 {
		t.Fatalf("len = %d, want 1", len(out))
	}
	if out[0].Type != "function" {
		t.Errorf("type = %q, want function", out[0].Type)
	}
	if out[0].Function.Name != "self.robot.set_head_angles" {
		t.Errorf("name = %q", out[0].Function.Name)
	}
	// Schema must round-trip verbatim — no re-marshaling that could
	// reorder keys or normalize whitespace.
	if string(out[0].Function.Parameters) != `{"type":"object","properties":{"yaw":{"type":"integer"}}}` {
		t.Errorf("parameters = %q", string(out[0].Function.Parameters))
	}
}

func TestMapMCPToolsToMistralEmptySchema(t *testing.T) {
	// Tools with no schema (zero-arg tools) must still get a valid
	// permissive object schema, or Mistral rejects the request.
	tools := []Tool{{Name: "self.robot.get_reminders"}}
	out := MapMCPToolsToMistral(tools)
	if len(out) != 1 {
		t.Fatalf("len = %d, want 1", len(out))
	}
	if string(out[0].Function.Parameters) != `{"type":"object","properties":{}}` {
		t.Errorf("empty-schema fallback = %q", string(out[0].Function.Parameters))
	}
}

func TestMapMCPToolsToMistralEmpty(t *testing.T) {
	if got := MapMCPToolsToMistral(nil); got != nil {
		t.Errorf("nil → %v, want nil", got)
	}
	if got := MapMCPToolsToMistral([]Tool{}); got != nil {
		t.Errorf("empty → %v, want nil", got)
	}
}

func TestFilterToolsDropsBlocked(t *testing.T) {
	tools := []Tool{
		{Name: "self.robot.set_head_angles"},
		{Name: "self.camera.take_photo"},
		{Name: "self.robot.set_led_color"},
	}
	out := FilterTools(tools, []string{"self.camera.take_photo"})
	if len(out) != 2 {
		t.Fatalf("len = %d, want 2", len(out))
	}
	for _, tl := range out {
		if tl.Name == "self.camera.take_photo" {
			t.Error("blocked tool present in filtered output")
		}
	}
}

func TestFilterToolsEmptyBlocklist(t *testing.T) {
	tools := []Tool{{Name: "a"}, {Name: "b"}}
	if got := FilterTools(tools, nil); len(got) != 2 {
		t.Errorf("nil blocklist should pass through, got %d", len(got))
	}
	if got := FilterTools(tools, []string{}); len(got) != 2 {
		t.Errorf("empty blocklist should pass through, got %d", len(got))
	}
}

func TestParseBlocklistVariants(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"-", nil}, // sentinel = explicitly disabled
		{"a", []string{"a"}},
		{"a,b,c", []string{"a", "b", "c"}},
		{"  a , b  ,c", []string{"a", "b", "c"}}, // whitespace trimmed
		{"a,,b", []string{"a", "b"}},             // empty entries dropped
	}
	for _, tc := range cases {
		got := parseBlocklist(tc.in)
		if len(got) != len(tc.want) {
			t.Errorf("%q → %v, want %v", tc.in, got, tc.want)
			continue
		}
		for i, w := range tc.want {
			if got[i] != w {
				t.Errorf("%q[%d] = %q, want %q", tc.in, i, got[i], w)
			}
		}
	}
}

// TestStubExecutorErrorPath confirms the Execute interface
// distinguishes the three failure modes (transport error, isError
// true, empty success) so the chat-loop tests can stub them.
func TestStubExecutorErrorPath(t *testing.T) {
	s := &stubExecutor{
		results: map[string]stubResult{
			"good":     {Text: "yaw=0,pitch=0"},
			"bad_args": {IsError: true, Text: "invalid yaw"},
			"down":     {Err: errors.New("device offline")},
		},
	}

	text, isErr, err := s.Execute(context.Background(), "good", nil)
	if err != nil || isErr || text != "yaw=0,pitch=0" {
		t.Errorf("good: text=%q isErr=%t err=%v", text, isErr, err)
	}

	text, isErr, err = s.Execute(context.Background(), "bad_args", map[string]any{"yaw": -999})
	if err != nil || !isErr || text != "invalid yaw" {
		t.Errorf("bad_args: text=%q isErr=%t err=%v", text, isErr, err)
	}

	_, _, err = s.Execute(context.Background(), "down", nil)
	if err == nil {
		t.Error("down: want non-nil error")
	}

	if len(s.calls) != 3 {
		t.Errorf("recorded %d calls, want 3", len(s.calls))
	}
}
