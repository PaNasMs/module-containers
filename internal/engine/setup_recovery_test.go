package engine

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type setupTransport func(*http.Request) (*http.Response, error)

func (f setupTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestSetupCanResumeAfterAptFailure(t *testing.T) {
	oldCommand, oldMount := setupCommand, setupMount
	oldConfig, oldGuard, oldRoot := dockerConfigPath, dockerStorageGuardPath, dockerDefaultRoot
	t.Cleanup(func() {
		setupCommand = oldCommand
		setupMount = oldMount
		dockerConfigPath = oldConfig
		dockerStorageGuardPath = oldGuard
		dockerDefaultRoot = oldRoot
	})
	root := t.TempDir()
	t.Setenv("PATH", root)
	dockerConfigPath = filepath.Join(root, "etc/docker/daemon.json")
	dockerStorageGuardPath = filepath.Join(root, "etc/systemd/docker.service.d/storage.conf")
	dockerDefaultRoot = filepath.Join(root, "var/docker")
	setupMount = func(context.Context, string) (string, error) { return "/srv", nil }
	ready, failInstall := false, true
	incompatible := false
	setupCommand = func(ctx context.Context, dir string, args ...string) (string, error) {
		switch args[0] {
		case "dpkg-query":
			if incompatible && args[len(args)-1] == "podman-docker" {
				return "installed", nil
			}
			return "", errors.New("not installed")
		case "docker":
			if ready {
				return "2.26.1", nil
			}
			return "", errors.New("missing compose")
		case "apt-cache":
			return "Candidate: 26.1", nil
		case "apt-get":
			if args[1] == "--simulate" {
				return "Inst docker.io (26.1)\n", nil
			}
			config, err := os.ReadFile(dockerConfigPath)
			var settings struct{ Features map[string]bool }
			if err != nil || json.Unmarshal(config, &settings) != nil {
				t.Fatal("storage configuration missing before package installation", err)
			}
			if enabled, exists := settings.Features["containerd-snapshotter"]; !exists || enabled {
				t.Fatal("Docker 29 would put image snapshots outside the selected data root")
			}
			if failInstall {
				return "", errors.New("injected APT failure")
			}
			os.WriteFile(filepath.Join(root, "docker"), []byte("#!/bin/sh\n"), 0700)
			ready = true
			return "", nil
		case "systemctl":
			return "", nil
		default:
			t.Fatalf("unexpected command %v", args)
			return "", nil
		}
	}
	e := &Engine{root: root, docker: &Docker{Client: &http.Client{Transport: setupTransport(func(r *http.Request) (*http.Response, error) {
		if !ready {
			return nil, errors.New("not installed")
		}
		body := `{"Version":"26.1","Os":"linux","Arch":"arm64"}`
		if r.URL.Path == "/info" {
			body = `{"DockerRootDir":"/srv/docker"}`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
	})}}}
	action := Action{Root: filepath.Join(root, "data")}
	if e.Setup(context.Background(), action) == nil {
		t.Fatal("injected failure ignored")
	}
	before, err := os.ReadFile(dockerConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(root, "setup.json")); err != nil {
		t.Fatal("recovery journal missing")
	}
	e.jobs = []Job{{Action: "setup", Status: "running"}}
	if check := e.Check(context.Background()); check.Problem != "Installing Docker components" {
		t.Fatal("active setup reported interrupted", check)
	}
	e.jobs[0].Status = "failed"
	if check := e.Check(context.Background()); !strings.Contains(check.Problem, "interrupted") {
		t.Fatal("interrupted setup not reported", check)
	}
	incompatible = true
	if err = e.Setup(context.Background(), action); err == nil || !strings.Contains(err.Error(), "podman-docker") {
		t.Fatal("retry ignored incompatible installation", err)
	}
	incompatible = false
	failInstall = false
	if err = e.Setup(context.Background(), action); err != nil {
		t.Fatal("retry failed", err)
	}
	after, _ := os.ReadFile(dockerConfigPath)
	if string(after) != string(before) {
		t.Fatal("retry changed storage")
	}
	if _, err = os.Stat(filepath.Join(root, "setup.json")); !os.IsNotExist(err) {
		t.Fatal("completed journal retained")
	}
}

func TestComposePackageSelection(t *testing.T) {
	old := setupCommand
	t.Cleanup(func() { setupCommand = old })
	for _, tc := range []struct{ v2, legacy, want string }{
		{"2.40.3", "1.29.2", "docker-compose-v2"},
		{"(none)", "2.26.1", "docker-compose"},
		{"(none)", "1.29.2", ""},
		{"2.19.0", "2.20.0", "docker-compose"},
	} {
		setupCommand = func(_ context.Context, _ string, args ...string) (string, error) {
			v := tc.legacy
			if args[len(args)-1] == "docker-compose-v2" {
				v = tc.v2
			}
			return "  Candidate: " + v, nil
		}
		if got := composePackage(context.Background(), false); got != tc.want {
			t.Fatalf("%+v: got %q", tc, got)
		}
		if got := composePackage(context.Background(), true); got != "docker-compose-plugin" {
			t.Fatal(got)
		}
	}
}

func TestSetupDockerCLIPackageSplit(t *testing.T) {
	old := setupCommand
	t.Cleanup(func() { setupCommand = old })
	for _, tc := range []struct {
		name, vendor        string
		cli, split, compose bool
		missing             string
		install             bool
	}{
		{name: "fresh Debian 13", split: true, missing: "docker.io,docker-cli,docker-compose", install: true},
		{name: "fresh Ubuntu bundled CLI", missing: "docker.io,docker-compose", install: true},
		{name: "resume Debian without CLI", vendor: "docker.io", split: true, compose: true, missing: "docker-cli", install: true},
		{name: "vendor without CLI", vendor: "docker-ce", compose: true, missing: "docker-ce-cli", install: true},
		{name: "broken plugin with CLI", vendor: "docker.io", cli: true, compose: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("PATH", root)
			if tc.cli {
				if err := os.WriteFile(filepath.Join(root, "docker"), []byte("#!/bin/sh\n"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			setupCommand = func(_ context.Context, _ string, args ...string) (string, error) {
				name := args[len(args)-1]
				switch args[0] {
				case "dpkg-query":
					if name == tc.vendor || tc.compose && name == "docker-compose" {
						return "installed", nil
					}
					return "", errors.New("not installed")
				case "apt-cache":
					if name == "docker-compose-v2" || name == "docker-cli" && !tc.split {
						return "Candidate: (none)", nil
					}
					return "Candidate: 26.1.5", nil
				case "docker":
					return "", errors.New("CLI or plugin unavailable")
				}
				t.Fatalf("unexpected command %v", args)
				return "", nil
			}
			e := &Engine{root: root, docker: &Docker{Client: &http.Client{Transport: setupTransport(func(r *http.Request) (*http.Response, error) {
				if tc.vendor == "" {
					return nil, errors.New("not installed")
				}
				body := `{"Version":"26.1","Os":"linux","Arch":"arm64"}`
				if r.URL.Path == "/info" {
					body = `{"DockerRootDir":"/srv/docker"}`
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
			})}}}
			check := e.Check(context.Background())
			if got := strings.Join(check.Missing, ","); got != tc.missing || check.CanInstall != tc.install || check.Compatible {
				t.Fatalf("unexpected setup plan: %+v", check)
			}
		})
	}
}

func TestSlowDockerSocketDoesNotHideInstalledPackages(t *testing.T) {
	old := setupCommand
	t.Cleanup(func() { setupCommand = old })
	t.Setenv("PATH", t.TempDir())
	setupCommand = func(ctx context.Context, _ string, args ...string) (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		name := args[len(args)-1]
		switch args[0] {
		case "dpkg-query":
			if name == "docker.io" || name == "docker-compose" {
				return "installed", nil
			}
			return "", errors.New("not installed")
		case "docker":
			return "", errors.New("CLI missing")
		case "apt-cache":
			return "Candidate: 26.1.5", nil
		}
		t.Fatalf("unexpected command %v", args)
		return "", nil
	}
	e := &Engine{root: t.TempDir(), docker: &Docker{Client: &http.Client{Transport: setupTransport(func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}}}
	check := e.Check(context.Background())
	if check.Reachable || !check.CanInstall || strings.Join(check.Missing, ",") != "docker-cli" {
		t.Fatalf("slow socket hid available CLI recovery: %+v", check)
	}
}
