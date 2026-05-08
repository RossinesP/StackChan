/*
SPDX-FileCopyrightText: 2026 M5Stack Technology CO LTD
SPDX-License-Identifier: MIT
*/

package mistral_gateway

import (
	"regexp"
	"strings"
)

// Valid emotion strings the StackChan firmware accepts. Per
// firmware/main/hal/board/stackchan_display.cc::SetEmotion, anything
// not in this set is silently mapped to Neutral on the device. We
// keep the list here as an allowlist for logging — unknown emotions
// from the model probably indicate a system-prompt drift worth
// flagging in the operator log.
var ValidEmotions = map[string]struct{}{
	"neutral":  {},
	"happy":    {},
	"laughing": {},
	"angry":    {},
	"sad":      {},
	"crying":   {},
	"sleepy":   {},
	"doubtful": {},
}

// emotionTagRE matches our convention: [emotion:NAME] anywhere in
// the model's reply. The capture is case-insensitive in practice
// because we lowercase the result.
//
// Allowed name characters are letters/digits/underscore — same as
// the firmware's strcmp targets above. Whitespace inside the
// brackets is forgiven (`[emotion: happy ]` works).
var emotionTagRE = regexp.MustCompile(`\[\s*emotion\s*:\s*([a-zA-Z][a-zA-Z0-9_]*)\s*\]`)

// ExtractEmotion finds the first [emotion:NAME] tag in `text`,
// returns it lowercased, and returns `text` with ALL emotion tags
// removed (and surrounding whitespace tidied). If no tag is present,
// returns ("", trimmed-text).
//
// We strip every occurrence even if the model emits multiple, so
// nothing leaks into TTS. The emission of the actual `llm` event
// uses only the first match — multiple emotions in one turn don't
// have a meaningful interpretation in the device avatar today.
func ExtractEmotion(text string) (emotion, cleaned string) {
	if text == "" {
		return "", ""
	}
	matches := emotionTagRE.FindStringSubmatch(text)
	if len(matches) >= 2 {
		emotion = strings.ToLower(matches[1])
	}
	// Strip every tag, then collapse the whitespace seam where the
	// tag used to live. We don't touch the rest of the text — the
	// model controls its own punctuation.
	cleaned = emotionTagRE.ReplaceAllString(text, "")
	cleaned = strings.TrimSpace(cleaned)
	// Collapse runs of internal whitespace introduced by stripping
	// (e.g. "Hello [emotion:happy] world" → "Hello  world" → "Hello world").
	cleaned = collapseSpaces(cleaned)
	return emotion, cleaned
}

// IsValidEmotion reports whether `e` is one the device's SetEmotion
// recognizes. The device falls back to Neutral on unknown values, so
// this is informational — used for log warnings.
func IsValidEmotion(e string) bool {
	_, ok := ValidEmotions[e]
	return ok
}

// collapseSpaces replaces runs of 2+ ASCII spaces / tabs with a
// single space. Doesn't touch newlines (the model's paragraph
// structure, if any, survives).
func collapseSpaces(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	prevSpace := false
	for _, r := range s {
		if r == ' ' || r == '\t' {
			if !prevSpace {
				b.WriteRune(' ')
				prevSpace = true
			}
			continue
		}
		prevSpace = false
		b.WriteRune(r)
	}
	return b.String()
}
