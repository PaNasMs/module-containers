package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestComposeInputsCannotLoadHostFiles(t *testing.T) {
	for _, src := range []string{"include: [/etc/private]\nservices: {}", "x-template: &template {env_file: /etc/private}\nservices: {web: {<<: *template}}", "services: {web: {extends: {file: /etc/private}}}", "services: {web: {image: example}}\nsecrets: {token: {file: /etc/private}}", "services: {web: {provider: {type: command}}}"} {
		if checkSource(src, "") == nil {
			t.Errorf("accepted unsupported source %s", src)
		}
	}
	if checkSource("services: {web: {image: nginx}}", "COMPOSE_FILE=/other") == nil {
		t.Fatal("accepted CLI control variable")
	}
}
func TestComposeSourceAndLegacyRevision(t *testing.T) {
	dir := t.TempDir()
	s := savedWorkspace{projectDraft: projectDraft{Compose: "# a comment\nservices: {}\n", Env: "TAG=test"}, Config: map[string]any{"literal": "$TAG"}}
	if err := atomic(filepath.Join(dir, "compose.json"), s.Config); err != nil {
		t.Fatal(err)
	}
	old, r1, err := readProject(dir)
	if err != nil || !strings.Contains(old.Compose, "$$TAG") {
		t.Fatal(old, r1, err)
	}
	if err = atomic(filepath.Join(dir, "workspace.json"), s); err != nil {
		t.Fatal(err)
	}
	got, r2, err := readProject(dir)
	if err != nil || r1 == r2 || got.Compose != s.Compose || got.Env != s.Env {
		t.Fatal(got, r2, err)
	}
	if b, _ := json.Marshal(literalConfig(got.Config)); !strings.Contains(string(b), "$$TAG") {
		t.Fatal(string(b))
	}
	if stat, err := os.Stat(filepath.Join(dir, "workspace.json")); err != nil || stat.Mode().Perm() != 0600 {
		t.Fatal(err)
	}
}

func TestInstalledComposePlan(t *testing.T) {
	if os.Getenv("PANASMS_COMPOSE_QA") != "1" {
		t.Skip("requires installed Docker and Compose")
	}
	engine, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	draft := Action{Target: "panasms-editor-preflight-qa", Env: "TAG=alpine", Directory: dir, Compose: "# retain\nx-base: &base\n  image: nginx:${TAG}\nservices:\n  web:\n    <<: *base\n    volumes: ['./:/data:ro']\n    ports: ['127.0.0.1:37521:80']\n"}
	out, plan, err := engine.prepareProject(context.Background(), draft)
	if err != nil {
		t.Fatal(err)
	}
	_, again, err := engine.prepareProject(context.Background(), draft)
	if err != nil || plan.Token != again.Token {
		t.Fatal("unstable plan", err)
	}
	if len(plan.Services) != 1 || plan.Services[0].Image != "nginx:alpine" || !strings.Contains(plan.Services[0].Mounts[0], dir) {
		t.Fatalf("unexpected plan: %+v", plan)
	}
	draft.Plan = "obsolete"
	if err = engine.applyProject(context.Background(), draft); err == nil {
		t.Fatal("stale plan accepted")
	}
	if _, err = os.Stat(filepath.Join(engine.root, "projects", draft.Target)); !os.IsNotExist(err) {
		t.Fatal("stale plan created project")
	}
	if out.Compose != draft.Compose {
		t.Fatal("original source changed")
	}
}
