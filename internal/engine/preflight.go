package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// User-correctable messages. The frontend translates them through its
// "server.*" locale keys (frontend/locales), where every %d/%s is a {{vN}}
// placeholder; TestUserMessagesHaveTranslations keeps both sides in step.
const (
	msgNameRule          = "Use a lowercase name, 2–48 letters, digits, hyphens or underscores"
	msgProjectNameRule   = "Use a lowercase project name, 2–48 letters, digits, hyphens or underscores"
	msgBusy              = "Another operation is running. Wait for it to finish."
	msgProjectExists     = "Project already exists"
	msgExternalProject   = "A Compose project with this name already exists outside this module"
	msgImageReference    = "Invalid image reference"
	msgImageUnavailable  = "Local image is unavailable; download it in Images first"
	msgImageIncompatible = "Local image architecture is incompatible or could not be verified"
	msgPortInUse         = "NAS port %d/%s is already in use. Choose another NAS port."
	msgPortRepeated      = "NAS port %d/%s is listed more than once"
	msgPortMapping       = "Invalid port mapping"
	msgBindAddress       = "Invalid host bind address"
	msgWebPort           = "Web interface port must match a published TCP port"
	msgEnvFormat         = "Environment variables must use NAME=value"
	msgEnvInvalid        = "Invalid environment variable"
	msgMountPath         = "Container mount path must be absolute and not root"
	msgHostFolder        = "Host folder unavailable: %s"
	msgVolumeName        = "Invalid volume name"
	msgNetworkName       = "Invalid network name"
)

var userMessages = []string{msgNameRule, msgProjectNameRule, msgBusy, msgProjectExists, msgExternalProject, msgImageReference, msgImageUnavailable, msgImageIncompatible, msgPortInUse, msgPortRepeated, msgPortMapping, msgBindAddress, msgWebPort, msgEnvFormat, msgEnvInvalid, msgMountPath, msgHostFolder, msgVolumeName, msgNetworkName}

// Problem is one issue the user can correct in the create form. Field is
// name, image, ports (Index is the row), webPort or general.
type Problem struct {
	Field string `json:"field"`
	Index int    `json:"index"`
	Error string `json:"error"`
}

// portTaken probes one NAS port by binding it on the requested address and
// closing it at once. Only "address in use" counts: other failures (an address
// that is not configured, a restricted port) are left for Docker to report.
var portTaken = func(host string, port int, protocol string) bool {
	if host == "" {
		host = "0.0.0.0"
	}
	address := net.JoinHostPort(host, fmt.Sprint(port))
	var err error
	if protocol == "udp" {
		var c net.PacketConn
		if c, err = net.ListenPacket("udp", address); err == nil {
			c.Close()
		}
	} else {
		var l net.Listener
		if l, err = net.Listen("tcp", address); err == nil {
			l.Close()
		}
	}
	return errors.Is(err, syscall.EADDRINUSE)
}

func validBinding(p Binding) error {
	if p.Container < 1 || p.Container > 65535 || p.Published < 1 || p.Published > 65535 || (p.Protocol != "tcp" && p.Protocol != "udp") {
		return errors.New(msgPortMapping)
	}
	if p.Host != "" && net.ParseIP(p.Host) == nil {
		return errors.New(msgBindAddress)
	}
	return nil
}

// overlap reports whether two listen addresses claim the same NAS port.
func overlap(a, b string) bool {
	if a == "" {
		a = "0.0.0.0"
	}
	if b == "" {
		b = "0.0.0.0"
	}
	x, y := net.ParseIP(a), net.ParseIP(b)
	if x == nil || y == nil {
		return false
	}
	return x.Equal(y) || ((x.IsUnspecified() || y.IsUnspecified()) && (x.To4() == nil) == (y.To4() == nil))
}

// bindingProblems validates every row, then probes the valid ones.
func bindingProblems(ports []Binding) []Problem {
	problems := []Problem{}
	for i, p := range ports {
		if err := validBinding(p); err != nil {
			problems = append(problems, Problem{"ports", i, err.Error()})
			continue
		}
		repeated := false
		for _, earlier := range ports[:i] {
			if validBinding(earlier) == nil && earlier.Published == p.Published && earlier.Protocol == p.Protocol && overlap(earlier.Host, p.Host) {
				repeated = true
			}
		}
		if repeated {
			problems = append(problems, Problem{"ports", i, fmt.Sprintf(msgPortRepeated, p.Published, p.Protocol)})
		} else if portTaken(p.Host, p.Published, p.Protocol) {
			problems = append(problems, Problem{"ports", i, fmt.Sprintf(msgPortInUse, p.Published, p.Protocol)})
		}
	}
	return problems
}

// preflight checks a "create from image" request before a job exists, so the
// form can show what to correct. The job repeats these checks: state can
// change between the two. Docker-dependent checks are skipped while Docker is
// unreachable; the job then reports that instead.
func (e *Engine) preflight(ctx context.Context, a Action) []Problem {
	problems := []Problem{}
	if a.Action != "image.create" {
		return problems
	}
	exists := false
	if !nameRE.MatchString(a.Target) {
		problems = append(problems, Problem{"name", 0, msgNameRule})
	} else if _, err := os.Stat(filepath.Join(e.root, "projects", a.Target)); err == nil && !retryableCreate(filepath.Join(e.root, "projects", a.Target)) {
		exists = true
		problems = append(problems, Problem{"name", 0, msgProjectExists})
	}
	imageValid := validImage(a.Image) == nil
	if !imageValid {
		problems = append(problems, Problem{"image", 0, msgImageReference})
	}
	problems = append(problems, bindingProblems(a.Ports)...)
	if a.WebPort > 0 {
		found := false
		for _, p := range a.Ports {
			found = found || (p.Published == a.WebPort && p.Protocol == "tcp")
		}
		if !found {
			problems = append(problems, Problem{"webPort", 0, msgWebPort})
		}
	}
	if imageValid {
		rest := a
		rest.Ports, rest.WebPort = nil, 0
		if _, err := imageCompose(rest); err != nil {
			problems = append(problems, Problem{"general", 0, err.Error()})
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var existing []Container
	if e.docker == nil || e.docker.Call(ctx, "GET", "/containers/json?all=1", nil, &existing) != nil {
		return problems
	}
	if retryableCreate(filepath.Join(e.root, "projects", a.Target)) {
		kept := problems[:0]
		for _, problem := range problems {
			if problem.Field == "ports" && problem.Index < len(a.Ports) && problem.Error == fmt.Sprintf(msgPortInUse, a.Ports[problem.Index].Published, a.Ports[problem.Index].Protocol) && ownsBinding(existing, a.Target, a.Ports[problem.Index]) {
				continue
			}
			kept = append(kept, problem)
		}
		problems = kept
	}
	if !exists && nameRE.MatchString(a.Target) && !retryableCreate(filepath.Join(e.root, "projects", a.Target)) {
		for _, c := range existing {
			if c.Labels["com.docker.compose.project"] == a.Target {
				problems = append(problems, Problem{"name", 0, msgExternalProject})
				break
			}
		}
	}
	if imageValid {
		if detail, err := e.docker.ImageConfig(ctx, a.Image); err != nil {
			problems = append(problems, Problem{"image", 0, err.Error()})
		} else if detail.PlatformCheck.Status != "compatible" {
			problems = append(problems, Problem{"image", 0, msgImageIncompatible})
		}
	}
	return problems
}

// PortProbe answers one row of a NAS port availability check.
type PortProbe struct {
	Free      bool `json:"free"`
	Suggested int  `json:"suggested,omitempty"`
}

const proposalAttempts = 8

func portKey(port int, protocol string) string { return fmt.Sprint(port, "/", protocol) }

// proposePort returns a free NAS port for a container port whose own number is
// taken: container port + 8000 (30000–39999 when that leaves the range), then
// upward, a handful of probes at most. Zero means nothing was found.
func proposePort(host string, port int, protocol string, reserved map[string]bool) int {
	start := port + 8000
	if start > 65535 {
		start = 30000 + port%10000
	}
	for candidate := start; candidate < start+proposalAttempts && candidate <= 65535; candidate++ {
		if !reserved[portKey(candidate, protocol)] && !portTaken(host, candidate, protocol) {
			return candidate
		}
	}
	return 0
}

// proposeDefaults keeps the container port number as the NAS port where it is
// free and replaces taken ones. Free numbers are claimed first so a proposal
// never lands on another row's default.
func proposeDefaults(ports []Binding) {
	reserved := map[string]bool{}
	taken := []int{}
	for i, p := range ports {
		if portTaken(p.Host, p.Published, p.Protocol) {
			taken = append(taken, i)
		} else {
			reserved[portKey(p.Published, p.Protocol)] = true
		}
	}
	for _, i := range taken {
		if port := proposePort(ports[i].Host, ports[i].Container, ports[i].Protocol, reserved); port > 0 {
			ports[i].Published = port
			reserved[portKey(port, ports[i].Protocol)] = true
		}
	}
}

// probePorts reports for each row whether its NAS port is free on its listen
// address and, when it is not, a free alternative no other row uses.
func probePorts(ports []Binding) []PortProbe {
	reserved := map[string]bool{}
	for _, p := range ports {
		reserved[portKey(p.Published, p.Protocol)] = true
	}
	result := make([]PortProbe, len(ports))
	for i, p := range ports {
		result[i].Free = true
		if validBinding(p) != nil || !portTaken(p.Host, p.Published, p.Protocol) {
			continue
		}
		result[i].Free = false
		if port := proposePort(p.Host, p.Container, p.Protocol, reserved); port > 0 {
			result[i].Suggested = port
			reserved[portKey(port, p.Protocol)] = true
		}
	}
	return result
}

// Failed first starts retain their data and can be reconciled by Compose on retry.
func retryableCreate(dir string) bool {
	raw, err := os.ReadFile(filepath.Join(dir, "pending-create.json"))
	var state struct {
		Pending bool `json:"pending"`
	}
	return err == nil && json.Unmarshal(raw, &state) == nil && state.Pending
}

func ownsBinding(containers []Container, project string, binding Binding) bool {
	host := binding.Host
	if host == "" {
		host = "0.0.0.0"
	}
	for _, c := range containers {
		if c.Labels["com.docker.compose.project"] != project {
			continue
		}
		for _, p := range c.Ports {
			if p.IP == host && p.PublicPort == binding.Published && p.PrivatePort == binding.Container && p.Type == binding.Protocol {
				return true
			}
		}
	}
	return false
}
