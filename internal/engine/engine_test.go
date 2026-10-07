package engine

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestImageCompose(t *testing.T) {
	a := Action{Image: "nginx:alpine", Variables: "A=one=two\nB=", Network: "shared-apps", Ports: []Binding{{Container: 80, Published: 8080, Protocol: "tcp"}}}
	raw, err := imageCompose(a)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err = json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	s := doc["services"].(map[string]any)["app"].(map[string]any)
	if s["environment"].(map[string]any)["A"] != "one=two" {
		t.Fatal("environment changed")
	}
	port := s["ports"].([]any)[0].(map[string]any)
	if port["host_ip"] != "0.0.0.0" || port["protocol"] != "tcp" {
		t.Fatal(port)
	}
	if doc["networks"].(map[string]any)["shared"].(map[string]any)["external"] != true {
		t.Fatal("network not external")
	}
	for _, bad := range []Action{{Image: "--privileged"}, {Image: "nginx", Variables: "INVALID"}, {Image: "nginx", Ports: []Binding{{Container: 80, Published: 70000, Protocol: "tcp"}}}, {Image: "nginx", Mounts: []Mount{{Source: "foo", Target: "/"}}}} {
		if _, err := imageCompose(bad); err == nil {
			t.Fatal("invalid accepted", bad)
		}
	}
}
func TestComposeValidation(t *testing.T) {
	for _, c := range []struct {
		raw   string
		valid bool
	}{
		{`{"services":{"web":{"image":"nginx:alpine"}}}`, true},
		{`{"services":{"web":{"build":"."}}}`, false},
		{`{"services":{"web":{"image":"nginx","volumes":[{"type":"bind","source":"/nonexistent-panasms"}]}}}`, false},
		{`{"services":{"web":{"image":"nginx"}},"secrets":{"token":{"file":"token.txt"}}}`, false},
		{`{"services":{}}`, false},
	} {
		var v map[string]any
		_ = json.Unmarshal([]byte(c.raw), &v)
		if (validateCompose(v) == nil) != c.valid {
			t.Fatal(c.raw)
		}
	}
}
func TestVersions(t *testing.T) {
	for s, want := range map[string]bool{"v2.26.1": true, "2.20.0": true, "1.29.2": false, "2.19.9": false, "garbage": false, "3.0.0": true} {
		if atLeast(s, 2, 20) != want {
			t.Fatal(s)
		}
	}
}
func TestLogs(t *testing.T) {
	raw := make([]byte, 8)
	raw[0] = 1
	binary.BigEndian.PutUint32(raw[4:], 5)
	raw = append(raw, []byte("hello")...)
	if demux(raw) != "hello" || demux([]byte("TTY output")) != "TTY output" {
		t.Fatal("log decoding")
	}
}
func TestRestart(t *testing.T) {
	root := t.TempDir()
	if err := atomic(filepath.Join(root, "jobs.json"), []Job{{ID: "test-job", Status: "running"}}); err != nil {
		t.Fatal(err)
	}
	e, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if e.jobs[0].Status != "interrupted" || e.Active() != 0 {
		t.Fatal(e.jobs)
	}
}
func TestAuthorization(t *testing.T) {
	e, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"GET", "POST"} {
		r := httptest.NewRequest(method, "/action?user=", strings.NewReader(`{}`))
		w := httptest.NewRecorder()
		e.Handler(map[string]bool{}).ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatal(w.Code)
		}
	}
}
func TestStorage(t *testing.T) {
	for _, root := range []string{"relative/path", "/tmp/has space", "/tmp/%n"} {
		if _, err := storageMount(context.Background(), root); err == nil {
			t.Fatal(root)
		}
	}
	if !within("/srv/docker/sub", "/srv/docker") || within("/srv/docker-old", "/srv/docker") {
		t.Fatal("overlap")
	}
}
func TestInvalidAction(t *testing.T) {
	e, _ := Open(t.TempDir())
	if _, err := e.submit(Action{ID: "12345678", Action: "shell"}); err == nil {
		t.Fatal("unknown accepted")
	}
	if _, err := e.submit(Action{ID: "bad", Action: "setup"}); err == nil {
		t.Fatal("invalid id accepted")
	}
	if _, err := os.Stat(filepath.Join(e.root, "jobs.json")); !os.IsNotExist(err) {
		t.Fatal("unexpected job")
	}
}

func TestPackageUpgradeDetection(t *testing.T) {
	if packageUpgrade("Inst runc (1.1.15 Debian [arm64])") {
		t.Fatal("new package treated as upgrade")
	}
	if !packageUpgrade("Inst runc [1.1.10] (1.1.15 Debian [arm64])") {
		t.Fatal("upgrade missed")
	}
}

func TestImageSearch(t *testing.T) {
	client := &http.Client{Transport: searchTransport{t: t}}
	result, err := (&Docker{Client: client}).Search(context.Background(), "library/nginx")
	if err != nil || len(result) != 1 || result[0].Name != "nginx" || !result[0].Official {
		t.Fatalf("%v %v", result, err)
	}
}

type searchTransport struct{ t *testing.T }

func (s searchTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Path != "/images/search" || r.URL.Query().Get("term") != "library/nginx" || r.URL.Query().Get("limit") != "15" {
		s.t.Error(r.URL)
	}
	response := httptest.NewRecorder()
	response.WriteString(`[{"name":"nginx","description":"web","is_official":true,"star_count":10}]`)
	return response.Result(), nil
}

func TestHubTags(t *testing.T) {
	client := &http.Client{Transport: tagTransport{t: t}}
	page, err := hubTags(context.Background(), client, "nginx", 2)
	if err != nil || len(page.Tags) != 1 || page.Tags[0] != "alpine" || !page.More {
		t.Fatalf("%+v %v", page, err)
	}
	for _, bad := range []string{"../../etc", "ghcr.io/owner/image", "https://example.com/a", "a/b/c"} {
		if _, err := hubTags(context.Background(), client, bad, 1); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
}

type tagTransport struct{ t *testing.T }

func (s tagTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Host != "hub.docker.com" || r.URL.Path != "/v2/namespaces/library/repositories/nginx/tags" || r.URL.Query().Get("page") != "2" {
		s.t.Error(r.URL)
	}
	response := httptest.NewRecorder()
	response.WriteString(`{"next":"https://example.invalid/not-followed","results":[{"name":"alpine"}]}`)
	return response.Result(), nil
}

func TestPlatformCompatibility(t *testing.T) {
	host := Platform{OS: "linux", Architecture: "aarch64"}
	cases := []struct {
		platforms []Platform
		want      string
	}{
		{[]Platform{{OS: "linux", Architecture: "arm64", Variant: "v8"}}, "compatible"},
		{[]Platform{{OS: "linux", Architecture: "amd64"}}, "incompatible"},
		{[]Platform{{OS: "windows", Architecture: "arm64"}}, "incompatible"},
		{[]Platform{{OS: "unknown", Architecture: "unknown"}}, "unknown"},
		{[]Platform{{OS: "linux", Architecture: "arm64", Variant: "v9"}}, "unknown"},
		{[]Platform{{OS: "linux", Architecture: "amd64"}, {OS: "linux", Architecture: "arm64"}}, "compatible"},
	}
	for _, c := range cases {
		if got := compatibility(host, c.platforms); got != c.want {
			t.Errorf("%+v: %s != %s", c.platforms, got, c.want)
		}
	}
}

func TestStructuredImageCompose(t *testing.T) {
	raw, err := imageCompose(Action{Image: "sha256:abc123", Environment: map[string]string{"VALUE": "one=two\n$three"}})
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	json.Unmarshal(raw, &doc)
	service := doc["services"].(map[string]any)["app"].(map[string]any)
	if service["pull_policy"] != "never" || service["environment"].(map[string]any)["VALUE"] != "one=two\n$$three" {
		t.Fatal(string(raw))
	}
	for _, a := range []Action{
		{Image: "nginx", Environment: map[string]string{"bad=name": "x"}},
		{Image: "nginx", Ports: []Binding{{Container: 80, Published: 80, Protocol: "tcp", Host: "not-an-address"}}},
		{Image: "nginx", Ports: []Binding{{Container: 80, Published: 80, Protocol: "udp"}}, WebPort: 80},
	} {
		if _, err := imageCompose(a); err == nil {
			t.Fatal("invalid configuration accepted")
		}
	}
}

func TestClearHistoryPreservesActiveJobsAndRetryIdentity(t *testing.T) {
	e := &Engine{root: t.TempDir(), subscribers: map[chan struct{}]bool{}, jobs: []Job{{ID: "active", Status: "running"}, {ID: "pending", Status: "queued"}, {ID: "done", Status: "succeeded", Digest: "preserve"}, {ID: "failed", Status: "failed"}, {ID: "stopped", Status: "interrupted"}}}
	if err := e.clearHistory(); err != nil {
		t.Fatal(err)
	}
	if len(e.jobs) != 5 || e.jobs[0].Hidden || e.jobs[1].Hidden || !e.jobs[2].Hidden || !e.jobs[3].Hidden || !e.jobs[4].Hidden || e.jobs[2].Digest != "preserve" {
		t.Fatal(e.jobs)
	}
	raw, err := os.ReadFile(filepath.Join(e.root, "jobs.json"))
	if err != nil {
		t.Fatal(err)
	}
	var saved []Job
	if err = json.Unmarshal(raw, &saved); err != nil || !saved[2].Hidden {
		t.Fatal(string(raw), err)
	}
	if err = e.clearHistory(); err != nil {
		t.Fatal(err)
	}
}
func TestClearHistoryWriteFailureKeepsVisibleJobs(t *testing.T) {
	root := t.TempDir()
	block := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(block, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	e := &Engine{root: block, jobs: []Job{{ID: "done", Status: "succeeded"}}}
	if err := e.clearHistory(); err == nil || e.jobs[0].Hidden {
		t.Fatal("failed write changed history")
	}
}

func TestRejectHostPrivilegeEscapes(t *testing.T) {
	for _, setting := range []string{`"privileged":true`, `"pid":"host"`, `"cap_add":["SYS_ADMIN"]`, `"devices":["/dev/sda:/dev/sda"]`, `"network_mode":"host"`, `"volumes":[{"type":"bind","source":"/etc"}]`, `"volumes":[{"type":"bind","source":"/"}]`} {
		var config map[string]any
		json.Unmarshal([]byte(`{"services":{"app":{"image":"nginx",`+setting+`}}}`), &config)
		if validateCompose(config) == nil {
			t.Fatal("host privilege accepted", setting)
		}
	}
	link := filepath.Join(t.TempDir(), "system")
	os.Symlink("/etc", link)
	if safeBind(link) == nil {
		t.Fatal("protected symlink accepted")
	}
	if err := safeBind(t.TempDir()); err != nil {
		t.Fatal("ordinary data directory rejected", err)
	}
}

func TestSetupRetryProtectsModifiedConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.json")
	expected := map[string]any{"data-root": "/srv/docker"}
	if err := verifySetupFile(path, expected); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"data-root":"/srv/docker"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := verifySetupFile(path, expected); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(path, []byte(`{"data-root":"/srv/changed"}`), 0600)
	if verifySetupFile(path, expected) == nil {
		t.Fatal("retry overwrote administrator changes")
	}
	os.WriteFile(path, []byte(`broken`), 0600)
	if verifySetupFile(path, expected) == nil {
		t.Fatal("invalid configuration accepted")
	}
}

func TestPortsFreeReportsTakenPort(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	taken := []Binding{{Host: "127.0.0.1", Container: 80, Published: port, Protocol: "tcp"}}
	if err = portsFree(taken); err == nil || !strings.Contains(err.Error(), "already in use") {
		t.Fatalf("taken port accepted: %v", err)
	}
	l.Close()
	if err = portsFree(taken); err != nil {
		t.Fatalf("free port refused: %v", err)
	}
}

// dockerStub answers Docker Engine API calls from canned JSON by path.
type dockerStub map[string]string

func (d dockerStub) RoundTrip(r *http.Request) (*http.Response, error) {
	response := httptest.NewRecorder()
	body, ok := d[r.URL.Path]
	if !ok {
		response.WriteHeader(404)
		body = `{"message":"not found"}`
	}
	response.WriteString(body)
	return response.Result(), nil
}

func stubEngine(t *testing.T, docker dockerStub) *Engine {
	t.Helper()
	e, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e.docker = &Docker{Client: &http.Client{Transport: docker}}
	return e
}

// stubPorts replaces the bind probe with a fixed set of taken "port/protocol".
func stubPorts(t *testing.T, taken ...string) {
	t.Helper()
	original := portTaken
	t.Cleanup(func() { portTaken = original })
	portTaken = func(_ string, port int, protocol string) bool {
		for _, entry := range taken {
			if entry == portKey(port, protocol) {
				return true
			}
		}
		return false
	}
}

var compatibleDocker = dockerStub{
	"/containers/json":    `[{"Id":"c1","Labels":{"com.docker.compose.project":"foreign"}}]`,
	"/info":               `{"OSType":"linux","Architecture":"x86_64"}`,
	"/images/nginx/json":  `{"Id":"sha256:abc","Os":"linux","Architecture":"amd64","Config":{"ExposedPorts":{"80/tcp":{},"8080/tcp":{},"53/udp":{}}}}`,
	"/images/armish/json": `{"Id":"sha256:def","Os":"linux","Architecture":"s390x"}`,
}

func fields(problems []Problem) string {
	parts := []string{}
	for _, p := range problems {
		parts = append(parts, p.Field+":"+p.Error)
	}
	return strings.Join(parts, "|")
}

func TestPreflightAcceptsValidCreate(t *testing.T) {
	stubPorts(t)
	e := stubEngine(t, compatibleDocker)
	a := Action{ID: "12345678", Action: "image.create", Target: "web", Image: "nginx", WebPort: 8080, Ports: []Binding{{Host: "0.0.0.0", Container: 80, Published: 8080, Protocol: "tcp"}, {Host: "::", Container: 80, Published: 8080, Protocol: "tcp"}, {Container: 53, Published: 8080, Protocol: "udp"}}}
	if problems := e.preflight(context.Background(), a); len(problems) != 0 {
		t.Fatal(fields(problems))
	}
	if problems := e.preflight(context.Background(), Action{Action: "project.save", Target: "Bad Name"}); len(problems) != 0 {
		t.Fatal("pre-flight applies to image.create only", fields(problems))
	}
}

func TestPreflightReportsEachField(t *testing.T) {
	stubPorts(t, "80/tcp")
	e := stubEngine(t, compatibleDocker)
	if err := os.Mkdir(filepath.Join(e.root, "projects", "taken"), 0700); err != nil {
		t.Fatal(err)
	}
	port := func(published int) Binding {
		return Binding{Host: "0.0.0.0", Container: 80, Published: published, Protocol: "tcp"}
	}
	for _, c := range []struct {
		name string
		a    Action
		want string
	}{
		{"name rule", Action{Target: "Bad Name", Image: "nginx"}, "name:" + msgNameRule},
		{"managed project", Action{Target: "taken", Image: "nginx"}, "name:" + msgProjectExists},
		{"external project", Action{Target: "foreign", Image: "nginx"}, "name:" + msgExternalProject},
		{"image reference", Action{Target: "web", Image: "--privileged"}, "image:" + msgImageReference},
		{"missing image", Action{Target: "web", Image: "absent"}, "image:" + msgImageUnavailable},
		{"architecture", Action{Target: "web", Image: "armish"}, "image:" + msgImageIncompatible},
		{"port in use", Action{Target: "web", Image: "nginx", Ports: []Binding{port(80)}}, "ports:NAS port 80/tcp is already in use. Choose another NAS port."},
		{"port range", Action{Target: "web", Image: "nginx", Ports: []Binding{port(70000)}}, "ports:" + msgPortMapping},
		{"bind address", Action{Target: "web", Image: "nginx", Ports: []Binding{{Host: "nas.local", Container: 80, Published: 8080, Protocol: "tcp"}}}, "ports:" + msgBindAddress},
		{"repeated port", Action{Target: "web", Image: "nginx", Ports: []Binding{port(8080), {Host: "192.168.1.5", Container: 81, Published: 8080, Protocol: "tcp"}}}, "ports:NAS port 8080/tcp is listed more than once"},
		{"web port", Action{Target: "web", Image: "nginx", WebPort: 9000, Ports: []Binding{port(8080)}}, "webPort:" + msgWebPort},
		{"mount", Action{Target: "web", Image: "nginx", Mounts: []Mount{{Source: "data", Target: "/"}}}, "general:" + msgMountPath},
		{"several", Action{Target: "Bad Name", Image: "nginx", Ports: []Binding{port(8080), port(80)}}, "name:" + msgNameRule + "|ports:NAS port 80/tcp is already in use. Choose another NAS port."},
	} {
		c.a.Action = "image.create"
		problems := e.preflight(context.Background(), c.a)
		if got := fields(problems); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
	second := e.preflight(context.Background(), Action{Action: "image.create", Target: "web", Image: "nginx", Ports: []Binding{port(8080), port(80)}})
	if len(second) != 1 || second[0].Index != 1 {
		t.Fatalf("problem must name the offending row: %+v", second)
	}
}

func TestPreflightSkipsDockerChecksWhenUnreachable(t *testing.T) {
	stubPorts(t)
	e := stubEngine(t, nil)
	e.docker = &Docker{Client: &http.Client{Transport: failingTransport{}}}
	if problems := e.preflight(context.Background(), Action{Action: "image.create", Target: "web", Image: "nginx"}); len(problems) != 0 {
		t.Fatal(fields(problems))
	}
}

type failingTransport struct{}

func (failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, os.ErrDeadlineExceeded
}

func TestSubmitRejectsBeforeCreatingJob(t *testing.T) {
	stubPorts(t, "80/tcp")
	e := stubEngine(t, compatibleDocker)
	a := Action{ID: "12345678", Action: "image.create", Target: "web", Image: "nginx", Ports: []Binding{{Host: "0.0.0.0", Container: 80, Published: 80, Protocol: "tcp"}}}
	if _, err := e.submit(a); err == nil || !strings.Contains(err.Error(), "NAS port 80/tcp is already in use") {
		t.Fatalf("taken port accepted: %v", err)
	}
	if len(e.jobs) != 0 || e.Active() != 0 {
		t.Fatal("rejected request left a job", e.jobs)
	}
	if _, err := os.Stat(filepath.Join(e.root, "jobs.json")); !os.IsNotExist(err) {
		t.Fatal("rejected request was recorded")
	}
}

func TestSubmitRepeatedRequestSkipsPreflight(t *testing.T) {
	stubPorts(t, "80/tcp")
	e := stubEngine(t, compatibleDocker)
	a := Action{ID: "12345678", Action: "image.create", Target: "web", Image: "nginx", Ports: []Binding{{Host: "0.0.0.0", Container: 80, Published: 80, Protocol: "tcp"}}}
	raw, _ := json.Marshal(a)
	sum := sha256.Sum256(raw)
	e.jobs = []Job{{ID: a.ID, Action: a.Action, Target: a.Target, Status: "succeeded", Digest: hex.EncodeToString(sum[:])}}
	job, err := e.submit(a)
	if err != nil || job.Status != "succeeded" {
		t.Fatalf("an accepted creation must be returned, not validated again: %v %v", job, err)
	}
	e.busy = true
	a.ID = "87654321"
	if _, err = e.submit(a); err == nil || err.Error() != msgBusy {
		t.Fatalf("busy engine must answer before the pre-flight: %v", err)
	}
}

func TestProposePort(t *testing.T) {
	stubPorts(t, "80/tcp", "8080/tcp", "8081/tcp", "60000/tcp")
	none := map[string]bool{}
	if got := proposePort("0.0.0.0", 80, "tcp", none); got != 8082 {
		t.Fatal("next free port above container port + 8000 expected", got)
	}
	if got := proposePort("0.0.0.0", 80, "tcp", map[string]bool{"8082/tcp": true}); got != 8083 {
		t.Fatal("a port another row uses must be skipped", got)
	}
	if got := proposePort("0.0.0.0", 80, "udp", none); got != 8080 {
		t.Fatal("protocols are independent", got)
	}
	if got := proposePort("0.0.0.0", 60000, "tcp", none); got != 30000 {
		t.Fatal("proposal must stay within the port range", got)
	}
	original := portTaken
	probes := 0
	portTaken = func(string, int, string) bool { probes++; return true }
	got := proposePort("0.0.0.0", 80, "tcp", none)
	portTaken = original
	if got != 0 || probes != proposalAttempts {
		t.Fatal("scan must stop after a handful of probes", got, probes)
	}
}

func TestProposeDefaults(t *testing.T) {
	stubPorts(t, "80/tcp", "443/tcp")
	ports := []Binding{{Host: "0.0.0.0", Container: 80, Published: 80, Protocol: "tcp"}, {Host: "0.0.0.0", Container: 443, Published: 443, Protocol: "tcp"}, {Host: "0.0.0.0", Container: 8080, Published: 8080, Protocol: "tcp"}, {Host: "0.0.0.0", Container: 53, Published: 53, Protocol: "udp"}}
	proposeDefaults(ports)
	if ports[0].Published != 8081 || ports[1].Published != 8443 || ports[2].Published != 8080 || ports[3].Published != 53 {
		t.Fatalf("free numbers are kept and taken ones replaced without collisions: %+v", ports)
	}
	if ports[0].Container != 80 {
		t.Fatal("container port changed")
	}
}

func TestImageConfigDefaultsGetFreePorts(t *testing.T) {
	stubPorts(t, "80/tcp")
	e := stubEngine(t, compatibleDocker)
	config, err := e.docker.ImageConfig(context.Background(), "nginx")
	if err != nil || len(config.Ports) != 3 {
		t.Fatal(config, err)
	}
	proposeDefaults(config.Ports)
	if config.Ports[1].Container != 80 || config.Ports[1].Published != 8081 || config.Ports[2].Published != 8080 || config.Ports[0].Published != 53 {
		t.Fatalf("%+v", config.Ports)
	}
}

func TestProbePorts(t *testing.T) {
	stubPorts(t, "80/tcp", "8080/tcp")
	result := probePorts([]Binding{{Container: 80, Published: 80, Protocol: "tcp"}, {Container: 80, Published: 8080, Protocol: "tcp"}, {Container: 80, Published: 9000, Protocol: "tcp"}, {Container: 80, Published: 0, Protocol: "tcp"}})
	if result[0].Free || result[0].Suggested != 8081 || result[1].Free || result[1].Suggested != 8082 || !result[2].Free || !result[3].Free || result[2].Suggested != 0 {
		t.Fatalf("%+v", result)
	}
}

func TestPortTakenProbe(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	if !portTaken("127.0.0.1", port, "tcp") || !portTaken("", port, "tcp") {
		t.Fatal("listening port reported free")
	}
	if portTaken("127.0.0.1", port, "udp") {
		t.Fatal("UDP is independent of a TCP listener")
	}
	l.Close()
	if portTaken("127.0.0.1", port, "tcp") {
		t.Fatal("released port reported taken")
	}
}

// Every user-correctable backend message needs a "server.*" locale entry, or
// the interface shows it in English only.
func TestUserMessagesHaveTranslations(t *testing.T) {
	placeholder := regexp.MustCompile(`%[ds]`)
	for _, lang := range []string{"en", "ru", "uk"} {
		raw, err := os.ReadFile(filepath.Join("..", "..", "frontend", "locales", lang+".json"))
		if err != nil {
			t.Fatal(err)
		}
		var locale map[string]string
		if err = json.Unmarshal(raw, &locale); err != nil {
			t.Fatal(err)
		}
		server := map[string]string{}
		for key, value := range locale {
			if strings.HasPrefix(key, "server.") {
				server[value] = key
			}
		}
		for _, message := range userMessages {
			n := 0
			source := placeholder.ReplaceAllStringFunc(message, func(string) string { n++; return fmt.Sprintf("{{v%d}}", n) })
			if lang == "en" {
				if server[source] == "" {
					t.Errorf("en.json has no server.* entry for %q", source)
				}
				continue
			}
			var en map[string]string
			raw, _ := os.ReadFile(filepath.Join("..", "..", "frontend", "locales", "en.json"))
			_ = json.Unmarshal(raw, &en)
			for key, value := range en {
				if value != source || !strings.HasPrefix(key, "server.") {
					continue
				}
				for i := 1; i <= n; i++ {
					if !strings.Contains(locale[key], fmt.Sprintf("{{v%d}}", i)) {
						t.Errorf("%s.json %s lost placeholder v%d", lang, key, i)
					}
				}
				if locale[key] == "" || locale[key] == value {
					t.Errorf("%s.json %s is not translated", lang, key)
				}
			}
		}
	}
}

func TestOnlyPendingCreatesCanBeRetried(t *testing.T) {
	dir := t.TempDir()
	if retryableCreate(dir) {
		t.Fatal("existing projects must not be replaced")
	}
	for _, raw := range []string{`{}`, `{"pending":false}`, `invalid`} {
		os.WriteFile(filepath.Join(dir, "pending-create.json"), []byte(raw), 0600)
		if retryableCreate(dir) {
			t.Fatal("invalid marker accepted")
		}
	}
	os.WriteFile(filepath.Join(dir, "pending-create.json"), []byte(`{"pending":true}`), 0600)
	if !retryableCreate(dir) {
		t.Fatal("failed first start should permit retry")
	}
}

func TestRetryFailedFirstStartPreservesProjectData(t *testing.T) {
	stubPorts(t)
	docker := dockerStub{}
	for k, v := range compatibleDocker {
		docker[k] = v
	}
	docker["/info"] = `{"OSType":"linux","Architecture":"x86_64","DockerRootDir":"/var/lib/docker"}`
	e := stubEngine(t, docker)
	bin := t.TempDir()
	failure := filepath.Join(bin, "fail")
	os.WriteFile(failure, []byte("fail"), 0600)
	script := "#!/bin/sh\ncase \" $* \" in\n*' config '*) echo '{\"services\":{\"app\":{\"image\":\"nginx\"}}}';;\n*) test ! -f '" + failure + "';;\nesac\n"
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	a := Action{ID: "12345678", Action: "image.create", Target: "retry", Image: "nginx"}
	if err := e.saveProject(context.Background(), a); err == nil {
		t.Fatal("expected failed start")
	}
	dir := filepath.Join(e.root, "projects", a.Target)
	if !retryableCreate(dir) {
		t.Fatal("missing retry marker")
	}
	os.WriteFile(filepath.Join(dir, "preserve"), []byte("data"), 0600)
	if issues := e.preflight(context.Background(), a); len(issues) != 0 {
		t.Fatal(issues)
	}
	os.Remove(failure)
	if err := e.saveProject(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if retryableCreate(dir) {
		t.Fatal("successful creation still retryable")
	}
	if data, err := os.ReadFile(filepath.Join(dir, "preserve")); err != nil || string(data) != "data" {
		t.Fatal("project data lost")
	}
	if err := e.saveProject(context.Background(), a); err == nil {
		t.Fatal("must not overwrite a successful project")
	}
}
