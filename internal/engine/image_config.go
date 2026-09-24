package engine

import (
	"context"
	"errors"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

type ImageConfig struct {
	ID            string            `json:"id"`
	PlatformCheck PlatformCheck     `json:"platformCheck"`
	Ports         []Binding         `json:"ports"`
	Environment   map[string]string `json:"environment"`
	Volumes       []string          `json:"volumes"`
	Addresses     []string          `json:"addresses"`
}

func (d *Docker) ImageConfig(ctx context.Context, reference string) (ImageConfig, error) {
	if validImage(reference) != nil || strings.ContainsAny(reference, "?#") {
		return ImageConfig{}, errors.New("Invalid image reference")
	}
	result := ImageConfig{Ports: []Binding{}, Environment: map[string]string{}, Volumes: []string{}, Addresses: []string{"0.0.0.0", "127.0.0.1", "::"}}
	var detail struct {
		ID, Os, Architecture, Variant string
		Config                        struct {
			ExposedPorts map[string]any
			Env          []string
			Volumes      map[string]any
		}
	}
	if err := d.Call(ctx, "GET", "/images/"+url.PathEscape(reference)+"/json", nil, &detail); err != nil {
		return result, errors.New("Local image is unavailable; download it in Images first")
	}
	host, err := d.HostPlatform(ctx)
	if err != nil {
		return result, err
	}
	result.ID = detail.ID
	platforms := []Platform{{OS: detail.Os, Architecture: archName(detail.Architecture), Variant: detail.Variant}}
	result.PlatformCheck = PlatformCheck{Host: host, Platforms: platforms, Status: compatibility(host, platforms)}
	for key := range detail.Config.ExposedPorts {
		number, protocol, ok := strings.Cut(key, "/")
		n, e := strconv.Atoi(number)
		if ok && e == nil && n > 0 && n <= 65535 && (protocol == "tcp" || protocol == "udp") {
			result.Ports = append(result.Ports, Binding{Host: "0.0.0.0", Container: n, Published: n, Protocol: protocol})
		}
	}
	sort.Slice(result.Ports, func(i, j int) bool {
		a, b := result.Ports[i], result.Ports[j]
		if a.Container == b.Container {
			return a.Protocol < b.Protocol
		}
		return a.Container < b.Container
	})
	for _, entry := range detail.Config.Env {
		if k, v, ok := strings.Cut(entry, "="); ok {
			result.Environment[k] = v
		}
	}
	for path := range detail.Config.Volumes {
		result.Volumes = append(result.Volumes, path)
	}
	sort.Strings(result.Volumes)
	addresses, _ := net.InterfaceAddrs()
	for _, address := range addresses {
		ip, _, err := net.ParseCIDR(address.String())
		if err == nil && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() {
			result.Addresses = append(result.Addresses, ip.String())
		}
	}
	return result, nil
}
