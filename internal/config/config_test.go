package config

import (
	"strings"
	"testing"
	"time"
)

func TestValidate_DefaultPasses(t *testing.T) {
	cfg := DefaultConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("default config failed validation: %v", err)
	}
}

func TestValidate_ShareTargetTimeTooLarge(t *testing.T) {
	// A unit-typo case: 30 minutes typed as "30h" — past the sanity bound.
	cfg := DefaultConfig()
	cfg.ShareTargetTime = 30 * time.Hour

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for share-target-time > 1h")
	}
	if !strings.Contains(err.Error(), "share-target-time") {
		t.Errorf("error %q does not mention share-target-time", err.Error())
	}
}

func TestValidate_ShareTargetTimeAtBoundary(t *testing.T) {
	// Exactly 1h must pass (the cap is inclusive of 1h).
	cfg := DefaultConfig()
	cfg.ShareTargetTime = time.Hour
	if err := cfg.Validate(); err != nil {
		t.Errorf("share-target-time = 1h should be valid: %v", err)
	}
}

func TestValidate_ShareTargetTimeTooSmall(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ShareTargetTime = 500 * time.Millisecond
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for share-target-time < 1s")
	}
}
