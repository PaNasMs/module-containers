package engine

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

type projectDraft struct {
	Compose   string `json:"compose"`
	Env       string `json:"env"`
	Directory string `json:"directory"`
}
type savedWorkspace struct {
	ImageIDs map[string]string `json:"imageIDs,omitempty"`
	projectDraft
	Config map[string]any `json:"config"`
}
type servicePlan struct {
	Name   string   `json:"name"`
	Image  string   `json:"image"`
	Ports  []string `json:"ports"`
	Mounts []string `json:"mounts"`
	Change string   `json:"change"`
}
type projectPlan struct {
	Images   []planImage   `json:"images"`
	Services []servicePlan `json:"services"`
	Token    string        `json:"token"`
}

func fingerprint(v any) string { b, _ := json.Marshal(v); return fmt.Sprintf("%x", sha256.Sum256(b)) }

func readProject(dir string) (savedWorkspace, string, error) {
	var w savedWorkspace
	data, err := os.ReadFile(filepath.Join(dir, "workspace.json"))
	if err == nil {
		err = json.Unmarshal(data, &w)
		return w, fingerprint(data), err
	}
	if !os.IsNotExist(err) {
		return w, "", err
	}
	data, err = os.ReadFile(filepath.Join(dir, "compose.json"))
	if err != nil {
		return w, "", err
	}
	err = json.Unmarshal(data, &w.Config)
	w.Compose = strings.ReplaceAll(string(data), "$", "$$")
	return w, fingerprint(data), err
}

// Check file-loading directives before handing a draft to the privileged Compose CLI.
func checkSource(source, env string) error {
	if len(source) > 256<<10 || len(env) > 64<<10 {
		return errors.New("Compose input is too large")
	}
	var doc map[string]any
	decoder := yaml.NewDecoder(strings.NewReader(source))
	if err := decoder.Decode(&doc); err != nil {
		return errors.New("Invalid Compose YAML")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errors.New("Use one Compose document")
	}
	if _, ok := doc["include"]; ok {
		return errors.New("External Compose files are not supported")
	}
	for _, section := range []string{"configs", "secrets"} {
		entries, _ := doc[section].(map[string]any)
		for _, entry := range entries {
			m, _ := entry.(map[string]any)
			if _, ok := m["file"]; ok {
				return errors.New("File-based secrets/configs are not supported")
			}
		}
	}
	services, _ := doc["services"].(map[string]any)
	if len(services) == 0 {
		return errors.New("Compose must contain services")
	}
	for name, entry := range services {
		m, _ := entry.(map[string]any)
		for _, field := range []string{"include", "extends", "env_file", "label_file", "build", "credential_spec", "provider", "use_api_socket", "post_start", "pre_stop", "develop"} {
			if _, ok := m[field]; ok {
				return fmt.Errorf("%s: %s is not supported in managed projects", name, field)
			}
		}
	}
	for _, line := range strings.Split(env, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, _, ok := strings.Cut(line, "=")
		if !ok || !envKey.MatchString(key) || strings.HasPrefix(key, "COMPOSE_") || strings.HasPrefix(key, "DOCKER_") {
			return errors.New("Use NAME=value lines in the environment file; Docker control variables are reserved")
		}
	}
	return nil
}

var envKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z_0-9]*$`)

func (e *Engine) prepareProject(ctx context.Context, a Action) (savedWorkspace, projectPlan, error) {
	var plan projectPlan
	w := savedWorkspace{projectDraft: projectDraft{a.Compose, a.Env, a.Directory}}
	if !nameRE.MatchString(a.Target) {
		return w, plan, errors.New(msgProjectNameRule)
	}
	if err := checkSource(a.Compose, a.Env); err != nil {
		return w, plan, err
	}
	dir := filepath.Join(e.root, "projects", a.Target)
	previous, revision, err := readProject(dir)
	if err != nil && !os.IsNotExist(err) {
		return w, plan, err
	}
	if revision != a.Revision {
		return w, plan, errors.New("Project changed. Reopen the editor and review your changes.")
	}
	var existing []Container
	if err = e.docker.Call(ctx, "GET", "/containers/json?all=1", nil, &existing); err != nil {
		return w, plan, err
	}
	for _, c := range existing {
		if c.Labels["com.docker.compose.project"] == a.Target && revision == "" {
			return w, plan, errors.New(msgExternalProject)
		}
	}
	var info struct{ DockerRootDir, OSType, Architecture string }
	if err = e.docker.Call(ctx, "GET", "/info", nil, &info); err != nil {
		return w, plan, err
	}
	if !filepath.IsAbs(info.DockerRootDir) {
		return w, plan, errors.New("Docker data directory is unavailable")
	}
	staging, err := os.MkdirTemp(filepath.Join(e.root, "projects"), ".draft-")
	if err != nil {
		return w, plan, err
	}
	defer os.RemoveAll(staging)
	base := staging
	if a.Directory != "" {
		if !filepath.IsAbs(a.Directory) {
			return w, plan, fmt.Errorf(msgHostFolder, a.Directory)
		}
		if err = safeBind(a.Directory, e.root, info.DockerRootDir); err != nil {
			return w, plan, err
		}
		s, err := os.Stat(a.Directory)
		if err != nil || !s.IsDir() {
			return w, plan, fmt.Errorf(msgHostFolder, a.Directory)
		}
		base = a.Directory
	}
	source := filepath.Join(staging, "compose.yaml")
	environment := filepath.Join(staging, ".env")
	if err = os.WriteFile(source, []byte(a.Compose), 0600); err != nil {
		return w, plan, err
	}
	if err = os.WriteFile(environment, []byte(a.Env), 0600); err != nil {
		return w, plan, err
	}
	raw, err := composeConfig(ctx, staging, "docker", "compose", "--env-file", environment, "--project-name", a.Target, "--project-directory", base, "-f", source, "config", "--format", "json")
	if err != nil {
		return w, plan, fmt.Errorf("Compose validation failed: %w", err)
	}
	if json.Unmarshal([]byte(raw), &w.Config) != nil {
		return w, plan, errors.New("Could not read the resolved Compose configuration")
	}
	delete(w.Config, "name")
	if err = validateCompose(w.Config, info.DockerRootDir, e.root); err != nil {
		return w, plan, err
	}
	prior, _ := previous.Config["services"].(map[string]any)
	services := w.Config["services"].(map[string]any)
	var bindings []Binding
	for name, value := range services {
		svc := value.(map[string]any)
		item := servicePlan{Name: name, Image: fmt.Sprint(svc["image"]), Change: "create"}
		if old, ok := prior[name]; ok {
			item.Change = "update"
			if fingerprint(old) == fingerprint(svc) {
				item.Change = "unchanged"
			}
		}
		mounts, _ := svc["volumes"].([]any)
		for _, value := range mounts {
			m, _ := value.(map[string]any)
			label := fmt.Sprint(m["target"])
			if src, ok := m["source"].(string); ok {
				label = src + " → " + label
			}
			item.Mounts = append(item.Mounts, label)
		}
		ports, _ := svc["ports"].([]any)
		for _, value := range ports {
			p, _ := value.(map[string]any)
			host, _ := p["host_ip"].(string)
			protocol, _ := p["protocol"].(string)
			target := fmt.Sprint(p["target"])
			published := fmt.Sprint(p["published"])
			label := target + "/" + protocol
			if p["published"] != nil {
				label = published + " → " + label
				if host != "" {
					label = host + ":" + label
				}
				if number, err := strconv.Atoi(published); err == nil && number > 0 {
					container, _ := strconv.Atoi(target)
					b := Binding{host, container, number, protocol}
					for _, other := range bindings {
						if other.Published == number && other.Protocol == protocol && overlap(host, other.Host) {
							return w, plan, fmt.Errorf(msgPortRepeated, number, protocol)
						}
					}
					bindings = append(bindings, b)
					for _, c := range existing {
						if c.State != "running" || c.Labels["com.docker.compose.project"] == a.Target {
							continue
						}
						for _, used := range c.Ports {
							if used.PublicPort == number && used.Type == protocol && overlap(host, used.IP) {
								owner := c.ID
								if len(c.Names) > 0 {
									owner = strings.TrimPrefix(c.Names[0], "/")
								}
								return w, plan, fmt.Errorf("%s: %s (%s)", name, fmt.Sprintf(msgPortInUse, number, protocol), owner)
							}
						}
					}
					if !ownsBinding(existing, a.Target, b) && (protocol == "tcp" || protocol == "udp") && portTaken(host, number, protocol) {
						return w, plan, fmt.Errorf("%s: %w", name, fmt.Errorf(msgPortInUse, number, protocol))
					}
				}
			}
			item.Ports = append(item.Ports, label)
		}
		plan.Services = append(plan.Services, item)
	}
	for name := range prior {
		if _, ok := services[name]; !ok {
			plan.Services = append(plan.Services, servicePlan{Name: name, Change: "orphan"})
		}
	}
	sort.Slice(plan.Services, func(i, j int) bool { return plan.Services[i].Name < plan.Services[j].Name })
	plan.Images, err = e.inspectProjectImages(ctx, services, Platform{OS: info.OSType, Architecture: archName(info.Architecture)})
	if err != nil {
		return w, plan, err
	}
	plan.Token = fingerprint([]any{w, a.Target, revision, plan.Images})
	return w, plan, nil
}

func (e *Engine) applyProject(ctx context.Context, a Action) error {
	e.stage(a.ID, "Validating Compose")
	w, plan, err := e.prepareProject(ctx, a)
	if err != nil {
		return err
	}
	if plan.Token != a.Plan {
		return errors.New("The deployment plan changed. Check the project again.")
	}
	w.ImageIDs, err = e.resolveProjectImages(ctx, a.ID, plan.Images)
	if err != nil {
		return err
	}
	dir := filepath.Join(e.root, "projects", a.Target)
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if err = atomic(filepath.Join(dir, "workspace.json"), w); err != nil {
		return err
	}
	e.stage(a.ID, "Starting services from local images")
	return e.compose(ctx, a.Target, "up", "-d", "--wait", "--wait-timeout", "120")
}

// Compose interpolates string values again when loading the resolved runtime document.
func literalConfig(value any) any {
	switch v := value.(type) {
	case string:
		return strings.ReplaceAll(v, "$", "$$")
	case map[string]any:
		for k, entry := range v {
			v[k] = literalConfig(entry)
		}
	case []any:
		for i, entry := range v {
			v[i] = literalConfig(entry)
		}
	}
	return value
}
