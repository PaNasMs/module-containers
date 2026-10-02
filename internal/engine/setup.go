package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type Check struct {
	Engine       string   `json:"engine"`
	Compose      string   `json:"compose"`
	Reachable    bool     `json:"reachable"`
	Compatible   bool     `json:"compatible"`
	Problem      string   `json:"problem"`
	Missing      []string `json:"missing"`
	CanInstall   bool     `json:"canInstall"`
	CanStart     bool     `json:"canStart"`
	DataRoot     string   `json:"dataRoot"`
	Architecture string   `json:"architecture"`
}

var setupCommand = command
var setupMount = storageMount
var dockerConfigPath = "/etc/docker/daemon.json"
var dockerStorageGuardPath = "/etc/systemd/system/docker.service.d/panasms-storage.conf"
var dockerDefaultRoot = "/var/lib/docker"

var versionRE = regexp.MustCompile(`v?(\d+)\.(\d+)`)

func atLeast(s string, major, minor int) bool {
	m := versionRE.FindStringSubmatch(s)
	if len(m) != 3 {
		return false
	}
	a, _ := strconv.Atoi(m[1])
	b, _ := strconv.Atoi(m[2])
	return a > major || a == major && b >= minor
}
func installed(ctx context.Context, p string) bool {
	s, err := setupCommand(ctx, "", "dpkg-query", "-W", "-f=${db:Status-Status}", p)
	return err == nil && s == "installed"
}
func composePackage(ctx context.Context, vendor bool) string {
	if vendor {
		return "docker-compose-plugin"
	}
	for _, name := range []string{"docker-compose-v2", "docker-compose"} {
		policy, err := setupCommand(ctx, "", "apt-cache", "policy", name)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(policy, "\n") {
			if value, ok := strings.CutPrefix(strings.TrimSpace(line), "Candidate:"); ok && atLeast(value, 2, 20) {
				return name
			}
		}
	}
	return ""
}

func (e *Engine) setupRunning() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, job := range e.jobs {
		if job.Action == "setup" && (job.Status == "running" || job.Status == "queued") {
			return true
		}
	}
	return false
}

func (e *Engine) Check(parent context.Context) (c Check) {
	defer func() {
		if _, err := os.Stat(filepath.Join(e.root, "setup.json")); err == nil && (c.Compatible || c.CanStart || c.CanInstall) {
			c.CanInstall = true
			c.Problem = "Docker installation was interrupted. Retry setup to resume the saved configuration."
			if e.setupRunning() {
				c.Problem = "Installing Docker components"
			}
		}
	}()
	ctx, cancel := context.WithTimeout(parent, 8*time.Second)
	defer cancel()
	c = Check{Missing: []string{}}
	var v struct {
		Version    string
		Os         string
		Arch       string
		APIVersion string
	}
	err := e.docker.Call(ctx, "GET", "/version", nil, &v)
	if err == nil {
		c.Engine = v.Version
		c.Reachable = true
		c.Architecture = v.Arch
		if v.Os != "linux" || !atLeast(v.Version, 24, 0) {
			c.Problem = "Requires Linux Docker Engine 24 or newer. Existing installation was not changed."
			return c
		}
		var info struct{ DockerRootDir string }
		if e.docker.Call(ctx, "GET", "/info", nil, &info) == nil {
			c.DataRoot = info.DockerRootDir
		}
	}
	_, cliErr := exec.LookPath("docker")
	if installed(ctx, "podman-docker") {
		c.Problem = "podman-docker is installed. This module requires Docker Engine; automatic replacement is disabled."
		return c
	}
	if cliErr != nil {
		if installed(ctx, "docker-ce") {
			c.Missing = append(c.Missing, "docker-ce-cli")
		} else if installed(ctx, "docker.io") {
			c.Missing = append(c.Missing, "docker-cli")
		} else {
			c.Missing = append(c.Missing, "docker.io")
		}
	} else if !c.Reachable {
		if installed(ctx, "docker.io") || installed(ctx, "docker-ce") {
			c.Problem = "Docker Engine is stopped or its local socket is unavailable."
			c.CanStart = true
		} else {
			c.Problem = "Docker CLI exists without a reachable local Engine. Review the existing installation; it will not be replaced."
			return c
		}
	}
	compose, composeErr := setupCommand(ctx, "", "docker", "compose", "version", "--short")
	if composeErr == nil {
		c.Compose = strings.TrimSpace(compose)
		if !atLeast(c.Compose, 2, 20) {
			c.Problem = "Requires Docker Compose 2.20 or newer. Upgrade the existing Compose installation."
			return c
		}
	} else {
		if installed(ctx, "docker-compose") || installed(ctx, "docker-compose-v2") || installed(ctx, "docker-compose-plugin") {
			c.Problem = "Compose is installed but its Docker plugin is unavailable or incompatible. Review the installation."
			return c
		}
		pkg := composePackage(ctx, installed(ctx, "docker-ce"))
		if pkg == "" {
			c.Problem = "Docker Compose 2.20 or newer is unavailable in configured APT repositories."
			return c
		}
		c.Missing = append(c.Missing, pkg)
	}
	if len(c.Missing) > 0 {
		c.CanInstall = true
		for _, p := range c.Missing {
			policy, err := setupCommand(ctx, "", "apt-cache", "policy", p)
			if err != nil || !strings.Contains(policy, "Candidate:") || strings.Contains(policy, "Candidate: (none)") {
				c.CanInstall = false
				c.Problem = "Required packages are unavailable in configured APT repositories: " + strings.Join(c.Missing, ", ")
				return c
			}
		}
		c.Problem = "Missing components: " + strings.Join(c.Missing, ", ")
		return c
	}
	c.Compatible = c.Reachable && c.Compose != ""
	return c
}
func storageMount(ctx context.Context, root string) (string, error) {
	if !filepath.IsAbs(root) || strings.ContainsAny(root, "\n\r\t%") || strings.Contains(root, " ") {
		return "", errors.New("Choose an absolute local storage path without whitespace or percent signs")
	}
	parent := root
	for {
		if _, err := os.Stat(parent); err == nil {
			break
		}
		next := filepath.Dir(parent)
		if next == parent {
			return "", errors.New("Storage parent unavailable")
		}
		parent = next
	}
	resolved, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return "", err
	}
	if resolved != parent {
		return "", errors.New("Choose the real storage path, not a symlink")
	}
	raw, err := setupCommand(ctx, "", "findmnt", "--json", "--target", parent, "--output", "TARGET,SOURCE,FSTYPE,OPTIONS")
	if err != nil {
		return "", err
	}
	var m struct {
		Filesystems []struct{ Target, Source, Fstype, Options string }
	}
	if json.Unmarshal([]byte(raw), &m) != nil || len(m.Filesystems) != 1 {
		return "", errors.New("Cannot identify storage")
	}
	fs := m.Filesystems[0]
	if fs.Target == "/" || fs.Target == "/boot" || fs.Target == "/boot/firmware" || !strings.HasPrefix(fs.Source, "/dev/") || !(fs.Fstype == "ext4" || fs.Fstype == "xfs" || fs.Fstype == "btrfs") || !strings.Contains(","+fs.Options+",", ",rw,") {
		return "", errors.New("Choose a mounted writable local data filesystem, not the system disk or a network share")
	}
	fstab, err := setupCommand(ctx, "", "findmnt", "--fstab", "--noheadings", "--output", "TARGET")
	if err != nil {
		return "", err
	}
	persist := false
	for _, s := range strings.Split(fstab, "\n") {
		if strings.TrimSpace(s) == fs.Target {
			persist = true
		}
	}
	if !persist {
		return "", errors.New("The filesystem must have a persistent mount configured in Storage")
	}
	blocks, err := setupCommand(ctx, "", "lsblk", "--inverse", "--noheadings", "--output", "RM,TRAN", fs.Source)
	if err != nil {
		return "", err
	}
	for _, l := range strings.Split(blocks, "\n") {
		f := strings.Fields(l)
		if len(f) > 0 && (f[0] == "1" || len(f) > 1 && f[1] == "usb") {
			return "", errors.New("Removable and USB storage cannot be used for Docker data in this preview")
		}
	}
	if root == fs.Target {
		return "", errors.New("Choose a dedicated subfolder for Docker data")
	}
	return fs.Target, nil
}
func (e *Engine) Setup(ctx context.Context, a Action) error {
	journal := filepath.Join(e.root, "setup.json")
	var pending struct {
		Root  string
		Mount string
	}
	raw, readErr := os.ReadFile(journal)
	resume := readErr == nil
	if readErr != nil && !os.IsNotExist(readErr) {
		return readErr
	}
	if resume && (json.Unmarshal(raw, &pending) != nil || pending.Root == "" || pending.Mount == "") {
		return errors.New("Docker setup journal is invalid; review the saved configuration")
	}
	if resume {
		a.Root = pending.Root
	}
	status := e.Check(ctx)
	if !status.CanInstall || !resume && len(status.Missing) == 0 {
		return fmt.Errorf("No automatic installation available: %s", status.Problem)
	}
	fresh := resume || !installed(ctx, "docker.io") && !installed(ctx, "docker-ce")
	mount := ""
	var err error
	if fresh {
		if _, err = os.Stat(dockerConfigPath); !resume && !os.IsNotExist(err) {
			return errors.New("Existing Docker daemon configuration found. Review it before installing; no configuration was replaced")
		}
		if entries, err := os.ReadDir(dockerDefaultRoot); !resume && err == nil && len(entries) > 0 {
			return errors.New("Existing Docker data found. Automatic fresh installation is disabled")
		}
		mount, err = setupMount(ctx, a.Root)
		if err != nil {
			return err
		}
		if entries, err := os.ReadDir(a.Root); !resume && err == nil && len(entries) > 0 {
			return errors.New("Docker data directory must be empty for a new installation")
		}
	}
	e.stage(a.ID, "Checking package changes")
	args := append([]string{"apt-get", "--simulate", "--no-remove", "install"}, status.Missing...)
	simulation, err := setupCommand(ctx, "", args...)
	if err != nil {
		return err
	}
	if strings.Contains(simulation, "Remv ") {
		return errors.New("Package installation would remove existing components")
	}
	for _, line := range strings.Split(simulation, "\n") {
		if packageUpgrade(line) {
			return errors.New("Package installation requires upgrading existing components; review system updates first")
		}
	}
	if fresh {
		desired := map[string]any{"data-root": a.Root, "log-driver": "local", "log-opts": map[string]string{"max-size": "10m", "max-file": "3"}}
		unit := "[Unit]\nRequiresMountsFor=" + a.Root + "\nConditionPathIsMountPoint=" + mount + "\n"
		if resume {
			if err = verifySetupFile(dockerConfigPath, desired); err != nil {
				return err
			}
			if raw, err := os.ReadFile(dockerStorageGuardPath); err == nil && string(raw) != unit {
				return errors.New("Docker storage guard changed since interrupted setup; no configuration replaced")
			} else if err != nil && !os.IsNotExist(err) {
				return err
			}
		} else {
			if _, err := os.Lstat(dockerStorageGuardPath); !os.IsNotExist(err) {
				return errors.New("Existing Docker storage guard found; no configuration replaced")
			}
			pending.Root = a.Root
			pending.Mount = mount
			if err = atomic(journal, pending); err != nil {
				return err
			}
		}
		if err = os.MkdirAll(a.Root, 0710); err != nil {
			return err
		}
		if err = os.MkdirAll(filepath.Dir(dockerConfigPath), 0755); err != nil {
			return err
		}
		if err = atomic(dockerConfigPath, map[string]any{"data-root": a.Root, "log-driver": "local", "log-opts": map[string]string{"max-size": "10m", "max-file": "3"}}); err != nil {
			return err
		}
		path := filepath.Dir(dockerStorageGuardPath)
		if err = os.MkdirAll(path, 0755); err != nil {
			return err
		}
		if err = os.WriteFile(dockerStorageGuardPath, []byte(unit), 0644); err != nil {
			return err
		}
		if _, err = setupCommand(ctx, "", "systemctl", "daemon-reload"); err != nil {
			return err
		}
	}
	e.stage(a.ID, "Installing Docker components")
	args = append([]string{"apt-get", "-y", "--no-remove", "install"}, status.Missing...)
	if len(status.Missing) > 0 {
		if _, err = setupCommand(ctx, "", args...); err != nil {
			return fmt.Errorf("Package installation failed; retry setup to resume the saved configuration: %w", err)
		}
	}
	if fresh {
		if _, err = setupCommand(ctx, "", "systemctl", "enable", "--now", "docker"); err != nil {
			return err
		}
	}
	status = e.Check(ctx)
	if !status.Compatible && !status.CanStart {
		return fmt.Errorf("Components installed but Docker is not ready: %s", status.Problem)
	}
	if fresh {
		if err = os.Remove(journal); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func verifySetupFile(path string, expected any) error {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var actual any
	if json.Unmarshal(raw, &actual) != nil {
		return errors.New("Docker configuration is invalid")
	}
	want, _ := json.Marshal(expected)
	got, _ := json.Marshal(actual)
	if string(want) != string(got) {
		return errors.New("Docker configuration changed since interrupted setup; no configuration replaced")
	}
	return nil
}

func packageUpgrade(line string) bool {
	f := strings.Fields(line)
	return len(f) > 2 && f[0] == "Inst" && strings.HasPrefix(f[2], "[")
}
