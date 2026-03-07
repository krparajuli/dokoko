package dockerimageops

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"

	"dokoko.ai/dokoko/internal/docker"
	"dokoko.ai/dokoko/pkg/logger"
	dockertypes "github.com/docker/docker/api/types"
	dockerimage "github.com/docker/docker/api/types/image"
)

// Ops provides image-level operations against a Docker daemon.
type Ops struct {
	conn *docker.Connection
	log  *logger.Logger
}

// New constructs an Ops bound to an existing Connection.
func New(conn *docker.Connection, log *logger.Logger) *Ops {
	log.LowTrace("initialising image ops")
	log.Trace("image ops bound to connection %p", conn)
	return &Ops{conn: conn, log: log}
}

// pullEvent is the JSON message shape streamed back by ImagePull.
type pullEvent struct {
	Status         string `json:"status"`
	ID             string `json:"id"`
	Error          string `json:"error"`
	ProgressDetail struct {
		Current int64 `json:"current"`
		Total   int64 `json:"total"`
	} `json:"progressDetail"`
}

// Pull pulls an image by reference (e.g. "ubuntu:22.04").  The progress
// stream is consumed line-by-line; status events are logged at Debug level
// and errors in the stream surface as a returned error.
func (o *Ops) Pull(ctx context.Context, ref string, opts dockerimage.PullOptions) error {
	o.log.LowTrace("pulling image: %s", ref)
	o.log.Debug("pull options: platform=%q registryAuth=%v", opts.Platform, opts.RegistryAuth != "")

	rc, err := o.conn.Client().ImagePull(ctx, ref, opts)
	if err != nil {
		o.log.Error("failed to initiate pull for %q: %v", ref, err)
		return fmt.Errorf("pull %q: %w", ref, err)
	}
	defer func() {
		o.log.Trace("closing pull response body for %q", ref)
		if cerr := rc.Close(); cerr != nil {
			o.log.Warn("non-fatal: failed to close pull body for %q: %v", ref, cerr)
		}
	}()

	o.log.Debug("pull stream open for %q, draining progress", ref)

	scanner := bufio.NewScanner(rc)
	var lastStatus string
	var seenIDs []string

	for scanner.Scan() {
		line := scanner.Bytes()
		o.log.Trace("pull raw event: %s", line)

		var ev pullEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			o.log.Warn("pull: unrecognised event line (skipping): %s", line)
			continue
		}

		if ev.Error != "" {
			o.log.Error("pull stream error for %q: %s", ref, ev.Error)
			return fmt.Errorf("pull %q: daemon error: %s", ref, ev.Error)
		}

		if ev.Status != lastStatus {
			o.log.Debug("pull [%s] status: %s", ref, ev.Status)
			lastStatus = ev.Status
		}

		if ev.ID != "" {
			seen := false
			for _, id := range seenIDs {
				if id == ev.ID {
					seen = true
					break
				}
			}
			if !seen {
				seenIDs = append(seenIDs, ev.ID)
				o.log.Trace("pull [%s] new layer: %s", ref, ev.ID)
			}
		}

		if ev.ProgressDetail.Total > 0 {
			pct := float64(ev.ProgressDetail.Current) / float64(ev.ProgressDetail.Total) * 100
			o.log.Trace("pull [%s] layer %s: %.1f%% (%d/%d bytes)",
				ref, ev.ID, pct, ev.ProgressDetail.Current, ev.ProgressDetail.Total)
		}
	}

	if err := scanner.Err(); err != nil {
		o.log.Error("reading pull stream for %q: %v", ref, err)
		return fmt.Errorf("pull %q stream: %w", ref, err)
	}

	o.log.Debug("pull complete for %q: %d unique layers seen", ref, len(seenIDs))
	o.log.Info("image pulled: %s", ref)
	return nil
}

// List returns a slice of image summaries. Pass a zero-value ListOptions to
// list all images; set Filters / All as needed.
func (o *Ops) List(ctx context.Context, opts dockerimage.ListOptions) ([]dockerimage.Summary, error) {
	o.log.LowTrace("listing images")
	o.log.Debug("list options: all=%v filters=%v", opts.All, opts.Filters)

	images, err := o.conn.Client().ImageList(ctx, opts)
	if err != nil {
		o.log.Error("ImageList failed: %v", err)
		return nil, fmt.Errorf("image list: %w", err)
	}

	o.log.Debug("image list returned %d entries", len(images))

	for i, img := range images {
		o.log.Trace("image[%d]: id=%s tags=%v size=%d created=%d",
			i, shortID(img.ID), img.RepoTags, img.Size, img.Created)
	}

	o.log.Info("listed %d images", len(images))
	return images, nil
}

// Inspect returns detailed metadata for a single image by ID or reference.
func (o *Ops) Inspect(ctx context.Context, imageID string) (dockertypes.ImageInspect, error) {
	o.log.LowTrace("inspecting image: %s", imageID)

	resp, _, err := o.conn.Client().ImageInspectWithRaw(ctx, imageID)
	if err != nil {
		o.log.Error("ImageInspectWithRaw failed for %q: %v", imageID, err)
		return dockertypes.ImageInspect{}, fmt.Errorf("inspect %q: %w", imageID, err)
	}

	o.log.Debug("inspect response: id=%s tags=%v os=%s arch=%s size=%d",
		shortID(resp.ID), resp.RepoTags, resp.Os, resp.Architecture, resp.Size)
	o.log.Trace("inspect full: parent=%s created=%s docker_version=%s",
		shortID(resp.Parent), resp.Created, resp.DockerVersion)
	if resp.Config != nil {
		o.log.Trace("inspect config: cmd=%v entrypoint=%v exposedPorts=%d env=%d",
			resp.Config.Cmd, resp.Config.Entrypoint,
			len(resp.Config.ExposedPorts), len(resp.Config.Env))
	}
	o.log.Trace("inspect rootfs: type=%s layers=%d", resp.RootFS.Type, len(resp.RootFS.Layers))

	o.log.Info("inspected image %s (%s, %s, %d bytes)", shortID(resp.ID), resp.Os, resp.Architecture, resp.Size)
	return resp, nil
}

// Remove deletes an image by ID or reference.  Set opts.Force to remove even
// if a container is using the image; set opts.PruneChildren to also remove
// untagged parent images.
func (o *Ops) Remove(ctx context.Context, imageID string, opts dockerimage.RemoveOptions) ([]dockerimage.DeleteResponse, error) {
	o.log.LowTrace("removing image: %s", imageID)
	o.log.Debug("remove options: force=%v pruneChildren=%v", opts.Force, opts.PruneChildren)

	responses, err := o.conn.Client().ImageRemove(ctx, imageID, opts)
	if err != nil {
		o.log.Error("ImageRemove failed for %q: %v", imageID, err)
		return nil, fmt.Errorf("remove %q: %w", imageID, err)
	}

	o.log.Debug("remove returned %d response entries", len(responses))
	for i, r := range responses {
		if r.Deleted != "" {
			o.log.Trace("remove[%d] deleted: %s", i, shortID(r.Deleted))
		}
		if r.Untagged != "" {
			o.log.Trace("remove[%d] untagged: %s", i, r.Untagged)
		}
	}

	o.log.Info("removed image %s (%d layers deleted)", imageID, len(responses))
	return responses, nil
}

// Tag creates a new tag pointing at source.
func (o *Ops) Tag(ctx context.Context, source, target string) error {
	o.log.LowTrace("tagging image")
	o.log.Debug("tag: source=%q target=%q", source, target)

	if err := o.conn.Client().ImageTag(ctx, source, target); err != nil {
		o.log.Error("ImageTag failed (%q → %q): %v", source, target, err)
		return fmt.Errorf("tag %q as %q: %w", source, target, err)
	}

	o.log.Trace("tag request sent successfully")
	o.log.Info("tagged %q as %q", source, target)
	return nil
}

// Exists reports whether an image is present in the local store.
// It uses Inspect internally so the result is authoritative.
func (o *Ops) Exists(ctx context.Context, ref string) (bool, error) {
	o.log.LowTrace("checking local existence: %s", ref)

	_, _, err := o.conn.Client().ImageInspectWithRaw(ctx, ref)
	if err != nil {
		if isNotFound(err) {
			o.log.Debug("image %q not found in local store", ref)
			return false, nil
		}
		o.log.Error("Exists check failed for %q: %v", ref, err)
		return false, fmt.Errorf("exists check %q: %w", ref, err)
	}

	o.log.Debug("image %q found in local store", ref)
	o.log.Info("image %q exists locally", ref)
	return true, nil
}

// shortID truncates a full image SHA256 ID to a readable 12-character prefix.
func shortID(id string) string {
	const prefix = "sha256:"
	s := id
	if len(s) > len(prefix) && s[:len(prefix)] == prefix {
		s = s[len(prefix):]
	}
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

// isNotFound returns true when the Docker daemon signals a 404-style error.
func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	// The Docker client wraps 404s with this message.
	msg := err.Error()
	for _, needle := range []string{"No such image", "not found", "404"} {
		for i := 0; i+len(needle) <= len(msg); i++ {
			if msg[i:i+len(needle)] == needle {
				return true
			}
		}
	}
	return false
}

// drainAndClose is a safety helper used in deferred closes.
func drainAndClose(rc io.ReadCloser) error {
	_, _ = io.Copy(io.Discard, rc)
	return rc.Close()
}
