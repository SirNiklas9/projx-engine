package main

// cmd_models.go controls Codex model selection directly. It does not launch a
// ProjX worker to switch models: a profile is written to .codex/config.toml for
// the next Desktop task, and rendered as flags for a new CLI invocation.

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/SirNiklas9/projx-engine/internal/routing"
)

const defaultModelCatalogTTL = 6 * time.Hour

type codexDebugModels struct {
	Models []struct {
		Slug                     string `json:"slug"`
		DisplayName              string `json:"display_name"`
		Visibility               string `json:"visibility"`
		SupportedReasoningLevels []struct {
			Effort string `json:"effort"`
		} `json:"supported_reasoning_levels"`
	} `json:"models"`
}

type claudeAuthStatus struct {
	LoggedIn    bool   `json:"loggedIn"`
	APIProvider string `json:"apiProvider"`
}

// codexProfilesFromJSON turns the installed CLI's local model catalog into
// provider-neutral routing profiles. It is deliberately a pure parser so the
// exact model/effort contract is acceptance-testable without a network call.
func codexProfilesFromJSON(data []byte, checkedAt int64) ([]routing.ModelProfile, error) {
	var raw codexDebugModels
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse Codex model catalog: %w", err)
	}
	profiles := make([]routing.ModelProfile, 0)
	for _, model := range raw.Models {
		if strings.TrimSpace(model.Slug) == "" || (model.Visibility != "" && model.Visibility != "list") {
			continue
		}
		for _, level := range model.SupportedReasoningLevels {
			effort, ok := routing.NormalizeEffort(strings.ToLower(strings.TrimSpace(level.Effort)))
			if !ok {
				continue // record new native values only after their normalized policy is defined
			}
			profile := routing.ModelProfile{
				Provider: "codex", Model: model.Slug, Family: model.DisplayName,
				Effort: effort, NativeEffort: level.Effort,
				Availability: routing.AvailabilityUsable, Source: "codex debug models", CheckedAt: checkedAt,
				Freshness: 1,
			}
			applyCodexProfileDefaults(&profile)
			profiles = append(profiles, profile)
		}
	}
	if len(profiles) == 0 {
		return nil, fmt.Errorf("Codex reported no visible model/effort profiles")
	}
	sort.Slice(profiles, func(i, j int) bool {
		if profiles[i].Model != profiles[j].Model {
			return profiles[i].Model < profiles[j].Model
		}
		return profiles[i].NativeEffort < profiles[j].NativeEffort
	})
	return profiles, nil
}

// applyCodexProfileDefaults gives newly discovered profiles conservative,
// deterministic routing metadata until measured/provider-published values are
// curated into the catalog. Availability is always discovered, never guessed.
func applyCodexProfileDefaults(p *routing.ModelProfile) {
	name := strings.ToLower(p.Model)
	p.Reliability, p.Capability = .90, .85
	p.Quality, p.Cost, p.Latency, p.Efficiency = .78, .45, .45, .72
	switch {
	case strings.Contains(name, "sol"):
		p.Quality, p.Cost, p.Latency, p.Capability, p.Efficiency = .99, .95, .90, 1, .42
	case strings.Contains(name, "terra"):
		p.Quality, p.Cost, p.Latency, p.Capability, p.Efficiency = .92, .62, .58, .94, .78
	case strings.Contains(name, "luna"):
		p.Quality, p.Cost, p.Latency, p.Capability, p.Efficiency = .84, .32, .25, .86, .96
	case strings.Contains(name, "mini"), strings.Contains(name, "spark"):
		p.Quality, p.Cost, p.Latency, p.Capability, p.Efficiency = .70, .12, .10, .72, 1
	}
	applyEffortCostDefaults(p)
}

func applyEffortCostDefaults(p *routing.ModelProfile) {
	switch p.Effort {
	case routing.EffortLow:
		p.Quality -= .08
		p.Cost *= .55
		p.Latency *= .55
		p.Efficiency = minFloat(1, p.Efficiency+.08)
	case routing.EffortHigh:
		p.Quality = minFloat(1, p.Quality+.04)
		p.Cost = minFloat(1, p.Cost*1.25)
		p.Latency = minFloat(1, p.Latency*1.25)
	case routing.EffortUltra:
		p.Quality = minFloat(1, p.Quality+.07)
		p.Cost = minFloat(1, p.Cost*1.5)
		p.Latency = minFloat(1, p.Latency*1.5)
	}
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func modelCatalogTTL() time.Duration {
	if raw := strings.TrimSpace(os.Getenv("PROJX_MODEL_CATALOG_TTL")); raw != "" {
		if ttl, err := time.ParseDuration(raw); err == nil && ttl > 0 {
			return ttl
		}
	}
	return defaultModelCatalogTTL
}

// ensureModelCatalogFresh refreshes provider availability before work is
// routed. Failure preserves the last-known-good catalog so an offline provider
// cannot break an otherwise usable project.
func ensureModelCatalogFresh(absRoot string) {
	catalog, err := routing.LoadCatalog(absRoot)
	if err == nil && len(catalog.Profiles) > 0 &&
		time.Since(time.Unix(catalog.UpdatedAt, 0)) < modelCatalogTTL() {
		return
	}
	now := time.Now().Unix()
	merged := catalog.Profiles
	if profiles, discoverErr := discoverCodexProfiles(); discoverErr == nil {
		merged = routing.MergeProviderProfiles(merged, profiles, "codex", now)
	}
	if profiles, discoverErr := discoverClaudeProfiles(); discoverErr == nil {
		merged = routing.MergeProviderProfiles(merged, profiles, "claude", now)
	}
	if len(merged) > 0 {
		_ = routing.SaveCatalog(absRoot, routing.ModelCatalog{UpdatedAt: now, Profiles: merged})
	}
}

func discoverCodexProfiles() ([]routing.ModelProfile, error) {
	output, err := exec.Command("codex", "debug", "models").Output()
	if err != nil {
		return nil, fmt.Errorf("read Codex model catalog: %w", err)
	}
	return codexProfilesFromJSON(output, time.Now().Unix())
}

func discoverClaudeProfiles() ([]routing.ModelProfile, error) {
	authOutput, err := exec.Command("claude", "auth", "status", "--json").Output()
	if err != nil {
		return nil, fmt.Errorf("read Claude authentication status: %w", err)
	}
	var auth claudeAuthStatus
	if json.Unmarshal(authOutput, &auth) != nil || !auth.LoggedIn {
		return nil, fmt.Errorf("Claude is not logged in")
	}
	helpOutput, err := exec.Command("claude", "--help").Output()
	if err != nil {
		return nil, fmt.Errorf("read Claude capabilities: %w", err)
	}
	return claudeProfilesFromHelp(string(helpOutput), time.Now().Unix()), nil
}

func claudeProfilesFromHelp(help string, checkedAt int64) []routing.ModelProfile {
	models := []string{"haiku", "sonnet", "opus", "fable"}
	for _, token := range strings.FieldsFunc(strings.ToLower(help), func(r rune) bool {
		return !(r == '-' || r == '_' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	}) {
		if strings.HasPrefix(token, "claude-") {
			models = append(models, token)
		}
	}
	models = uniqueStrings(models)
	efforts := []string{"low", "medium", "high"}
	if strings.Contains(help, "xhigh") {
		efforts = append(efforts, "xhigh")
	}
	if strings.Contains(help, "max") {
		efforts = append(efforts, "max")
	}
	var profiles []routing.ModelProfile
	for _, model := range models {
		for _, native := range efforts {
			effort, ok := routing.NormalizeEffort(native)
			if !ok {
				continue
			}
			profile := routing.ModelProfile{
				Provider: "claude", Model: model, Family: claudeFamily(model),
				Effort: effort, NativeEffort: native, Availability: routing.AvailabilityUsable,
				Source: "claude --help + auth status", CheckedAt: checkedAt, Freshness: 1,
			}
			applyClaudeProfileDefaults(&profile)
			profiles = append(profiles, profile)
		}
	}
	return profiles
}

func claudeFamily(model string) string {
	for _, family := range []string{"haiku", "sonnet", "opus", "fable"} {
		if strings.Contains(strings.ToLower(model), family) {
			return family
		}
	}
	return model
}

func applyClaudeProfileDefaults(p *routing.ModelProfile) {
	p.Reliability, p.Freshness = .9, 1
	switch claudeFamily(p.Model) {
	case "haiku":
		p.Quality, p.Cost, p.Latency, p.Capability, p.Efficiency = .72, .15, .12, .74, 1
	case "sonnet":
		p.Quality, p.Cost, p.Latency, p.Capability, p.Efficiency = .9, .55, .48, .92, .82
	case "opus":
		p.Quality, p.Cost, p.Latency, p.Capability, p.Efficiency = .98, .9, .84, 1, .48
	case "fable":
		p.Quality, p.Cost, p.Latency, p.Capability, p.Efficiency = .95, .75, .68, .98, .62
	default:
		p.Quality, p.Cost, p.Latency, p.Capability, p.Efficiency = .84, .5, .5, .86, .7
	}
	applyEffortCostDefaults(p)
}

func findCodexProfile(profiles []routing.ModelProfile, model, effort string) (routing.ModelProfile, error) {
	requested := strings.ToLower(strings.TrimSpace(effort))
	normalized, ok := routing.NormalizeEffort(requested)
	if !ok {
		return routing.ModelProfile{}, fmt.Errorf("unknown effort %q (want low, medium, high, or ultra)", effort)
	}
	// A literal native selection wins. This keeps Codex's meaningful distinctions
	// (high/xhigh and max/ultra) available instead of silently collapsing them.
	for _, p := range profiles {
		if p.Provider == "codex" && p.Model == model && strings.EqualFold(p.NativeEffort, requested) && p.Availability == routing.AvailabilityUsable {
			return p, nil
		}
	}
	// A neutral ProjX band chooses the closest native level, in a deterministic
	// preference order. For example Ultra means literal ultra when the model has
	// it, otherwise max; High means high before xhigh.
	preferredNative := map[routing.Effort][]string{
		routing.EffortLow:    {"low", "minimal", "none"},
		routing.EffortMedium: {"medium"},
		routing.EffortHigh:   {"high", "xhigh"},
		routing.EffortUltra:  {"ultra", "max", "adaptive-high", "adaptive"},
	}[normalized]
	for _, native := range preferredNative {
		for _, p := range profiles {
			if p.Provider == "codex" && p.Model == model && strings.EqualFold(p.NativeEffort, native) && p.Availability == routing.AvailabilityUsable {
				return p, nil
			}
		}
	}
	for _, p := range profiles {
		if p.Provider == "codex" && p.Model == model && p.Effort == normalized && p.Availability == routing.AvailabilityUsable {
			return p, nil
		}
	}
	return routing.ModelProfile{}, fmt.Errorf("Codex profile %s + %s is not available in the current catalog", model, effort)
}

// applyCodexProjectProfile preserves every unrelated config key (including
// ProjX MCP configuration) while changing exactly the two Codex selectors.
func applyCodexProjectProfile(absRoot string, profile routing.ModelProfile) (string, error) {
	if profile.Provider != "codex" || profile.Model == "" || profile.NativeEffort == "" {
		return "", fmt.Errorf("invalid Codex model profile")
	}
	path := filepath.Join(absRoot, ".codex", "config.toml")
	cfg := map[string]any{}
	if data, err := os.ReadFile(path); err == nil && len(bytes.TrimSpace(data)) > 0 {
		if _, err := toml.Decode(string(data), &cfg); err != nil {
			return "", fmt.Errorf("%s exists but is not valid TOML: %w", path, err)
		}
	} else if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	cfg["model"] = profile.Model
	cfg["model_reasoning_effort"] = profile.NativeEffort
	var out bytes.Buffer
	if err := toml.NewEncoder(&out).Encode(cfg); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, out.Bytes(), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

func codexCLIArgs(profile routing.ModelProfile, task string) []string {
	return []string{"exec", "--model", profile.Model, "--config", "model_reasoning_effort=" + profile.NativeEffort, task}
}

func runModelsCmd(absRoot string, args []string) {
	if len(args) == 0 {
		die("models: usage: models refresh | list | apply --model <model> --effort <low|medium|high|ultra> | cleanup [--apply]")
	}
	switch args[0] {
	case "refresh":
		checkedAt := time.Now().Unix()
		previous, loadErr := routing.LoadCatalog(absRoot)
		if loadErr != nil {
			die("models refresh: load existing catalog: %v", loadErr)
		}
		profiles := previous.Profiles
		refreshed := []string{}
		if codexProfiles, err := discoverCodexProfiles(); err == nil {
			profiles = routing.MergeProviderProfiles(profiles, codexProfiles, "codex", checkedAt)
			refreshed = append(refreshed, "codex")
		}
		if claudeProfiles, err := discoverClaudeProfiles(); err == nil {
			profiles = routing.MergeProviderProfiles(profiles, claudeProfiles, "claude", checkedAt)
			refreshed = append(refreshed, "claude")
		}
		if len(refreshed) == 0 {
			die("models refresh: no authenticated provider catalog was available")
		}
		if err := routing.SaveCatalog(absRoot, routing.ModelCatalog{UpdatedAt: checkedAt, Profiles: profiles}); err != nil {
			die("models refresh: save catalog: %v", err)
		}
		usable := map[string]int{}
		for _, profile := range profiles {
			if profile.Availability == routing.AvailabilityUsable {
				usable[profile.Provider]++
			}
		}
		fmt.Printf("models: refreshed %s; usable profiles codex=%d claude=%d\n",
			strings.Join(refreshed, ","), usable["codex"], usable["claude"])
	case "list":
		catalog, err := routing.LoadCatalog(absRoot)
		if err != nil {
			die("models list: %v", err)
		}
		if len(catalog.Profiles) == 0 {
			die("models list: no project catalog; run `projx models refresh`")
		}
		for _, p := range catalog.Profiles {
			fmt.Printf("%s  %s  effort=%s (native=%s)  %s\n", p.Provider, p.Model, p.Effort, p.NativeEffort, p.Availability)
		}
	case "apply":
		fs := flag.NewFlagSet("models apply", flag.ExitOnError)
		model := fs.String("model", "", "exact Codex model slug")
		effort := fs.String("effort", "", "low|medium|high|ultra (or a listed native level such as xhigh/max)")
		_ = fs.Parse(args[1:])
		if *model == "" || *effort == "" {
			die("models apply: --model and --effort are required")
		}
		catalog, err := routing.LoadCatalog(absRoot)
		if err != nil {
			die("models apply: %v", err)
		}
		if len(catalog.Profiles) == 0 {
			profiles, refreshErr := discoverCodexProfiles()
			if refreshErr != nil {
				die("models apply: %v", refreshErr)
			}
			if err := routing.SaveCatalog(absRoot, routing.ModelCatalog{UpdatedAt: time.Now().Unix(), Profiles: profiles}); err != nil {
				die("models apply: save catalog: %v", err)
			}
			catalog.Profiles = profiles
		}
		profile, err := findCodexProfile(catalog.Profiles, *model, *effort)
		if err != nil {
			die("models apply: %v", err)
		}
		path, err := applyCodexProjectProfile(absRoot, profile)
		if err != nil {
			die("models apply: %v", err)
		}
		fmt.Printf("models: applied Codex default -> %s + %s\n", profile.Model, profile.NativeEffort)
		fmt.Printf("  config: %s\n", path)
		fmt.Printf("  CLI:    codex %s\n", strings.Join(codexCLIArgs(profile, "<task>"), " "))
		fmt.Println("  Applies to new Desktop tasks and new CLI invocations; an existing task keeps its current model.")
	case "cleanup":
		apply := len(args) > 1 && args[1] == "--apply"
		home, err := claudeHomeDir()
		if err != nil {
			die("models cleanup: %v", err)
		}
		paths, err := cleanupStaleManagedArtifacts(home, apply)
		if err != nil {
			die("models cleanup: %v", err)
		}
		for _, path := range paths {
			fmt.Println(path)
		}
		if !apply {
			fmt.Printf("models cleanup: %d provably incomplete artifact(s); re-run with --apply to remove\n", len(paths))
		} else {
			fmt.Printf("models cleanup: removed %d provably incomplete artifact(s)\n", len(paths))
		}
	default:
		die("models: unknown subcommand %q (want refresh, list, or apply)", args[0])
	}
}
