package engine

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
