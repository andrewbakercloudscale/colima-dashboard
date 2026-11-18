package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// ImageConfig is the subset of /images/{ref}/json the bloat check reads.
type ImageConfig struct {
	ID           string `json:"Id"`
	RepoTags     []string
	Created      string
	Size         int64
	Os           string
	Architecture string
	Config       struct {
		User       string
		Env        []string
		Cmd        []string
		Entrypoint []string
		WorkingDir string
	}
	RootFS struct {
		Layers []string
	}
}

// HistoryEntry is one row of /images/{ref}/history: a build step and the bytes it added.
type HistoryEntry struct {
	ID        string `json:"Id"`
	Created   int64
	CreatedBy string
	Size      int64
	Comment   string
	Tags      []string
}

// ImageInspect returns an image's configuration.
func (c *Client) ImageInspect(ctx context.Context, ref string) (*ImageConfig, error) {
	if !ValidRef(ref) {
		return nil, fmt.Errorf("invalid image reference %q", ref)
	}
	var out ImageConfig
	return &out, c.getJSON(ctx, "/images/"+url.PathEscape(ref)+"/json", nil, &out)
}

// ImageHistory returns an image's build steps, newest first, as the engine reports them.
func (c *Client) ImageHistory(ctx context.Context, ref string) ([]HistoryEntry, error) {
	if !ValidRef(ref) {
		return nil, fmt.Errorf("invalid image reference %q", ref)
	}
	var out []HistoryEntry
	return out, c.getJSON(ctx, "/images/"+url.PathEscape(ref)+"/history", nil, &out)
}

// ExportImage streams an image's final filesystem as a tar to fn. It creates a container that is
// never started, exports it and removes it again, so the image and its containers are untouched.
func (c *Client) ExportImage(ctx context.Context, ref string, fn func(io.Reader) error) error {
	if !ValidRef(ref) {
		return fmt.Errorf("invalid image reference %q", ref)
	}
	body, _ := json.Marshal(map[string]any{
		"Image":           ref,
		"Cmd":             []string{"true"},
		"NetworkDisabled": true,
		"Labels":          map[string]string{"io.github.colima-dashboard.bloat": "1"},
	})
	resp, err := c.do(ctx, http.MethodPost, "/containers/create", nil, bytes.NewReader(body))
	if err != nil {
		return err
	}
	var created struct{ ID string }
	err = json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	if err != nil || created.ID == "" {
		return fmt.Errorf("could not create a temporary container from %s: %v", ref, err)
	}
	defer func() {
		rm, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = c.RemoveContainer(rm, created.ID, true)
	}()
	r, err := c.Raw(ctx, "/containers/"+created.ID+"/export", nil)
	if err != nil {
		return err
	}
	defer r.Close()
	return fn(r)
}

// HelperLabel marks the short-lived containers Colima Dashboard starts for itself.
const HelperLabel = "io.github.colima-dashboard.helper"

// RunHelper starts a container from image running cmd with the given host settings (for example
// Privileged and PidMode "host") and returns its id. It removes itself when cmd ends.
func (c *Client) RunHelper(ctx context.Context, image, name string, cmd []string, host map[string]any) (string, error) {
	if !ValidRef(image) {
		return "", fmt.Errorf("invalid image reference %q", image)
	}
	host["AutoRemove"] = true
	body, _ := json.Marshal(map[string]any{
		"Image": image, "Cmd": cmd, "HostConfig": host,
		"Labels": map[string]string{HelperLabel: name},
	})
	q := url.Values{}
	if name != "" {
		q.Set("name", name)
	}
	resp, err := c.do(ctx, http.MethodPost, "/containers/create", q, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	var created struct{ ID string }
	err = json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	if err != nil || created.ID == "" {
		return "", fmt.Errorf("could not create a helper container from %s: %v", image, err)
	}
	if err := c.ContainerAction(ctx, created.ID, "start"); err != nil {
		_ = c.RemoveContainer(context.Background(), created.ID, true)
		return "", err
	}
	return created.ID, nil
}
