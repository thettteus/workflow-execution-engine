package settings_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thettteus/workflow-execution-engine/core/settings"
)

// REQ-CTRL-05: settings persist and reload unchanged across a fresh Store (a
// restart) — the durability guarantee M2.2 acceptance depends on.
func TestSaveLoadRoundTrip(t *testing.T) {
	ws := t.TempDir()
	want := settings.Settings{
		ProviderBaseURLs: map[string]string{"openai": "http://localhost:11434/v1"},
		ProviderKeyEnv:   map[string]string{"openai": "OPENAI_API_KEY"},
		DefaultBudgetUSD: 2.5,
		CacheMode:        "readonly",
		WorkspaceRoot:    "/repo",
		TemplatePaths:    []string{"examples"},
		Connections: []settings.Connection{{
			ID:        "kimi",
			Label:     "Kimi",
			Kind:      settings.ConnectionKindModelProvider,
			Type:      "openai-compatible",
			BaseURL:   "https://api.moonshot.ai/v1",
			SecretEnv: "MOONSHOT_API_KEY",
			Defaults:  map[string]string{"model": "moonshot-v1-8k"},
		}},
		Notifications: settings.NotificationSettings{
			Enabled:        true,
			BrowserEnabled: true,
			Events:         map[string]bool{"failed": true, "finished": false},
			Thresholds:     settings.NotificationThresholds{MinCostUSD: 1.2, MinDurationSec: 30},
			QuietHours:     settings.NotificationQuietHours{Enabled: true, Start: "22:00", End: "07:00"},
		},
	}
	if err := settings.New(ws).Save(want); err != nil {
		t.Fatalf("save: %v", err)
	}
	// A brand-new Store (as a restarted process would build) sees the same data.
	got, err := settings.New(ws).Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.CacheMode != want.CacheMode || got.DefaultBudgetUSD != want.DefaultBudgetUSD ||
		got.WorkspaceRoot != want.WorkspaceRoot || got.ProviderBaseURLs["openai"] != want.ProviderBaseURLs["openai"] ||
		got.ProviderKeyEnv["openai"] != want.ProviderKeyEnv["openai"] || len(got.TemplatePaths) != 1 ||
		len(got.Connections) != 1 || got.Connections[0].ID != "kimi" ||
		got.Connections[0].SecretEnv != "MOONSHOT_API_KEY" ||
		!got.Notifications.Enabled || !got.Notifications.BrowserEnabled ||
		got.Notifications.Events["finished"] ||
		got.Notifications.Thresholds.MinDurationSec != 30 ||
		got.Notifications.QuietHours.Start != "22:00" {
		t.Fatalf("round-trip mismatch:\n got=%+v\nwant=%+v", got, want)
	}
}

// A workspace that has never been configured loads as the zero Settings, not an
// error — a blank service must start clean.
func TestLoadMissingIsZero(t *testing.T) {
	got, err := settings.New(t.TempDir()).Load()
	if err != nil {
		t.Fatalf("load missing: %v", err)
	}
	if (got.CacheMode != "") || got.DefaultBudgetUSD != 0 || len(got.ProviderBaseURLs) != 0 {
		t.Fatalf("missing settings should be zero, got %+v", got)
	}
}

// PRIN-10 / REQ-CTRL-05: the persisted file must never contain key material.
// The Settings type has no field for a secret value; this guards against a
// future field regressing that by asserting a plausible secret never lands on
// disk even when the env-var NAME that holds it is recorded.
func TestSecretValueNeverPersisted(t *testing.T) {
	ws := t.TempDir()
	const secret = "sk-super-secret-key-value"
	t.Setenv("OPENAI_API_KEY", secret)
	if err := settings.New(ws).Save(settings.Settings{
		ProviderKeyEnv: map[string]string{"openai": "OPENAI_API_KEY"},
		Connections: []settings.Connection{{
			ID:        "github",
			Label:     "GitHub",
			Kind:      settings.ConnectionKindChangeSource,
			Type:      "github",
			BaseURL:   "https://api.github.com",
			SecretEnv: "GITHUB_AUTH_HEADER",
		}},
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(ws, "settings.json"))
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if strings.Contains(string(data), secret) {
		t.Fatalf("settings.json leaked a secret value:\n%s", data)
	}
	if !strings.Contains(string(data), "OPENAI_API_KEY") {
		t.Fatalf("settings.json should record the env var NAME, got:\n%s", data)
	}
}

// NFR-CTRL-01: a Save leaves no stray temp files behind (temp-then-rename
// commits atomically; the temp is cleaned up).
func TestSaveLeavesNoTempFiles(t *testing.T) {
	ws := t.TempDir()
	if err := settings.New(ws).Save(settings.Settings{CacheMode: "on"}); err != nil {
		t.Fatalf("save: %v", err)
	}
	entries, err := os.ReadDir(ws)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") {
			t.Fatalf("leftover temp file: %s", e.Name())
		}
	}
}
