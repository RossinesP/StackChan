/*
SPDX-FileCopyrightText: 2026 M5Stack Technology CO LTD
SPDX-License-Identifier: MIT
*/

package mistral_gateway

import "testing"

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
