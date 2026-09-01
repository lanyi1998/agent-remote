package tool

import (
	"strings"
	"testing"
)

func TestApplyReplacementsUsesOriginalFileAndPreservesCRLF(t *testing.T) {
	original := []byte("first\r\nsecond\r\nthird\r\n")
	updated, line, err := applyReplacements(original, []replacement{
		{OldText: "first", NewText: "one"},
		{OldText: "third", NewText: "three"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if line != 1 {
		t.Fatalf("first changed line = %d", line)
	}
	if string(updated) != "one\r\nsecond\r\nthree\r\n" {
		t.Fatalf("unexpected content %q", updated)
	}
}

func TestApplyReplacementsRejectsAmbiguousMatch(t *testing.T) {
	_, _, err := applyReplacements([]byte("same same"), []replacement{{OldText: "same", NewText: "new"}})
	if err == nil || !strings.Contains(err.Error(), "exactly once") {
		t.Fatalf("unexpected error: %v", err)
	}
}
