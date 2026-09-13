package openaiapi

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/inferplane/inferplane/internal/keystore"
	"github.com/inferplane/inferplane/internal/principal"
	"github.com/inferplane/inferplane/internal/router"
)

type ModelsHandler struct{ r *router.Router }

func NewModelsHandler(r *router.Router) *ModelsHandler { return &ModelsHandler{r: r} }

// ServeHTTP returns the configured models in OpenAI's GET /v1/models shape:
// {"object":"list","data":[{"id","object":"model","owned_by":"inferplane"}]}.
// Filtered by the virtual key's allow-list when a principal is present (§3.1);
// an absent principal returns the full, unfiltered list (tests without auth).
func (h *ModelsHandler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	var p *keystore.Principal
	if authenticated, ok := principal.From(req.Context()); ok {
		p = &authenticated
	}
	models := h.r.DescribeModels(p)
	data := make([]map[string]any, 0, len(models))
	for _, model := range models {
		entry := map[string]any{
			"id":       model.Name,
			"object":   "model",
			"owned_by": "inferplane",
		}
		// Gateway extension, omitted when undeclared: context_window is the
		// key context-aware OpenAI-wire clients look for; max_model_len is
		// the vLLM spelling of the same fact, included for clients that read
		// that instead.
		if win := model.ContextWindow; win > 0 {
			entry["context_window"] = win
			entry["max_model_len"] = win
		}
		mode, capabilities := responsesContract(model)
		entry["responses_mode"] = mode
		entry["capabilities"] = capabilities
		if mode == "native" {
			// Bind client metadata from the public name only. An upstream
			// target may be a private deployment identifier and must not be
			// disclosed just to populate a client-side model catalog.
			entry["codex_model"] = strings.TrimPrefix(strings.TrimPrefix(model.Name, "openai."), "openai/")
		}
		data = append(data, entry)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"object": "list",
		"data":   data,
	})
}

func responsesContract(model router.ModelDescriptor) (string, []string) {
	capabilities := append([]string{}, model.Capabilities...)
	sort.Strings(capabilities)
	if len(model.Targets) == 0 {
		return "unsupported", capabilities
	}
	native := true
	for _, target := range model.Targets {
		if target.Provider == nil {
			return "unsupported", capabilities
		}
		supporter, declared := target.Provider.(router.IngressSupporter)
		if declared && !supporter.SupportsIngress("responses") {
			return "unsupported", capabilities
		}
		if target.Provider.Name() == "openai_responses" {
			continue
		}
		native = false
		if target.Provider.Name() != "anthropic" && target.Provider.Name() != "openai_compatible" && !declared {
			return "unsupported", capabilities
		}
	}
	if native {
		return "native", capabilities
	}
	return "bridge", capabilities
}
