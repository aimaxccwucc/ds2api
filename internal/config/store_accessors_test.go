package config

import "testing"

func TestStoreHistorySplitAccessors(t *testing.T) {
	enabled := true
	turns := 3
	store := &Store{cfg: Config{HistorySplit: HistorySplitConfig{
		Enabled:           &enabled,
		TriggerAfterTurns: &turns,
	}}}
	if store.HistorySplitEnabled() {
		t.Fatal("expected history split to stay disabled")
	}
	if got := store.HistorySplitTriggerAfterTurns(); got != 1 {
		t.Fatalf("history split trigger_after_turns=%d want=1", got)
	}
}

func TestStoreCurrentInputFileAccessors(t *testing.T) {
	store := &Store{cfg: Config{}}
	if store.CurrentInputFileEnabled() {
		t.Fatal("expected current input file disabled by default")
	}
	if got := store.CurrentInputFileMinChars(); got != 0 {
		t.Fatalf("default current input file min_chars=%d want=0", got)
	}

	enabled := false
	store.cfg.CurrentInputFile = CurrentInputFileConfig{Enabled: &enabled, MinChars: 12345}
	if store.CurrentInputFileEnabled() {
		t.Fatal("expected current input file disabled")
	}

	enabled = true
	store.cfg.CurrentInputFile.Enabled = &enabled
	if !store.CurrentInputFileEnabled() {
		t.Fatal("expected current input file enabled")
	}
	if got := store.CurrentInputFileMinChars(); got != 12345 {
		t.Fatalf("current input file min_chars=%d want=12345", got)
	}

	historyEnabled := true
	store.cfg.HistorySplit.Enabled = &historyEnabled
	if !store.CurrentInputFileEnabled() {
		t.Fatal("expected history split config to not suppress current input file mode")
	}
}

func TestStoreThinkingInjectionAccessors(t *testing.T) {
	store := &Store{cfg: Config{}}
	if !store.ThinkingInjectionEnabled() {
		t.Fatal("expected thinking injection enabled by default")
	}

	disabled := false
	store.cfg.ThinkingInjection.Enabled = &disabled
	if store.ThinkingInjectionEnabled() {
		t.Fatal("expected thinking injection disabled by explicit config")
	}

	store.cfg.ThinkingInjection.Prompt = "  custom thinking prompt  "
	if got := store.ThinkingInjectionPrompt(); got != "custom thinking prompt" {
		t.Fatalf("thinking injection prompt=%q want custom thinking prompt", got)
	}
}
