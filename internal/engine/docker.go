package engine

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Docker struct{ Client *http.Client }

func NewDocker() *Docker {
	return &Docker{&http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", "/var/run/docker.sock")
	}}, Timeout: 30 * time.Second}}
}
func (d *Docker) Call(ctx context.Context, method, path string, body any, out any) error {
	var b io.Reader
	if body != nil {
		raw, e := json.Marshal(body)
		if e != nil {
			return e
		}
		b = bytes.NewReader(raw)
	}
	req, e := http.NewRequestWithContext(ctx, method, "http://docker"+path, b)
	if e != nil {
		return e
	}
	req.Header.Set("Content-Type", "application/json")
	resp, e := d.Client.Do(req)
	if e != nil {
		return fmt.Errorf("Docker Engine unavailable: %w", e)
	}
	defer resp.Body.Close()
	raw, e := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if e != nil {
		return e
	}
	if resp.StatusCode >= 300 {
		var msg struct{ Message string }
		_ = json.Unmarshal(raw, &msg)
		return fmt.Errorf("Docker: %s", msg.Message)
	}
	if out != nil && len(raw) > 0 {
		return json.Unmarshal(raw, out)
	}
	return nil
}
func (d *Docker) Logs(ctx context.Context, id string) (string, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://docker/containers/"+url.PathEscape(id)+"/logs?stdout=1&stderr=1&tail=150&timestamps=1", nil)
	resp, e := d.Client.Do(req)
	if e != nil {
		return "", e
	}
	defer resp.Body.Close()
	raw, e := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if e != nil {
		return "", e
	}
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("Cannot read logs: %s", strings.TrimSpace(string(raw)))
	}
	return demux(raw), nil
}
func demux(raw []byte) string {
	var b strings.Builder
	for len(raw) >= 8 && raw[0] <= 2 && raw[1] == 0 && raw[2] == 0 && raw[3] == 0 {
		n := int(binary.BigEndian.Uint32(raw[4:8]))
		if n > len(raw)-8 {
			break
		}
		b.Write(raw[8 : 8+n])
		raw = raw[8+n:]
	}
	b.Write(raw)
	return b.String()
}

type Container struct {
	ID     string            `json:"Id"`
	Names  []string          `json:"Names"`
	Image  string            `json:"Image"`
	State  string            `json:"State"`
	Status string            `json:"Status"`
	Labels map[string]string `json:"Labels"`
	Ports  []Port            `json:"Ports"`
}
type Port struct {
	IP          string
	PrivatePort int
	PublicPort  int
	Type        string
}
type Image struct {
	PlatformCheck PlatformCheck `json:"platformCheck"`
	ID            string        `json:"Id"`
	RepoTags      []string
	Size          int64
}
type Network struct {
	ID       string `json:"Id"`
	Name     string
	Driver   string
	Internal bool
}
type Volume struct {
	Name       string
	Driver     string
	Mountpoint string
}

type ImageSearchResult struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Official    bool   `json:"is_official"`
	Stars       int    `json:"star_count"`
}

func (d *Docker) Search(ctx context.Context, term string) ([]ImageSearchResult, error) {
	results := []ImageSearchResult{}
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	err := d.Call(ctx, "GET", "/images/search?limit=15&term="+url.QueryEscape(term), nil, &results)
	return results, err
}
