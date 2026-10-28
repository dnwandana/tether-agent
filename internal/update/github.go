package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DefaultAPIBase is the GitHub API base of the repository.
const DefaultAPIBase = "https://api.github.com/repos/dnwandana/tether-agent"

// maxReleaseBytes is the largest release body that the client reads.
const maxReleaseBytes = 1 << 20

// releasePlatforms holds each GOOS/GOARCH pair that has a release archive.
var releasePlatforms = map[string]bool{"linux/amd64": true, "linux/arm64": true, "darwin/arm64": true}

// Release is one GitHub release of Tether Agent.
type Release struct {
	Version     string            // "0.4.0", the tag without the v
	Tag         string            // "v0.4.0"
	PublishedAt time.Time         // from published_at
	Assets      map[string]string // asset name -> browser_download_url
}

// Client reads releases from the GitHub API.
type Client struct {
	HC        *http.Client // nil uses a client with a 30 s timeout
	APIBase   string       // "" uses DefaultAPIBase
	UserAgent string       // "" uses "tether-agent"
}

// Latest returns the newest release that is not a draft or a pre-release.
func (c *Client) Latest(ctx context.Context) (Release, error) {
	return c.get(ctx, "/releases/latest")
}

// Tag returns the release of the tag, for example "v0.4.0".
func (c *Client) Tag(ctx context.Context, tag string) (Release, error) {
	return c.get(ctx, "/releases/tags/"+url.PathEscape(tag))
}

// httpClient returns HC, or a client with a 30 s timeout when HC is nil.
func (c *Client) httpClient() *http.Client {
	if c.HC != nil {
		return c.HC
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// downloadClient returns HC, or a client with no total timeout when HC is nil. The archive
// can need more than 30 s on a slow link, so the context of the download limits its time.
func (c *Client) downloadClient() *http.Client {
	if c.HC != nil {
		return c.HC
	}
	return &http.Client{}
}

// userAgent returns UserAgent, or "tether-agent" when UserAgent is empty.
func (c *Client) userAgent() string {
	if c.UserAgent != "" {
		return c.UserAgent
	}
	return "tether-agent"
}

func (c *Client) apiBase() string {
	if c.APIBase != "" {
		return strings.TrimSuffix(c.APIBase, "/")
	}
	return DefaultAPIBase
}

// get reads one release from path, which is relative to the API base.
func (c *Client) get(ctx context.Context, path string) (Release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.apiBase()+path, nil)
	if err != nil {
		return Release{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", c.userAgent())
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return Release{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		if (resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests) &&
			resp.Header.Get("X-RateLimit-Remaining") == "0" {
			return Release{}, rateLimitError(resp.Header.Get("X-RateLimit-Reset"))
		}
		return Release{}, fmt.Errorf("GitHub returned %s for %s", resp.Status, path)
	}

	var body struct {
		TagName     string    `json:"tag_name"`
		PublishedAt time.Time `json:"published_at"`
		Assets      []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	// The limit stops a large body. The decoder then fails on the cut JSON.
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxReleaseBytes)).Decode(&body); err != nil {
		return Release{}, fmt.Errorf("read the GitHub release from %s: %w", path, err)
	}
	if body.TagName == "" {
		return Release{}, fmt.Errorf("the GitHub release from %s has no tag_name", path)
	}
	rel := Release{
		Version:     strings.TrimPrefix(body.TagName, "v"),
		Tag:         body.TagName,
		PublishedAt: body.PublishedAt,
		Assets:      make(map[string]string, len(body.Assets)),
	}
	for _, a := range body.Assets {
		rel.Assets[a.Name] = a.URL
	}
	return rel, nil
}

// rateLimitError gives the time of X-RateLimit-Reset as local time.
func rateLimitError(reset string) error {
	n, err := strconv.ParseInt(reset, 10, 64)
	if err != nil {
		return errors.New("GitHub rate limit reached; try again later")
	}
	return fmt.Errorf("GitHub rate limit reached; try again after %s",
		time.Unix(n, 0).Local().Format("2006-01-02 15:04:05"))
}

// AssetName returns the name of the release archive for the platform.
// A platform without a release archive is an error.
func AssetName(version, goos, goarch string) (string, error) {
	if !releasePlatforms[goos+"/"+goarch] {
		return "", fmt.Errorf("no release for %s/%s; the release platforms are linux/amd64, linux/arm64, and darwin/arm64", goos, goarch)
	}
	return "tether-agent_" + version + "_" + goos + "_" + goarch + ".tar.gz", nil
}
