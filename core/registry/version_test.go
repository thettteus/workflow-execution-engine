package registry_test

import (
	"testing"

	"github.com/thettteus/workflow-execution-engine/core/registry"
)

func TestValidVersion(t *testing.T) {
	valid := []string{"0.0.0", "1.0.0", "1.2.3", "10.20.30", "1.0.0-alpha", "1.0.0-alpha.1", "1.0.0+build.5", "1.0.0-rc.1+exp.sha.5114f85"}
	for _, v := range valid {
		if !registry.ValidVersion(v) {
			t.Errorf("ValidVersion(%q) = false, want true", v)
		}
	}
	invalid := []string{"", "1", "1.0", "1.0.0.0", "v1.0.0", "1.0.x", "01.0.0", "1.0.0-", "latest", "1.0.0 "}
	for _, v := range invalid {
		if registry.ValidVersion(v) {
			t.Errorf("ValidVersion(%q) = true, want false", v)
		}
	}
}

func TestParseRef(t *testing.T) {
	id, ver, err := registry.ParseRef("reviewer@1.2.3")
	if err != nil {
		t.Fatalf("ParseRef(reviewer@1.2.3): %v", err)
	}
	if id != "reviewer" || ver != "1.2.3" {
		t.Errorf("ParseRef = (%q, %q), want (reviewer, 1.2.3)", id, ver)
	}

	for _, bad := range []string{"reviewer", "reviewer@", "@1.0.0", "reviewer@latest", "reviewer@1.0"} {
		if _, _, err := registry.ParseRef(bad); err == nil {
			t.Errorf("ParseRef(%q) = nil error, want an error", bad)
		}
	}
}
