package store

import (
	"reflect"
	"testing"
)

func TestResolveCompletion(t *testing.T) {
	// Empty store → default integration, declared=false.
	m := NewMem()
	if spec, declared := ResolveCompletion(m); declared || spec.Name != "claude-code" || spec.Transport != TransportCLI {
		t.Fatalf("empty store: got %+v declared=%v, want default claude-code/false", spec, declared)
	}

	// Declare an OpenAI-compatible integration and make it active.
	want := CompletionSpec{
		Name: "openrouter", Transport: TransportHTTPOpenAI,
		BaseURL: "https://openrouter.ai/api/v1", APIKeyEnv: "PROJX_TRIAGE_API_KEY",
		Model: "anthropic/claude-haiku-4.5",
	}
	if err := m.Put(IntegrationRecord(want)); err != nil {
		t.Fatal(err)
	}
	if err := m.Put(IntegrationActiveRecord("openrouter")); err != nil {
		t.Fatal(err)
	}
	got, declared := ResolveCompletion(m)
	if !declared {
		t.Fatal("declared should be true after seeding an active integration")
	}
	if got.Transport != TransportHTTPOpenAI || got.BaseURL != want.BaseURL || got.APIKeyEnv != want.APIKeyEnv || got.Model != want.Model {
		t.Errorf("resolved %+v, want %+v", got, want)
	}
}

func TestRenderCLIArgs(t *testing.T) {
	// {prompt}/{model} substitute as WHOLE argv elements — a spaced prompt stays one arg.
	got := RenderCLIArgs("claude -p {prompt} --model {model}", "rename the foo bar", "haiku")
	want := []string{"claude", "-p", "rename the foo bar", "--model", "haiku"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("RenderCLIArgs = %#v, want %#v", got, want)
	}
	if RenderCLIArgs("", "x", "y") != nil {
		t.Error("empty template should render nil")
	}
}
