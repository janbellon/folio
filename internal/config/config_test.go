package config

import (
	"testing"
	"time"
)

func TestDefaults(t *testing.T) {
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != 8080 || cfg.MaxUploadBytes != 100<<20 || cfg.JobTimeout != time.Minute {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}

func TestOverrides(t *testing.T) {
	t.Setenv("PORT", "9000")
	t.Setenv("MAX_UPLOAD_MB", "5")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != 9000 || cfg.MaxUploadBytes != 5<<20 {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestInvalidValuesFailLoudly(t *testing.T) {
	for _, v := range []string{"abc", "0", "99999999"} {
		t.Setenv("MAX_FILES", v)
		if _, err := Load(); err == nil {
			t.Fatalf("MAX_FILES=%s should be rejected", v)
		}
	}
}
