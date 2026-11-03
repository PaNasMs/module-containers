package engine

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

type planImage struct {
	Reference string `json:"reference"`
	Status    string `json:"status"`
	ID        string `json:"id,omitempty"`
}

func (d *Docker) planImage(ctx context.Context, ref string, host Platform) (planImage, error) {
	result := planImage{Reference: ref}
	var local struct{ ID, Os, Architecture, Variant string }
	err := d.Call(ctx, "GET", "/images/"+url.PathEscape(ref)+"/json", nil, &local)
	var response *dockerResponseError
	if errors.As(err, &response) && response.status == http.StatusNotFound {
		result.Status = "missing"
		if strings.HasPrefix(ref, "sha256:") {
			result.Status = "unavailable"
		}
		return result, nil
	}
	if err != nil {
		return result, err
	}
	result.ID = local.ID
	result.Status = compatibility(host, []Platform{{OS: local.Os, Architecture: archName(local.Architecture), Variant: local.Variant}})
	if result.ID == "" {
		result.Status = "unknown"
	}
	return result, nil
}

func (e *Engine) inspectProjectImages(ctx context.Context, services map[string]any, host Platform) ([]planImage, error) {
	refs := map[string]planImage{}
	for _, value := range services {
		svc := value.(map[string]any)
		ref := svc["image"].(string)
		if _, ok := refs[ref]; !ok {
			item, err := e.docker.planImage(ctx, ref, host)
			if err != nil {
				return nil, err
			}
			refs[ref] = item
		}
		if required, _ := svc["platform"].(string); required != "" {
			parts := strings.Split(required, "/")
			if len(parts) < 2 || len(parts) > 3 {
				return nil, fmt.Errorf("Invalid service platform: %s", required)
			}
			need := Platform{OS: parts[0], Architecture: archName(parts[1])}
			if len(parts) == 3 {
				need.Variant = parts[2]
			}
			if compatibility(host, []Platform{need}) != "compatible" {
				item := refs[ref]
				item.Status = "incompatible"
				refs[ref] = item
			}
		}
	}
	images := make([]planImage, 0, len(refs))
	for _, image := range refs {
		images = append(images, image)
	}
	sort.Slice(images, func(i, j int) bool { return images[i].Reference < images[j].Reference })
	return images, nil
}
func (e *Engine) pullMissing(ctx context.Context, id string, images []planImage) error {
	for _, image := range images {
		if image.Status != "compatible" && image.Status != "missing" {
			return errors.New(msgImageIncompatible)
		}
	}
	for _, image := range images {
		if image.Status != "missing" {
			continue
		}
		e.stage(id, "Downloading missing image: "+image.Reference)
		if _, err := command(ctx, "", "docker", "pull", image.Reference); err != nil {
			return err
		}
	}
	return nil
}
