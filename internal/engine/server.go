package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/PaNasMs/module-sdk/auth"
	"github.com/coder/websocket"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Job struct {
	DisplayName string    `json:"displayName,omitempty"`
	ID          string    `json:"id"`
	Action      string    `json:"action"`
	Target      string    `json:"target"`
	Status      string    `json:"status"`
	Stage       string    `json:"stage"`
	Error       string    `json:"error,omitempty"`
	Created     time.Time `json:"created"`
	Updated     time.Time `json:"updated"`
	Digest      string    `json:"-"`
}
type Engine struct {
	root        string
	docker      *Docker
	mu          sync.Mutex
	jobs        []Job
	busy        bool
	subscribers map[chan struct{}]bool
}

func Open(root string) (*Engine, error) {
	if err := os.MkdirAll(filepath.Join(root, "projects"), 0700); err != nil {
		return nil, err
	}
	e := &Engine{root: root, docker: NewDocker(), jobs: []Job{}, subscribers: map[chan struct{}]bool{}}
	raw, err := os.ReadFile(filepath.Join(root, "jobs.json"))
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if len(raw) > 0 {
		if err = json.Unmarshal(raw, &e.jobs); err != nil {
			return nil, err
		}
	}
	for i := range e.jobs {
		if e.jobs[i].Status == "running" || e.jobs[i].Status == "queued" {
			e.jobs[i].Status = "interrupted"
			e.jobs[i].Error = "Service restarted. Inspect the application before retrying; partial changes may remain."
		}
	}
	return e, nil
}
func (e *Engine) Active() int32 {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.busy {
		return 1
	}
	return 0
}
func atomic(path string, v any) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path+".tmp", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(raw)
	if err == nil {
		err = f.Sync()
	}
	ce := f.Close()
	if err == nil {
		err = ce
	}
	if err != nil {
		return err
	}
	if err = os.Rename(path+".tmp", path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
func (e *Engine) emit() {
	for c := range e.subscribers {
		select {
		case c <- struct{}{}:
		default:
		}
	}
}
func (e *Engine) stage(id, stage string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i := range e.jobs {
		if e.jobs[i].ID == id {
			e.jobs[i].Stage = stage
			e.jobs[i].Updated = time.Now()
		}
	}
	e.emit()
}
func output(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func failure(w http.ResponseWriter, status int, err error) {
	output(w, status, map[string]string{"error": err.Error()})
}

var nameRE = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,47}$`)
var idRE = regexp.MustCompile(`^[a-zA-Z0-9_-]{8,80}$`)
var searchRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_/-]*$`)
var hashRE = regexp.MustCompile(`^(sha256:)?[a-f0-9]{12,64}$`)

type Action struct {
	Environment    map[string]string `json:"environment"`
	DisplayName    string            `json:"displayName,omitempty"`
	WebPort        int               `json:"webPort"`
	PublishDefault bool              `json:"publishDefault"`
	ID             string            `json:"id"`
	Action         string            `json:"action"`
	Target         string            `json:"target"`
	Compose        string            `json:"compose"`
	Env            string            `json:"env"`
	Image          string            `json:"image"`
	Ports          []Binding         `json:"ports"`
	Variables      string            `json:"variables"`
	Mounts         []Mount           `json:"mounts"`
	Network        string            `json:"network"`
	Root           string            `json:"root"`
}
type Binding struct {
	Host      string `json:"host"`
	Container int    `json:"container"`
	Published int    `json:"published"`
	Protocol  string `json:"protocol"`
}
type Mount struct {
	Source   string `json:"source"`
	Target   string `json:"target"`
	ReadOnly bool   `json:"readOnly"`
}

func (e *Engine) Handler(allowed map[string]bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		who, err := auth.Lookup(r.URL.Query().Get("user"), allowed)
		if err != nil || who.Role != "admin" {
			failure(w, 403, errors.New("Administrator permissions required"))
			return
		}
		ctx := r.Context()
		switch {
		case r.URL.Path == "/events" && r.Method == "GET":
			e.events(w, r)
		case r.URL.Path == "/state" && r.Method == "GET":
			state, err := e.State(ctx)
			if err != nil {
				failure(w, 503, err)
			} else {
				output(w, 200, state)
			}
		case r.URL.Path == "/images/config" && r.Method == "GET":
			result, err := e.docker.ImageConfig(r.Context(), r.URL.Query().Get("image"))
			if err != nil {
				failure(w, 400, err)
				return
			}
			output(w, 200, result)
		case r.URL.Path == "/images/platform" && r.Method == "GET":
			reference := r.URL.Query().Get("image")
			if validImage(reference) != nil || strings.Contains(reference, "?") || strings.Contains(reference, "#") {
				failure(w, 400, errors.New("Invalid image reference"))
				return
			}
			result, err := e.docker.RemotePlatform(ctx, reference)
			if err != nil {
				failure(w, 502, errors.New("Image platform could not be checked"))
			} else {
				output(w, 200, result)
			}
		case r.URL.Path == "/images/tags" && r.Method == "GET":
			page, err := strconv.Atoi(r.URL.Query().Get("page"))
			if err != nil || page < 1 || page > 10000 {
				failure(w, 400, errors.New("Invalid page"))
				return
			}
			result, err := hubTags(ctx, hubClient, r.URL.Query().Get("repository"), page)
			if err != nil {
				failure(w, 502, errors.New("Docker Hub tags are unavailable"))
			} else {
				output(w, 200, result)
			}
		case r.URL.Path == "/images/search" && r.Method == "GET":
			term := strings.TrimSpace(r.URL.Query().Get("term"))
			if len(term) < 2 || len(term) > 100 || !searchRE.MatchString(term) {
				failure(w, 400, errors.New("Invalid image search term"))
				return
			}
			results, err := e.docker.Search(ctx, term)
			if err != nil {
				failure(w, 502, errors.New("Docker Hub search is unavailable"))
			} else {
				output(w, 200, results)
			}
		case r.URL.Path == "/jobs" && r.Method == "GET":
			e.mu.Lock()
			jobs := append([]Job{}, e.jobs...)
			e.mu.Unlock()
			output(w, 200, jobs)
		case r.URL.Path == "/logs" && r.Method == "GET":
			id := r.URL.Query().Get("id")
			if !hashRE.MatchString(id) {
				failure(w, 400, errors.New("Invalid container"))
				return
			}
			s, err := e.docker.Logs(ctx, id)
			if err != nil {
				failure(w, 502, err)
			} else {
				output(w, 200, map[string]string{"text": s})
			}
		case r.URL.Path == "/inspect" && r.Method == "GET":
			id := r.URL.Query().Get("id")
			if !hashRE.MatchString(id) {
				failure(w, 400, errors.New("Invalid container"))
				return
			}
			var data map[string]any
			err := e.docker.Call(ctx, "GET", "/containers/"+id+"/json", nil, &data)
			if err != nil {
				failure(w, 502, err)
			} else {
				delete(data, "Config")
				output(w, 200, data)
			}
		case r.URL.Path == "/stats" && r.Method == "GET":
			id := r.URL.Query().Get("id")
			if !hashRE.MatchString(id) {
				failure(w, 400, errors.New("Invalid container"))
				return
			}
			var data any
			err := e.docker.Call(ctx, "GET", "/containers/"+id+"/stats?stream=false&one-shot=true", nil, &data)
			if err != nil {
				failure(w, 502, err)
			} else {
				output(w, 200, data)
			}
		case r.URL.Path == "/project" && r.Method == "GET":
			id := r.URL.Query().Get("id")
			if !nameRE.MatchString(id) {
				failure(w, 400, errors.New("Invalid project"))
				return
			}
			raw, err := os.ReadFile(filepath.Join(e.root, "projects", id, "compose.json"))
			if err != nil {
				failure(w, 404, errors.New("Managed project not found"))
			} else {
				output(w, 200, map[string]string{"compose": string(raw)})
			}
		case r.URL.Path == "/action" && r.Method == "POST":
			var a Action
			dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 512<<10))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&a); err != nil {
				failure(w, 400, errors.New("Invalid action"))
				return
			}
			if dec.Decode(new(any)) != io.EOF {
				failure(w, 400, errors.New("Invalid trailing data"))
				return
			}
			job, err := e.submit(a)
			if err != nil {
				failure(w, 409, err)
			} else {
				output(w, 202, job)
			}
		default:
			failure(w, 404, errors.New("Not found"))
		}
	})
}
func (e *Engine) events(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer c.CloseNow()
	ctx := c.CloseRead(r.Context())
	ch := make(chan struct{}, 1)
	e.mu.Lock()
	e.subscribers[ch] = true
	e.mu.Unlock()
	defer func() { e.mu.Lock(); delete(e.subscribers, ch); e.mu.Unlock() }()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ch:
		case <-ticker.C:
		}
		send, cancel := context.WithTimeout(ctx, 5*time.Second)
		err = c.Write(send, websocket.MessageText, []byte(`{"type":"refresh"}`))
		cancel()
		if err != nil {
			return
		}
	}
}
func (e *Engine) submit(a Action) (Job, error) {
	if !idRE.MatchString(a.ID) {
		return Job{}, errors.New("Missing operation identifier")
	}
	valid := map[string]bool{"storage.move": true, "storage.recover": true, "setup": true, "engine.start": true, "project.save": true, "image.create": true, "image.pull": true, "project.start": true, "project.stop": true, "project.restart": true, "project.remove": true, "container.start": true, "container.stop": true, "container.restart": true, "container.remove": true, "network.create": true, "network.remove": true, "volume.create": true, "volume.remove": true, "image.remove": true}
	if !valid[a.Action] {
		return Job{}, errors.New("Unknown action")
	}
	if strings.HasPrefix(a.Action, "project.") || a.Action == "image.create" || a.Action == "network.create" || a.Action == "volume.create" {
		if !nameRE.MatchString(a.Target) {
			return Job{}, errors.New("Use a lowercase name, 2–48 letters, digits, hyphens or underscores")
		}
	}
	if a.Action == "image.create" || a.Action == "image.pull" {
		if err := validImage(a.Image); err != nil {
			return Job{}, err
		}
	}
	if len(a.DisplayName) > 512 || strings.ContainsAny(a.DisplayName, "\r\n\x00") {
		return Job{}, errors.New("Invalid display name")
	}
	raw, _ := json.Marshal(a)
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, j := range e.jobs {
		if j.ID == a.ID {
			if j.Digest != "" && j.Digest != digest {
				return Job{}, errors.New("Operation identifier already used")
			}
			return j, nil
		}
	}
	if e.busy {
		return Job{}, errors.New("Another operation is running. Wait for it to finish.")
	}
	j := Job{DisplayName: a.DisplayName, ID: a.ID, Action: a.Action, Target: a.Target, Status: "running", Stage: "Preparing", Created: time.Now(), Updated: time.Now(), Digest: digest}
	e.jobs = append(e.jobs, j)
	if len(e.jobs) > 100 {
		e.jobs = e.jobs[len(e.jobs)-100:]
	}
	if err := atomic(filepath.Join(e.root, "jobs.json"), e.jobs); err != nil {
		e.jobs = e.jobs[:len(e.jobs)-1]
		return Job{}, err
	}
	e.busy = true
	e.emit()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
		defer cancel()
		err := e.execute(ctx, a)
		e.mu.Lock()
		defer e.mu.Unlock()
		for i := range e.jobs {
			if e.jobs[i].ID == a.ID {
				e.jobs[i].Status = "succeeded"
				e.jobs[i].Stage = "Completed"
				if err != nil {
					e.jobs[i].Status = "failed"
					e.jobs[i].Stage = "Failed"
					e.jobs[i].Error = err.Error()
				}
				e.jobs[i].Updated = time.Now()
			}
		}
		e.busy = false
		_ = atomic(filepath.Join(e.root, "jobs.json"), e.jobs)
		e.emit()
	}()
	return j, nil
}
func (e *Engine) State(ctx context.Context) (map[string]any, error) {
	status := e.Check(ctx)
	state := map[string]any{"engine": status, "containers": []Container{}, "images": []Image{}, "networks": []Network{}, "volumes": []Volume{}, "projects": []map[string]string{}}
	entries, err := os.ReadDir(filepath.Join(e.root, "projects"))
	if err != nil {
		return nil, err
	}
	projects := []map[string]string{}
	for _, v := range entries {
		if v.IsDir() && !strings.HasPrefix(v.Name(), ".") {
			projects = append(projects, map[string]string{"name": v.Name()})
		}
	}
	state["projects"] = projects
	migration, err := e.migration()
	if err != nil {
		return nil, err
	}
	state["migration"] = migration
	if status.Engine != "" && status.Reachable {
		var cs []Container
		var images []Image
		var networks []Network
		var volumes struct{ Volumes []Volume }
		if err = e.docker.Call(ctx, "GET", "/containers/json?all=1", nil, &cs); err != nil {
			return nil, err
		}
		if cs != nil {
			for i := range cs {
				if cs[i].Labels == nil {
					cs[i].Labels = map[string]string{}
				}
				if cs[i].Ports == nil {
					cs[i].Ports = []Port{}
				}
				if cs[i].Names == nil {
					cs[i].Names = []string{}
				}
			}
			state["containers"] = cs
		}
		if err = e.docker.Call(ctx, "GET", "/images/json", nil, &images); err != nil {
			return nil, err
		}
		if images != nil {
			host, _ := e.docker.HostPlatform(ctx)
			for i := range images {
				images[i].PlatformCheck = e.docker.LocalPlatform(ctx, images[i].ID, host)
			}
			state["images"] = images
		}
		if err = e.docker.Call(ctx, "GET", "/networks", nil, &networks); err != nil {
			return nil, err
		}
		if networks != nil {
			state["networks"] = networks
		}
		if err = e.docker.Call(ctx, "GET", "/volumes", nil, &volumes); err != nil {
			return nil, err
		}
		if volumes.Volumes != nil {
			state["volumes"] = volumes.Volumes
		}
	}
	return state, nil
}

type bounded struct{ b strings.Builder }

func (b *bounded) Write(p []byte) (int, error) {
	n := len(p)
	if b.b.Len() < 64<<10 {
		b.b.Write(p[:min(len(p), (64<<10)-b.b.Len())])
	}
	return n, nil
}
func command(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	cmd.Dir = dir
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "HOME=/var/lib/panasms-containers", "LANG=C.UTF-8", "DEBIAN_FRONTEND=noninteractive", "DOCKER_HOST=unix:///var/run/docker.sock", "COMPOSE_ANSI=never", "COMPOSE_PROGRESS=plain"}
	var out bounded
	cmd.Stdout = &out
	var stderr bounded
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		return out.b.String(), fmt.Errorf("%s failed: %s", args[0], strings.TrimSpace(stderr.b.String()+"\n"+out.b.String()))
	}
	return out.b.String(), nil
}
func (e *Engine) compose(ctx context.Context, name string, args ...string) error {
	if !nameRE.MatchString(name) {
		return errors.New("Invalid project name")
	}
	dir := filepath.Join(e.root, "projects", name)
	if _, err := os.Stat(filepath.Join(dir, "compose.json")); err != nil {
		return errors.New("This project is external. Manage its original Compose file outside this module.")
	}
	argv := []string{"docker", "compose", "--project-name", name, "--project-directory", dir, "-f", filepath.Join(dir, "compose.json")}
	argv = append(argv, args...)
	_, err := command(ctx, dir, argv...)
	return err
}
func (e *Engine) execute(ctx context.Context, a Action) error {
	e.stage(a.ID, a.Action)
	if a.Action == "storage.recover" {
		return e.recoverMove(ctx)
	}
	if m, err := e.migration(); err != nil {
		return err
	} else if m != nil {
		return errors.New("Storage transfer requires recovery before other changes")
	}
	if a.Action == "setup" {
		return e.Setup(ctx, a)
	}
	if a.Action == "engine.start" {
		_, err := command(ctx, "", "systemctl", "start", "docker")
		return err
	}
	st := e.Check(ctx)
	if !st.Compatible {
		return fmt.Errorf("Docker is not ready: %s", st.Problem)
	}
	switch a.Action {
	case "storage.move":
		return e.move(ctx, a)
	case "project.save", "image.create":
		return e.saveProject(ctx, a)
	case "project.start":
		return e.compose(ctx, a.Target, "up", "-d", "--wait", "--wait-timeout", "120")
	case "project.stop":
		return e.compose(ctx, a.Target, "stop", "--timeout", "30")
	case "project.restart":
		return e.compose(ctx, a.Target, "restart", "--timeout", "30")
	case "project.remove":
		if err := e.compose(ctx, a.Target, "down", "--timeout", "30"); err != nil {
			return err
		}
		return os.Rename(filepath.Join(e.root, "projects", a.Target), filepath.Join(e.root, "projects", ".removed-"+a.ID))
	case "image.pull":
		if err := validImage(a.Image); err != nil {
			return err
		}
		_, err := command(ctx, "", "docker", "pull", a.Image)
		return err
	case "container.start", "container.stop", "container.restart", "container.remove":
		if !hashRE.MatchString(a.Target) {
			return errors.New("Invalid container identifier")
		}
		method, path := "POST", "/containers/"+a.Target+"/"+strings.TrimPrefix(a.Action, "container.")
		if a.Action == "container.remove" {
			method = "DELETE"
			path = "/containers/" + a.Target + "?v=0&force=0"
		}
		if a.Action == "container.stop" || a.Action == "container.restart" {
			path += "?t=30"
		}
		return e.docker.Call(ctx, method, path, nil, nil)
	case "network.create":
		if !nameRE.MatchString(a.Target) {
			return errors.New("Invalid network name")
		}
		return e.docker.Call(ctx, "POST", "/networks/create", map[string]any{"Name": a.Target, "Driver": "bridge", "CheckDuplicate": true}, nil)
	case "network.remove":
		if !hashRE.MatchString(a.Target) {
			return errors.New("Invalid network identifier")
		}
		return e.docker.Call(ctx, "DELETE", "/networks/"+a.Target, nil, nil)
	case "volume.create":
		if !nameRE.MatchString(a.Target) {
			return errors.New("Invalid volume name")
		}
		return e.docker.Call(ctx, "POST", "/volumes/create", map[string]string{"Name": a.Target}, nil)
	case "volume.remove":
		if !nameRE.MatchString(a.Target) {
			return errors.New("Invalid volume name")
		}
		return e.docker.Call(ctx, "DELETE", "/volumes/"+a.Target+"?force=0", nil, nil)
	case "image.remove":
		if !hashRE.MatchString(a.Target) {
			return errors.New("Invalid image identifier")
		}
		return e.docker.Call(ctx, "DELETE", "/images/"+a.Target+"?force=0", nil, nil)
	}
	return errors.New("Unsupported action")
}
func validImage(s string) error {
	if s == "" || len(s) > 256 || strings.HasPrefix(s, "-") || strings.ContainsAny(s, " \t\r\n") {
		return errors.New("Invalid image reference")
	}
	return nil
}
func (e *Engine) saveProject(ctx context.Context, a Action) error {
	if !nameRE.MatchString(a.Target) {
		return errors.New("Use a lowercase project name, 2–48 letters, digits, hyphens or underscores")
	}
	dir := filepath.Join(e.root, "projects", a.Target)
	_, statErr := os.Stat(dir)
	var existing []Container
	if err := e.docker.Call(ctx, "GET", "/containers/json?all=1", nil, &existing); err != nil {
		return err
	}
	for _, c := range existing {
		if c.Labels["com.docker.compose.project"] == a.Target && os.IsNotExist(statErr) {
			return errors.New("A Compose project with this name already exists outside this module")
		}
	}
	if a.Action == "image.create" {
		detail, err := e.docker.ImageConfig(ctx, a.Image)
		if err != nil {
			return err
		}
		if detail.PlatformCheck.Status != "compatible" {
			return errors.New("Local image architecture is incompatible or could not be verified")
		}
		a.Image = detail.ID

		if statErr == nil {
			return errors.New("Project already exists")
		}
		raw, err := imageCompose(a)
		if err != nil {
			return err
		}
		a.Compose = string(raw)
	}
	if strings.TrimSpace(a.Compose) == "" {
		return errors.New("Compose content is required")
	}
	staging, err := os.MkdirTemp(filepath.Join(e.root, "projects"), ".draft-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	if err = os.WriteFile(filepath.Join(staging, "compose.yaml"), []byte(a.Compose), 0600); err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(staging, ".env"), []byte(a.Env), 0600); err != nil {
		return err
	}
	e.stage(a.ID, "Validating Compose")
	raw, err := command(ctx, staging, "docker", "compose", "--project-name", a.Target, "--project-directory", staging, "-f", filepath.Join(staging, "compose.yaml"), "config", "--format", "json")
	if err != nil {
		return err
	}
	var config map[string]any
	if err = json.Unmarshal([]byte(raw), &config); err != nil {
		return errors.New("Cannot parse Compose configuration")
	}
	if err = validateCompose(config); err != nil {
		return err
	}
	delete(config, "name")
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if previous, err := os.ReadFile(filepath.Join(dir, "compose.json")); err == nil {
		if err = os.WriteFile(filepath.Join(dir, "compose.previous.json"), previous, 0600); err != nil {
			return err
		}
	}
	if err = atomic(filepath.Join(dir, "compose.json"), config); err != nil {
		return err
	}
	e.stage(a.ID, "Downloading images and starting services")
	return e.compose(ctx, a.Target, "up", "-d", "--wait", "--wait-timeout", "120")
}
func validateCompose(config map[string]any) error {
	services, ok := config["services"].(map[string]any)
	if !ok || len(services) == 0 {
		return errors.New("Compose must contain services")
	}
	for name, value := range services {
		s, ok := value.(map[string]any)
		if !ok {
			return errors.New("Invalid service")
		}
		if s["build"] != nil {
			return fmt.Errorf("%s: image builds are not supported in this preview; use a published image", name)
		}
		if image, _ := s["image"].(string); validImage(image) != nil {
			return fmt.Errorf("%s: image is required", name)
		}
		if mounts, ok := s["volumes"].([]any); ok {
			for _, v := range mounts {
				m, _ := v.(map[string]any)
				if m["type"] == "bind" {
					source, _ := m["source"].(string)
					if !filepath.IsAbs(source) || strings.Contains(source, "/.draft-") {
						return fmt.Errorf("%s: bind mounts must use existing absolute host paths", name)
					}
					if _, err := os.Stat(source); err != nil {
						return fmt.Errorf("Host path unavailable: %s", source)
					}
				}
			}
		}
	}
	for _, kind := range []string{"configs", "secrets"} {
		if entries, ok := config[kind].(map[string]any); ok {
			for _, v := range entries {
				m, _ := v.(map[string]any)
				if m["file"] != nil {
					return errors.New("File-based secrets/configs are not supported in this preview; use environment values or existing external resources")
				}
			}
		}
	}
	return nil
}
func imageCompose(a Action) ([]byte, error) {
	if err := validImage(a.Image); err != nil {
		return nil, err
	}
	s := map[string]any{"image": a.Image, "restart": "unless-stopped", "pull_policy": "never"}
	if a.WebPort > 0 {
		found := false
		for _, p := range a.Ports {
			if p.Published == a.WebPort && p.Protocol == "tcp" {
				found = true
			}
		}
		if !found {
			return nil, errors.New("Web interface port must match a published TCP port")
		}
		s["labels"] = map[string]string{"com.panasms.web-port": fmt.Sprint(a.WebPort)}
	}
	ports := []map[string]any{}
	for _, p := range a.Ports {
		if p.Container < 1 || p.Container > 65535 || p.Published < 1 || p.Published > 65535 || (p.Protocol != "tcp" && p.Protocol != "udp") {
			return nil, errors.New("Invalid port mapping")
		}
		if p.Host == "" {
			p.Host = "0.0.0.0"
		}
		if net.ParseIP(p.Host) == nil {
			return nil, errors.New("Invalid host bind address")
		}
		ports = append(ports, map[string]any{"target": p.Container, "published": fmt.Sprint(p.Published), "host_ip": p.Host, "protocol": p.Protocol})
	}
	if len(ports) > 0 {
		s["ports"] = ports
	}
	env := map[string]string{}
	for _, l := range strings.Split(a.Variables, "\n") {
		if strings.TrimSpace(l) == "" {
			continue
		}
		k, v, ok := strings.Cut(l, "=")
		if !ok || k == "" {
			return nil, errors.New("Environment variables must use NAME=value")
		}
		env[k] = v
	}
	for key, value := range a.Environment {
		if key == "" || strings.ContainsAny(key, "=\x00") || strings.ContainsRune(value, 0) {
			return nil, errors.New("Invalid environment variable")
		}
		env[key] = value
	}
	if len(env) > 0 {
		s["environment"] = env
	}
	volumes := []map[string]any{}
	for _, m := range a.Mounts {
		if !filepath.IsAbs(m.Target) || m.Target == "/" {
			return nil, errors.New("Container mount path must be absolute and not root")
		}
		kind := "volume"
		if filepath.IsAbs(m.Source) {
			kind = "bind"
			if _, err := os.Stat(m.Source); err != nil {
				return nil, fmt.Errorf("Host folder unavailable: %s", m.Source)
			}
		} else if !nameRE.MatchString(m.Source) {
			return nil, errors.New("Invalid volume name")
		}
		volumes = append(volumes, map[string]any{"type": kind, "source": m.Source, "target": m.Target, "read_only": m.ReadOnly})
	}
	if len(volumes) > 0 {
		s["volumes"] = volumes
	}
	doc := map[string]any{"services": map[string]any{"app": s}}
	named := map[string]any{}
	for _, m := range a.Mounts {
		if !filepath.IsAbs(m.Source) {
			named[m.Source] = map[string]any{"name": m.Source}
		}
	}
	if len(named) > 0 {
		doc["volumes"] = named
	}
	if a.Network != "" {
		if !nameRE.MatchString(a.Network) {
			return nil, errors.New("Invalid network name")
		}
		s["networks"] = []string{"shared"}
		doc["networks"] = map[string]any{"shared": map[string]any{"external": true, "name": a.Network}}
	}
	raw, err := json.Marshal(doc)
	return []byte(strings.ReplaceAll(string(raw), "$", "$$")), err
}
