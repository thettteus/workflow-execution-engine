package security_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/thettteus/workflow-execution-engine/core/security"
)

func TestScanFilesForSecretsFindsForbiddenBytes(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "events.jsonl"), []byte(`{"token":"sk-test"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	findings, err := security.ScanFilesForSecrets(root, []string{"sk-test", ""})
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 || findings[0].Value != "sk-test" {
		t.Fatalf("findings = %#v", findings)
	}
}
