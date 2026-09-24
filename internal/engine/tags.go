package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var hubRepositoryRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*/[a-z0-9][a-z0-9_.-]*$`)
var hubClient = &http.Client{Timeout: 12 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

type TagPage struct {
	Tags []string `json:"tags"`
	More bool     `json:"more"`
}

func hubTags(ctx context.Context, client *http.Client, repository string, page int) (TagPage, error) {
	result := TagPage{Tags: []string{}}
	repository = strings.TrimPrefix(repository, "docker.io/")
	if !strings.Contains(repository, "/") {
		repository = "library/" + repository
	}
	if !hubRepositoryRE.MatchString(repository) || len(repository) > 200 || page < 1 || page > 10000 {
		return result, fmt.Errorf("Invalid Docker Hub repository or page")
	}
	parts := strings.Split(repository, "/")
	address := "https://hub.docker.com/v2/namespaces/" + url.PathEscape(parts[0]) + "/repositories/" + url.PathEscape(parts[1]) + "/tags?page_size=100&page=" + strconv.Itoa(page)
	req, err := http.NewRequestWithContext(ctx, "GET", address, nil)
	if err != nil {
		return result, err
	}
	response, err := client.Do(req)
	if err != nil {
		return result, err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return result, fmt.Errorf("Docker Hub tags unavailable (%d)", response.StatusCode)
	}
	var payload struct {
		Next    *string `json:"next"`
		Results []struct {
			Name string `json:"name"`
		} `json:"results"`
	}
	if err = json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&payload); err != nil {
		return result, err
	}
	for _, tag := range payload.Results {
		result.Tags = append(result.Tags, tag.Name)
	}
	result.More = payload.Next != nil && *payload.Next != ""
	return result, nil
}
