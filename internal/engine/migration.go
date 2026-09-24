package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Migration struct {
	OriginalDropIn []byte   `json:"originalDropIn,omitempty"`
	HadDropIn      bool     `json:"hadDropIn"`
	Old            string   `json:"old"`
	New            string   `json:"new"`
	Mount          string   `json:"mount"`
	Running        []string `json:"running"`
	Phase          string   `json:"phase"`
}

func (e *Engine) migration() (*Migration, error) {
	raw, err := os.ReadFile(filepath.Join(e.root, "migration.json"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var m Migration
	if err = json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return &m, nil
}
func (e *Engine) writeMigration(m *Migration) error {
	return atomic(filepath.Join(e.root, "migration.json"), m)
}
func readDaemon() (map[string]any, error) {
	v := map[string]any{}
	raw, err := os.ReadFile("/etc/docker/daemon.json")
	if os.IsNotExist(err) {
		return v, nil
	}
	if err != nil {
		return nil, err
	}
	err = json.Unmarshal(raw, &v)
	return v, err
}
func within(a, b string) bool {
	return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}
func (e *Engine) move(ctx context.Context, a Action) error {
	if m, err := e.migration(); err != nil {
		return err
	} else if m != nil {
		return errors.New("An interrupted storage transfer requires recovery first")
	}
	var info struct {
		DockerRootDir      string
		LiveRestoreEnabled bool
		DriverStatus       [][]string
	}
	if err := e.docker.Call(ctx, "GET", "/info", nil, &info); err != nil {
		return err
	}
	if info.LiveRestoreEnabled {
		return errors.New("Disable Docker live-restore before moving its data directory")
	}
	for _, row := range info.DriverStatus {
		for _, value := range row {
			if strings.Contains(value, "containerd.snapshotter") {
				return errors.New("Moving a separate containerd image store is not supported in this preview; the current Docker installation was not changed")
			}
		}
	}
	destination := filepath.Clean(a.Root)
	source := filepath.Clean(info.DockerRootDir)
	if within(source, destination) {
		return errors.New("Source and destination must be separate directories")
	}
	mount, err := storageMount(ctx, destination)
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(destination)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if len(entries) > 0 {
		return errors.New("Destination folder must be empty")
	}
	if _, err = command(ctx, "", "rsync", "--version"); err != nil {
		return errors.New("Install rsync before moving Docker data")
	}
	cfg, err := readDaemon()
	if err != nil {
		return err
	}
	if configured, ok := cfg["data-root"].(string); ok && filepath.Clean(configured) != source {
		return errors.New("Docker daemon settings differ from the running data directory. Reconcile them first")
	}
	var cs []Container
	if err = e.docker.Call(ctx, "GET", "/containers/json", nil, &cs); err != nil {
		return err
	}
	dropIn, dropErr := os.ReadFile("/etc/systemd/system/docker.service.d/panasms-storage.conf")
	if dropErr != nil && !os.IsNotExist(dropErr) {
		return dropErr
	}
	m := &Migration{OriginalDropIn: dropIn, HadDropIn: dropErr == nil, Old: source, New: destination, Mount: mount, Running: []string{}, Phase: "stopping"}
	for _, c := range cs {
		m.Running = append(m.Running, c.ID)
	}
	if err = e.writeMigration(m); err != nil {
		return err
	}
	e.stage(a.ID, "Stopping containers for storage transfer")
	for _, id := range m.Running {
		if err = e.docker.Call(ctx, "POST", "/containers/"+id+"/stop?t=30", nil, nil); err != nil {
			return err
		}
	}
	if _, err = command(ctx, "", "systemctl", "stop", "docker.socket", "docker.service"); err != nil {
		return err
	}
	m.Phase = "copying"
	if err = e.writeMigration(m); err != nil {
		return err
	}
	e.stage(a.ID, "Copying Docker data; original files are preserved")
	if err = os.MkdirAll(destination, 0710); err != nil {
		return err
	}
	if _, err = command(ctx, "", "rsync", "-aHAXS", "--numeric-ids", "--", source+"/", destination+"/"); err != nil {
		return fmt.Errorf("Transfer failed; original data retained. Use Recover: %w", err)
	}
	cfg["data-root"] = destination
	if err = os.MkdirAll("/etc/systemd/system/docker.service.d", 0755); err != nil {
		return err
	}
	if err = os.WriteFile("/etc/systemd/system/docker.service.d/panasms-storage.conf", []byte("[Unit]\nRequiresMountsFor="+destination+"\nConditionPathIsMountPoint="+mount+"\n"), 0644); err != nil {
		return err
	}
	if err = atomic("/etc/docker/daemon.json", cfg); err != nil {
		return err
	}
	m.Phase = "switched"
	if err = e.writeMigration(m); err != nil {
		return err
	}
	e.stage(a.ID, "Starting Docker at the new location")
	return e.recoverMove(ctx)
}
func (e *Engine) recoverMove(ctx context.Context) error {
	m, err := e.migration()
	if err != nil {
		return err
	}
	if m == nil {
		return errors.New("No interrupted transfer")
	}
	cfg, err := readDaemon()
	if err != nil {
		return err
	}
	expected := m.Old
	if cfg["data-root"] == m.New {
		expected = m.New
		if _, err = storageMount(ctx, m.New); err != nil {
			return err
		}
	} else {
		// Before the configuration switch, only the untouched source is authoritative.
		path := "/etc/systemd/system/docker.service.d/panasms-storage.conf"
		if m.HadDropIn {
			if err = os.WriteFile(path, m.OriginalDropIn, 0644); err != nil {
				return err
			}
		} else if err = os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}

	}
	if _, err = command(ctx, "", "systemctl", "daemon-reload"); err != nil {
		return err
	}
	if _, err = command(ctx, "", "systemctl", "start", "docker"); err != nil {
		return err
	}
	var info struct{ DockerRootDir string }
	if err = e.docker.Call(ctx, "GET", "/info", nil, &info); err != nil {
		return err
	}
	if filepath.Clean(info.DockerRootDir) != filepath.Clean(expected) {
		return errors.New("Docker started with an unexpected data directory; transfer remains pending")
	}
	for _, id := range m.Running {
		var c struct{ State struct{ Running bool } }
		if err = e.docker.Call(ctx, "GET", "/containers/"+id+"/json", nil, &c); err != nil {
			return err
		}
		if !c.State.Running {
			if err = e.docker.Call(ctx, "POST", "/containers/"+id+"/start", nil, nil); err != nil {
				return err
			}
		}
	}
	return os.Rename(filepath.Join(e.root, "migration.json"), filepath.Join(e.root, "last-migration.json"))
}
