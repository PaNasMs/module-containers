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
	s, err := command(ctx, "", "dpkg-query", "-W", "-f=${db:Status-Status}", p)
	return err == nil && s == "installed"
}
func (e *Engine) Check(parent context.Context) Check {
	ctx, cancel := context.WithTimeout(parent, 8*time.Second)
	defer cancel()
	c := Check{Missing: []string{}}
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
	compose, composeErr := command(ctx, "", "docker", "compose", "version", "--short")
	if composeErr == nil {
		c.Compose = strings.TrimSpace(compose)
		if !atLeast(c.Compose, 2, 20) {
			c.Problem = "Requires Docker Compose 2.20 or newer. Upgrade the existing Compose installation."
			return c
		}
	} else {
		if installed(ctx, "docker-compose") || installed(ctx, "docker-compose-plugin") {
			c.Problem = "Compose is installed but its Docker plugin is unavailable or incompatible. Review the installation."
			return c
		}
		if installed(ctx, "docker-ce") {
			c.Missing = append(c.Missing, "docker-compose-plugin")
		} else {
			c.Missing = append(c.Missing, "docker-compose")
		}
	}
	if len(c.Missing) > 0 {
		c.CanInstall = true
		for _, p := range c.Missing {
			policy, err := command(ctx, "", "apt-cache", "policy", p)
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
	raw, err := command(ctx, "", "findmnt", "--json", "--target", parent, "--output", "TARGET,SOURCE,FSTYPE,OPTIONS")
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
	fstab, err := command(ctx, "", "findmnt", "--fstab", "--noheadings", "--output", "TARGET")
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
	blocks, err := command(ctx, "", "lsblk", "--inverse", "--noheadings", "--output", "RM,TRAN", fs.Source)
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
	status := e.Check(ctx)
	if !status.CanInstall || len(status.Missing) == 0 {
		return fmt.Errorf("No automatic installation available: %s", status.Problem)
	}
	fresh := !installed(ctx, "docker.io") && !installed(ctx, "docker-ce")
	mount := ""
	var err error
	if fresh {
		if _, err = os.Stat("/etc/docker/daemon.json"); !os.IsNotExist(err) {
			return errors.New("Existing Docker daemon configuration found. Review it before installing; no configuration was replaced")
		}
		if entries, err := os.ReadDir("/var/lib/docker"); err == nil && len(entries) > 0 {
			return errors.New("Existing Docker data found. Automatic fresh installation is disabled")
		}
		mount, err = storageMount(ctx, a.Root)
		if err != nil {
			return err
		}
		if entries, err := os.ReadDir(a.Root); err == nil && len(entries) > 0 {
			return errors.New("Docker data directory must be empty for a new installation")
		}
	}
	e.stage(a.ID, "Checking package changes")
	args := append([]string{"apt-get", "--simulate", "--no-remove", "install"}, status.Missing...)
	simulation, err := command(ctx, "", args...)
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
		if err = os.MkdirAll(a.Root, 0710); err != nil {
			return err
		}
		if err = os.MkdirAll("/etc/docker", 0755); err != nil {
			return err
		}
		if err = atomic("/etc/docker/daemon.json", map[string]any{"data-root": a.Root, "log-driver": "local", "log-opts": map[string]string{"max-size": "10m", "max-file": "3"}}); err != nil {
			return err
		}
		path := "/etc/systemd/system/docker.service.d"
		if err = os.MkdirAll(path, 0755); err != nil {
			return err
		}
		unit := "[Unit]\nRequiresMountsFor=" + a.Root + "\nConditionPathIsMountPoint=" + mount + "\n"
		if err = os.WriteFile(path+"/panasms-storage.conf", []byte(unit), 0644); err != nil {
			return err
		}
		if _, err = command(ctx, "", "systemctl", "daemon-reload"); err != nil {
			return err
		}
	}
	e.stage(a.ID, "Installing Docker components")
	args = append([]string{"apt-get", "-y", "--no-remove", "install"}, status.Missing...)
	if _, err = command(ctx, "", args...); err != nil {
		return fmt.Errorf("Package installation failed; any new storage configuration was preserved for review: %w", err)
	}
	if fresh {
		if _, err = command(ctx, "", "systemctl", "enable", "--now", "docker"); err != nil {
			return err
		}
	}
	status = e.Check(ctx)
	if !status.Compatible && !status.CanStart {
		return fmt.Errorf("Components installed but Docker is not ready: %s", status.Problem)
	}
	return nil
}

func packageUpgrade(line string) bool {
	f := strings.Fields(line)
	return len(f) > 2 && f[0] == "Inst" && strings.HasPrefix(f[2], "[")
}
