package engine

import (
	"context"
	"net/url"
	"strings"
	"time"
)

type Platform struct {
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
	Variant      string `json:"variant,omitempty"`
}
type PlatformCheck struct {
	Host      Platform   `json:"host"`
	Platforms []Platform `json:"platforms"`
	Status    string     `json:"status"`
}

func archName(s string) string {
	switch s {
	case "aarch64":
		return "arm64"
	case "x86_64":
		return "amd64"
	case "armv7l", "armv6l":
		return "arm"
	}
	return s
}
func compatibility(host Platform, platforms []Platform) string {
	if host.OS == "" || host.Architecture == "" {
		return "unknown"
	}
	known, uncertain := false, false
	for _, p := range platforms {
		if p.OS == "" || p.Architecture == "" || p.OS == "unknown" || p.Architecture == "unknown" {
			continue
		}
		known = true
		if p.OS != host.OS || archName(p.Architecture) != archName(host.Architecture) {
			continue
		}
		if p.Variant == "" || p.Variant == host.Variant || (archName(host.Architecture) == "arm64" && p.Variant == "v8" && host.Variant == "") {
			return "compatible"
		}
		uncertain = true
	}
	if !known || uncertain {
		return "unknown"
	}
	return "incompatible"
}
func (d *Docker) HostPlatform(ctx context.Context) (Platform, error) {
	var info struct {
		OSType       string
		Architecture string
	}
	err := d.Call(ctx, "GET", "/info", nil, &info)
	return Platform{OS: info.OSType, Architecture: archName(info.Architecture)}, err
}
func (d *Docker) RemotePlatform(ctx context.Context, reference string) (PlatformCheck, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	result := PlatformCheck{Status: "unknown", Platforms: []Platform{}}
	host, err := d.HostPlatform(ctx)
	if err != nil {
		return result, err
	}
	result.Host = host
	var manifest struct{ Platforms []Platform }
	err = d.Call(ctx, "GET", "/distribution/"+url.PathEscape(reference)+"/json", nil, &manifest)
	if err != nil {
		return result, err
	}
	for _, p := range manifest.Platforms {
		if p.OS != "unknown" && p.Architecture != "unknown" && p.OS != "" && p.Architecture != "" {
			result.Platforms = append(result.Platforms, p)
		}
	}
	result.Status = compatibility(host, result.Platforms)
	return result, nil
}
func (d *Docker) LocalPlatform(ctx context.Context, id string, host Platform) PlatformCheck {
	result := PlatformCheck{Host: host, Status: "unknown", Platforms: []Platform{}}
	var detail struct {
		Os           string
		Architecture string
		Variant      string
	}
	if d.Call(ctx, "GET", "/images/"+url.PathEscape(id)+"/json", nil, &detail) != nil {
		return result
	}
	p := Platform{OS: strings.ToLower(detail.Os), Architecture: archName(detail.Architecture), Variant: detail.Variant}
	result.Platforms = []Platform{p}
	result.Status = compatibility(host, result.Platforms)
	return result
}
