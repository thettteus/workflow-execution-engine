package providers_test

import (
	"testing"

	"github.com/thettteus/workflow-execution-engine/core/model/providers"
)

func TestConfiguredRegistersOpenAICompatibleProviderByConnectionID(t *testing.T) {
	reg := providers.Configured(providers.Config{
		OpenAICompatible: map[string]string{"kimi": "https://api.moonshot.ai/v1"},
	})
	if _, err := reg.Get("kimi"); err != nil {
		t.Fatalf("Get(kimi): %v", err)
	}
}
