package engine

import (
	"context"
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
	setupCommand = func(ctx context.Context, dir string, args ...string) (string, error) {
		switch args[0] {
		case "dpkg-query":
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
