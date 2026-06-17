package main

import "testing"

func TestParseTokensRejectsEmptyProject(t *testing.T) {
	// Valid: empera:tok-emp, ops:tok-ops. Rejected: ":bad" (empty project),
	// "badproj:" (empty token), "noColon" (no separator).
	got := parseTokens("empera:tok-emp, ops:tok-ops , :bad , noColon , badproj: ")

	if len(got) != 2 {
		t.Fatalf("got %d valid tokens, want 2: %#v", len(got), got)
	}
	if got["tok-emp"] != "empera" || got["tok-ops"] != "ops" {
		t.Errorf("unexpected mapping: %#v", got)
	}
	if _, ok := got["bad"]; ok {
		t.Error("empty-project token must be rejected (no wildcard via PULSE_TOKENS)")
	}
	for tok, proj := range got {
		if proj == "" {
			t.Errorf("token %q stored with empty (wildcard) project", tok)
		}
	}
}
