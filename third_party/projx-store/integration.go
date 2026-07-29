package store

import (
	"encoding/json"
	"sort"
	"strings"
)

// integration.go — the vendor-neutral PROVIDER seam. ProjX makes two kinds of model
// calls: a one-shot COMPLETION (triage / decompose splitting) and an AGENT launch (the
// work — already data, via the KRoute tier map). This file makes the completion side
// data too: an "integration" declares HOW to reach a provider, so the engine carries no
// vendor-specific flags. Claude Code ships as the DEFAULT integration (a replaceable
// datum, not code); you write others (an OpenAI-compatible endpoint, a local model, a
// different agent CLI) as records — "I write the integrations."
//
// An integration is a KRoute record keyed `setting/integration/<name>` (JSON body),
// excluded from context injection like every setting/* record. `setting/integration-active`
// names the one in force. The store stays OS-/network-free: it RESOLVES the spec; the
// engine EXECUTES it (exec for cli, HTTP for http-openai).

// CompletionSpec is a resolved provider definition for one-shot model calls.
type CompletionSpec struct {
	Name      string `json:"-"`
	Transport string `json:"transport"`            // "cli" | "http-openai"
	Template  string `json:"template,omitempty"`   // cli: argv template with {prompt} {model} placeholders
	BaseURL   string `json:"base_url,omitempty"`   // http-openai: OpenAI-compatible base
	APIKeyEnv string `json:"api_key_env,omitempty"` // http-openai: NAME of the env var holding the key (never the key itself)
	Model     string `json:"model,omitempty"`      // optional default model for this provider
}

// Transport values.
const (
	TransportCLI      = "cli"
	TransportHTTPOpenAI = "http-openai"
)

// Integration setting record keys.
const (
	SettingIntegrationActive = "setting/integration-active" // Body = active integration name
	settingIntegrationPrefix = "setting/integration/"       // + <name>, Body = CompletionSpec JSON
)

// DefaultIntegration is the shipped fallback: drive the harness's own agent CLI in
// one-shot print mode. It is DATA (a replaceable default), not logic — override it by
// declaring an integration and marking it active. The `{prompt}`/`{model}` placeholders
// are substituted as whole argv elements (no shell), so prompts with spaces are safe.
var DefaultIntegration = CompletionSpec{
	Name:      "claude-code",
	Transport: TransportCLI,
	Template:  "claude -p {prompt} --model {model}",
	Model:     "haiku", // cheap default for triage/decompose; PROJX_TRIAGE_MODEL overrides
}

// ResolveCompletion returns the active integration spec and whether one was declared.
// When none is declared it returns DefaultIntegration/false, so a caller can tell an
// explicit choice from the built-in fallback.
func ResolveCompletion(s Store) (CompletionSpec, bool) {
	if s == nil {
		return DefaultIntegration, false
	}
	name := settingBody(s, SettingIntegrationActive)
	if name == "" {
		return DefaultIntegration, false
	}
	if spec, ok := IntegrationSpec(s, name); ok {
		return spec, true
	}
	return DefaultIntegration, false
}

// IntegrationSpec reads a named integration record. Returns ok=false if absent or
// malformed.
func IntegrationSpec(s Store, name string) (CompletionSpec, bool) {
	body := settingBody(s, settingIntegrationPrefix+strings.TrimSpace(name))
	if body == "" {
		return CompletionSpec{}, false
	}
	var spec CompletionSpec
	if json.Unmarshal([]byte(body), &spec) != nil || spec.Transport == "" {
		return CompletionSpec{}, false
	}
	spec.Name = name
	return spec, true
}

// IntegrationNames lists the names of every declared integration (sorted for stable
// output), read from the `setting/integration/<name>` records.
func IntegrationNames(s Store) []string {
	if s == nil {
		return nil
	}
	var out []string
	for _, r := range s.List(OfKind(KRoute)) {
		if name := strings.TrimPrefix(r.Key, settingIntegrationPrefix); name != r.Key && name != "" {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// IntegrationRecord builds the store record for a declared integration (JSON body).
func IntegrationRecord(spec CompletionSpec) Record {
	body, _ := json.Marshal(spec)
	return Record{
		ID:     KRoute.String() + "/" + seedSlug(settingIntegrationPrefix+spec.Name),
		Kind:   KRoute,
		Scope:  ScopeProject,
		Key:    settingIntegrationPrefix + spec.Name,
		Body:   string(body),
		Origin: "seed:floor",
	}
}

// IntegrationActiveRecord builds the record naming the active integration.
func IntegrationActiveRecord(name string) Record {
	return Record{
		ID:     KRoute.String() + "/" + seedSlug(SettingIntegrationActive),
		Kind:   KRoute,
		Scope:  ScopeProject,
		Key:    SettingIntegrationActive,
		Body:   name,
		Origin: "seed:floor",
	}
}

// RenderCLIArgs turns a cli template into an argv, substituting {prompt}/{model} as
// whole elements (never string-spliced, so spaces in the prompt are safe). An empty
// template yields nil.
func RenderCLIArgs(template, prompt, model string) []string {
	template = strings.TrimSpace(template)
	if template == "" {
		return nil
	}
	fields := strings.Fields(template)
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		switch f {
		case "{prompt}":
			out = append(out, prompt)
		case "{model}":
			out = append(out, model)
		default:
			out = append(out, f)
		}
	}
	return out
}
