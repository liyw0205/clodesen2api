package modelpolicy

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"clodesen2api/internal/controlstore"
	"clodesen2api/internal/openai"
)

func TestMappingChainUsesTerminalAndAllCapabilities(t *testing.T) {
	noStream := false
	s := Snapshot{Models: []Rule{
		{SourceID: "a", PublicID: "alias", UpstreamID: "b", Enabled: true},
		{SourceID: "b", PublicID: "b", UpstreamID: "c", Enabled: true},
		{SourceID: "c", PublicID: "c", UpstreamID: "c", Enabled: true, Endpoints: []string{openai.ChatCompletionsPath}, Stream: &noStream},
	}}
	rule, err := s.Resolve("alias", openai.ChatCompletionsPath, false)
	if err != nil || rule.SourceID != "a" || rule.UpstreamID != "c" {
		t.Fatalf("%+v %v", rule, err)
	}
	if _, err := s.Resolve("alias", openai.GenerationsPath, false); err == nil {
		t.Fatal("terminal endpoint restriction bypassed")
	}
	if _, err := s.Resolve("alias", openai.ChatCompletionsPath, true); err == nil {
		t.Fatal("terminal stream restriction bypassed")
	}
	s.Models[2].Enabled = false
	if s.Available(s.Models[0]) {
		t.Fatal("terminal disable bypassed")
	}
	if _, err := s.Resolve("alias", openai.ChatCompletionsPath, false); err == nil {
		t.Fatal("disabled terminal resolved")
	}
	s.Models[2].Enabled = true
	s.Models[2].UpstreamID = "a"
	if s.Available(s.Models[0]) {
		t.Fatal("cycle accepted")
	}
}

func TestPolicyNamespaceConflictCapabilitiesAndRestart(t *testing.T) {
	ctx := context.Background()
	db, err := controlstore.Open(filepath.Join(t.TempDir(), "control.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := Open(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Register(ctx, []string{"source-a", "source-b"}); err != nil {
		t.Fatal(err)
	}
	snap, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	noStream := false
	rule := Rule{SourceID: "source-a", PublicID: "public-a", UpstreamID: "source-b", Enabled: true, Endpoints: []string{openai.ChatCompletionsPath}, Stream: &noStream}
	updated, err := store.Put(ctx, snap.Revision, rule)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put(ctx, snap.Revision, rule); !errors.Is(err, controlstore.ErrConflict) {
		t.Fatalf("stale: %v", err)
	}
	if got, err := updated.Resolve("public-a", openai.ChatCompletionsPath, false); err != nil || got.SourceID != "source-a" || got.UpstreamID != "source-b" {
		t.Fatalf("resolved %+v %v", got, err)
	}
	for _, check := range []struct {
		name, path string
		stream     bool
	}{{"source-a", openai.ChatCompletionsPath, false}, {"PUBLIC-A", openai.ChatCompletionsPath, false}, {"public-a", openai.GenerationsPath, false}, {"public-a", openai.ChatCompletionsPath, true}, {"", openai.ChatCompletionsPath, false}} {
		if _, err := updated.Resolve(check.name, check.path, check.stream); err == nil {
			t.Fatalf("allowed %+v", check)
		}
	}
	bad := rule
	bad.PublicID = "source-b"
	if _, err := store.Put(ctx, updated.Revision, bad); err == nil {
		t.Fatal("alias collision accepted")
	}
	bad = rule
	bad.UpstreamID = "missing"
	if _, err := store.Put(ctx, updated.Revision, bad); err == nil {
		t.Fatal("dangling upstream accepted")
	}
	if _, err := store.Put(ctx, updated.Revision, Rule{SourceID: "source-b", PublicID: "source-b", UpstreamID: "source-a", Enabled: true}); err == nil {
		t.Fatal("mapping cycle accepted")
	}
	rule.Enabled = false
	updated, err = store.Put(ctx, updated.Revision, rule)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(db)
	if err != nil {
		t.Fatal(err)
	}
	updated, err = reopened.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := updated.Resolve("public-a", openai.ChatCompletionsPath, false); err == nil {
		t.Fatal("disabled alias accepted after restart")
	}
}
