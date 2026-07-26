// Package providers is the single wiring site that binds the built-in provider
// implementations into a model.Registry. It is deliberately the ONLY package —
// outside the provider packages themselves — permitted to import
// core/model/openai and core/model/anthropic; everything else, the engine
// included, depends only on the model.Provider interface (REQ-MODEL-01,
// enforced by core/model's isolation test). Keeping the binding here also breaks
// the import cycle that a binding inside core/model would create.
package providers

import (
	"github.com/thettteus/workflow-execution-engine/core/model"
	"github.com/thettteus/workflow-execution-engine/core/model/anthropic"
	"github.com/thettteus/workflow-execution-engine/core/model/openai"
)

// Config overrides the built-in providers' API roots (REQ-MODEL-04) — e.g. to
// point "openai" at a self-hosted OpenAI-compatible endpoint (Ollama, vLLM,
// llama.cpp). An empty base URL leaves that provider on its public default. This
// is the seam the durable control plane's persisted settings flow through
// (M2.2): the CLI reads settings.ProviderBaseURLs and passes them here, keeping
// the concrete openai/anthropic imports confined to this package (REQ-MODEL-01).
type Config struct {
	OpenAIBaseURL    string
	AnthropicBaseURL string
	// OpenAICompatible maps provider names (for example "kimi") to base URLs
	// served through the existing OpenAI-compatible client (REQ-CONN-02).
	OpenAICompatible map[string]string
}

// Default returns a Registry wired with the built-in providers on their public
// API roots, each reading its own API key from the environment: "openai" (the
// default, cheaper) and "anthropic".
func Default() *model.Registry { return Configured(Config{}) }

// Configured returns a Registry with the built-in providers, applying any base
// URL overrides in cfg. Keys are still read from the environment lazily.
func Configured(cfg Config) *model.Registry {
	r := model.NewRegistry()
	var oo []openai.Option
	if cfg.OpenAIBaseURL != "" {
		oo = append(oo, openai.WithBaseURL(cfg.OpenAIBaseURL))
	}
	r.Register("openai", openai.New(oo...))
	for name, baseURL := range cfg.OpenAICompatible {
		var opts []openai.Option
		if baseURL != "" {
			opts = append(opts, openai.WithBaseURL(baseURL))
		}
		r.Register(name, openai.New(opts...))
	}
	var ao []anthropic.Option
	if cfg.AnthropicBaseURL != "" {
		ao = append(ao, anthropic.WithBaseURL(cfg.AnthropicBaseURL))
	}
	r.Register("anthropic", anthropic.New(ao...))
	return r
}
