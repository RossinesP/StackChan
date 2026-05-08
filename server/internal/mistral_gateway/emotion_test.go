/*
SPDX-FileCopyrightText: 2026 M5Stack Technology CO LTD
SPDX-License-Identifier: MIT
*/

package mistral_gateway

import "testing"

func TestExtractEmotionNoTag(t *testing.T) {
	emo, clean := ExtractEmotion("Hello there.")
	if emo != "" {
		t.Errorf("emotion = %q, want empty", emo)
	}
	if clean != "Hello there." {
		t.Errorf("cleaned = %q, want unchanged", clean)
	}
}

func TestExtractEmotionAtStart(t *testing.T) {
	emo, clean := ExtractEmotion("[emotion:happy] Voilà!")
	if emo != "happy" {
		t.Errorf("emotion = %q, want happy", emo)
	}
	if clean != "Voilà!" {
		t.Errorf("cleaned = %q, want %q", clean, "Voilà!")
	}
}

func TestExtractEmotionMidSentence(t *testing.T) {
	emo, clean := ExtractEmotion("Hello [emotion:laughing] world!")
	if emo != "laughing" {
		t.Errorf("emotion = %q, want laughing", emo)
	}
	// Whitespace seam should collapse cleanly.
	if clean != "Hello world!" {
		t.Errorf("cleaned = %q, want %q", clean, "Hello world!")
	}
}

func TestExtractEmotionMultipleStripsAll(t *testing.T) {
	emo, clean := ExtractEmotion("[emotion:happy] First [emotion:sad] second.")
	if emo != "happy" {
		t.Errorf("emotion = %q, want happy (first match)", emo)
	}
	if clean != "First second." {
		t.Errorf("cleaned = %q, want %q (all tags stripped)", clean, "First second.")
	}
}

func TestExtractEmotionCaseAndWhitespace(t *testing.T) {
	cases := []struct {
		in       string
		wantEmo  string
		wantText string
	}{
		{"[emotion:HAPPY] hi", "happy", "hi"},
		{"[ emotion : sleepy ] zzz", "sleepy", "zzz"},
		{"[emotion:Doubtful]", "doubtful", ""},
	}
	for _, tc := range cases {
		emo, clean := ExtractEmotion(tc.in)
		if emo != tc.wantEmo {
			t.Errorf("%q → emo=%q, want %q", tc.in, emo, tc.wantEmo)
		}
		if clean != tc.wantText {
			t.Errorf("%q → clean=%q, want %q", tc.in, clean, tc.wantText)
		}
	}
}

func TestExtractEmotionUnknownStillExtracted(t *testing.T) {
	// The model might invent emotions ("hopeful", "smug"). We extract
	// them as-is; the caller logs a warning via IsValidEmotion. The
	// device falls back to Neutral on unknown values.
	emo, clean := ExtractEmotion("[emotion:hopeful] yeah")
	if emo != "hopeful" {
		t.Errorf("emo = %q, want hopeful", emo)
	}
	if clean != "yeah" {
		t.Errorf("clean = %q, want yeah", clean)
	}
}

func TestExtractEmotionMalformedIgnored(t *testing.T) {
	// These don't match the regex (missing colon, missing close bracket,
	// non-alpha leading char) — should pass through untouched.
	cases := []string{
		"[emotion happy] foo",
		"[emotion:happy",
		"[emotion:123] foo",
		"emotion:happy foo",
	}
	for _, in := range cases {
		emo, clean := ExtractEmotion(in)
		if emo != "" {
			t.Errorf("%q → emo=%q, want empty (malformed)", in, emo)
		}
		if clean != in {
			t.Errorf("%q → clean=%q, want unchanged (malformed)", in, clean)
		}
	}
}

func TestExtractEmotionEmpty(t *testing.T) {
	emo, clean := ExtractEmotion("")
	if emo != "" || clean != "" {
		t.Errorf("empty input → emo=%q clean=%q, want both empty", emo, clean)
	}
}

func TestIsValidEmotion(t *testing.T) {
	for _, e := range []string{"neutral", "happy", "sad", "doubtful"} {
		if !IsValidEmotion(e) {
			t.Errorf("%q should be valid", e)
		}
	}
	for _, e := range []string{"", "smug", "Happy" /* case-sensitive */, "neutral "} {
		if IsValidEmotion(e) {
			t.Errorf("%q should NOT be valid", e)
		}
	}
}

func TestCollapseSpaces(t *testing.T) {
	cases := []struct{ in, want string }{
		{"hello  world", "hello world"},
		{"hello\t\tworld", "hello world"},
		{"a  b  c   d", "a b c d"},
		{"single", "single"},
		{"", ""},
		{"line1\n\nline2", "line1\n\nline2"}, // newlines preserved
	}
	for _, tc := range cases {
		if got := collapseSpaces(tc.in); got != tc.want {
			t.Errorf("collapseSpaces(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
