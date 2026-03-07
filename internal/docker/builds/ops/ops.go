// Package dockerbuildops wraps the Docker image-build API with structured
// logging, build-context archiving, and build-event stream parsing.
package dockerbuildops

import (
	"archive/tar"
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	docker "dokoko.ai/dokoko/internal/docker"
	"dokoko.ai/dokoko/pkg/logger"
	dockertypes "github.com/docker/docker/api/types"
)

// ── Request / Response types ──────────────────────────────────────────────────

// BuildRequest describes a Docker image build job.
// Exactly one of ContextDir, ContextTar, or RemoteContext must be set.
type BuildRequest struct {
	// ContextDir is the local directory to archive as the build context.
	// Mutually exclusive with ContextTar and RemoteContext.
	ContextDir string

	// ContextTar is a pre-created tar archive of the build context.
	// Mutually exclusive with ContextDir and RemoteContext.
	ContextTar io.Reader

	// RemoteContext is a URL to a remote build context — a Git repository URL
	// (e.g. "https://github.com/user/repo.git#branch:subdir") or an HTTP URL
	// pointing to a tar archive.  When set, ContextDir and ContextTar are
	// ignored and no local tar is created.
	RemoteContext string

	// Dockerfile is the path to the Dockerfile relative to the context root.
	// Defaults to "Dockerfile".
	Dockerfile string

	// Tags to apply to the resulting image (e.g. "myapp:1.0", "myapp:latest").
	Tags []string

	// BuildArgs are build-time ARG variables.
	BuildArgs map[string]*string

	// Target selects a named build stage in a multi-stage Dockerfile.
	Target string

	// NoCache disables all layer caching.
	NoCache bool

	// Pull always attempts to pull a newer version of base images before build.
	Pull bool

	// Labels to attach to the resulting image.
	Labels map[string]string

	// Platform for the built image (e.g. "linux/amd64", "linux/arm64").
	Platform string

	// CacheFrom is a list of image references to use as cache sources.
	CacheFrom []string

	// SuppressOutput suppresses verbose build output when true.
	SuppressOutput bool
}

// BuildResponse carries the result of a completed build.
type BuildResponse struct {
	// ImageID is the full SHA256 image ID produced by the daemon.
	// May be empty for some BuildKit configurations that do not emit aux.
	ImageID string

	// Log contains each line of build output emitted by the daemon.
	Log []string
}

func (r *BuildRequest) validate() error {
	n := 0
	if r.ContextDir != "" {
		n++
	}
	if r.ContextTar != nil {
		n++
	}
	if r.RemoteContext != "" {
		n++
	}
	if n > 1 {
		return fmt.Errorf("only one of ContextDir, ContextTar, or RemoteContext may be set")
	}
	if n == 0 {
		return fmt.Errorf("one of ContextDir, ContextTar, or RemoteContext is required")
	}
	return nil
}

func (r *BuildRequest) toSDKOptions() dockertypes.ImageBuildOptions {
	df := r.Dockerfile
	if df == "" {
		df = "Dockerfile"
	}
	return dockertypes.ImageBuildOptions{
		Dockerfile:     df,
		Tags:           r.Tags,
		BuildArgs:      r.BuildArgs,
		Target:         r.Target,
		NoCache:        r.NoCache,
		PullParent:     r.Pull,
		Labels:         r.Labels,
		Platform:       r.Platform,
		CacheFrom:      r.CacheFrom,
		SuppressOutput: r.SuppressOutput,
		Remove:         true, // always remove intermediate containers
		RemoteContext:  r.RemoteContext,
	}
}

// ── Ops ───────────────────────────────────────────────────────────────────────

// Ops provides build-level operations against a Docker daemon.
type Ops struct {
	conn *docker.Connection
	log  *logger.Logger
}

// New constructs an Ops bound to an existing Connection.
func New(conn *docker.Connection, log *logger.Logger) *Ops {
	log.LowTrace("initialising build ops")
	log.Trace("build ops bound to connection %p", conn)
	return &Ops{conn: conn, log: log}
}

// Build executes a Docker image build.  It archives ContextDir into a tar (if
// needed), streams the build output, and returns the resulting image ID and
// a line-by-line log of all build output.
func (o *Ops) Build(ctx context.Context, req BuildRequest) (BuildResponse, error) {
	o.log.LowTrace("build: tags=%v dockerfile=%q target=%q platform=%q",
		req.Tags, req.Dockerfile, req.Target, req.Platform)

	if err := req.validate(); err != nil {
		return BuildResponse{}, fmt.Errorf("invalid build request: %w", err)
	}

	opts := req.toSDKOptions()

	var buildCtx io.Reader
	switch {
	case req.RemoteContext != "":
		o.log.Debug("build: using remote context %q", req.RemoteContext)
		// buildCtx stays nil — the daemon fetches it via RemoteContext.
	case req.ContextTar != nil:
		o.log.Debug("build: using pre-created tar context")
		buildCtx = req.ContextTar
	default:
		o.log.Debug("build: archiving context directory %q", req.ContextDir)
		var err error
		buildCtx, err = archiveDir(req.ContextDir)
		if err != nil {
			return BuildResponse{}, fmt.Errorf("archive context dir %q: %w", req.ContextDir, err)
		}
	}

	o.log.Trace("build: calling client.ImageBuild")
	resp, err := o.conn.Client().ImageBuild(ctx, buildCtx, opts)
	if err != nil {
		o.log.Error("build: ImageBuild call failed: %v", err)
		return BuildResponse{}, fmt.Errorf("image build: %w", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			o.log.Warn("build: non-fatal: close response body: %v", cerr)
		}
	}()

	o.log.Debug("build: stream open, draining output (builderVersion=%s)", resp.OSType)
	return o.streamBuildResponse(resp.Body, req.Tags)
}

// PruneCache removes unused build cache entries.
func (o *Ops) PruneCache(ctx context.Context, opts dockertypes.BuildCachePruneOptions) (*dockertypes.BuildCachePruneReport, error) {
	o.log.LowTrace("build cache prune: all=%v keepStorage=%d", opts.All, opts.KeepStorage)

	report, err := o.conn.Client().BuildCachePrune(ctx, opts)
	if err != nil {
		o.log.Error("BuildCachePrune failed: %v", err)
		return nil, fmt.Errorf("build cache prune: %w", err)
	}

	o.log.Debug("build cache prune: deleted=%d spaceReclaimed=%d",
		len(report.CachesDeleted), report.SpaceReclaimed)
	o.log.Info("build cache pruned: %d entries removed (%d bytes)",
		len(report.CachesDeleted), report.SpaceReclaimed)
	return report, nil
}

// ── Stream parsing ────────────────────────────────────────────────────────────

// buildEvent is the JSON shape streamed by ImageBuild.
type buildEvent struct {
	Stream      string           `json:"stream"`
	Status      string           `json:"status"`
	Error       string           `json:"error"`
	ErrorDetail struct {
		Message string `json:"message"`
		Code    int    `json:"code"`
	} `json:"errorDetail"`
	Aux *json.RawMessage `json:"aux"`
}

// streamBuildResponse drains the build event stream, collecting log lines and
// extracting the image ID from the aux field (BuildKit) or the
// "Successfully built" line (classic builder).
func (o *Ops) streamBuildResponse(body io.Reader, tags []string) (BuildResponse, error) {
	var lines []string
	var imageID string

	scanner := bufio.NewScanner(body)
	for scanner.Scan() {
		raw := scanner.Bytes()
		o.log.Trace("build event: %s", raw)

		var ev buildEvent
		if err := json.Unmarshal(raw, &ev); err != nil {
			o.log.Warn("build: unrecognised event (skipping): %s", raw)
			continue
		}

		if ev.Error != "" {
			o.log.Error("build: daemon error: %s", ev.Error)
			return BuildResponse{}, fmt.Errorf("build error: %s", ev.Error)
		}

		// BuildKit emits the final image ID in the "aux" field.
		if ev.Aux != nil && imageID == "" {
			var aux struct{ ID string }
			if err := json.Unmarshal(*ev.Aux, &aux); err == nil && aux.ID != "" {
				imageID = aux.ID
				o.log.Debug("build: aux image ID: %s", shortID(imageID))
			}
		}

		if line := strings.TrimRight(ev.Stream, "\n"); line != "" {
			lines = append(lines, line)
			o.log.Trace("build output: %s", line)

			// Classic builder emits the short ID here; normalise to the full
			// form by trusting the aux ID when present.
			if imageID == "" && strings.HasPrefix(line, "Successfully built ") {
				imageID = strings.TrimPrefix(line, "Successfully built ")
				o.log.Debug("build: parsed image ID from stream: %s", imageID)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return BuildResponse{}, fmt.Errorf("read build stream: %w", err)
	}

	if len(tags) > 0 {
		o.log.Info("build complete: tags=%v imageID=%s", tags, shortID(imageID))
	} else {
		o.log.Info("build complete: imageID=%s", shortID(imageID))
	}
	return BuildResponse{ImageID: imageID, Log: lines}, nil
}

// ── Context archiving ─────────────────────────────────────────────────────────

// archiveDir creates a streaming tar archive of dir suitable as a Docker build
// context.  The tar is written in a background goroutine; errors surface via
// the pipe.
func archiveDir(dir string) (io.Reader, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("stat context dir: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("context dir %q is not a directory", dir)
	}

	pr, pw := io.Pipe()
	go func() {
		if err := writeContextTar(pw, dir); err != nil {
			pw.CloseWithError(err)
			return
		}
		pw.Close()
	}()
	return pr, nil
}

// writeContextTar walks dir and writes every file and directory into tw.
// Symbolic links are preserved with their target.  The "." root entry is
// omitted so Docker treats the archive contents as the context root.
func writeContextTar(w io.Writer, dir string) error {
	tw := tar.NewWriter(w)
	defer tw.Close()

	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return fmt.Errorf("rel %s: %w", path, err)
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil // skip root — Docker doesn't need it
		}

		info, err := d.Info()
		if err != nil {
			return fmt.Errorf("stat %s: %w", path, err)
		}

		var linkTarget string
		if info.Mode()&os.ModeSymlink != 0 {
			if linkTarget, err = os.Readlink(path); err != nil {
				return fmt.Errorf("readlink %s: %w", path, err)
			}
		}

		hdr, err := tar.FileInfoHeader(info, linkTarget)
		if err != nil {
			return fmt.Errorf("tar header %s: %w", rel, err)
		}
		hdr.Name = rel

		if err := tw.WriteHeader(hdr); err != nil {
			return fmt.Errorf("write header %s: %w", rel, err)
		}

		if info.Mode().IsRegular() {
			f, err := os.Open(path)
			if err != nil {
				return fmt.Errorf("open %s: %w", path, err)
			}
			defer f.Close()
			if _, err := io.Copy(tw, f); err != nil {
				return fmt.Errorf("copy %s to tar: %w", rel, err)
			}
		}
		return nil
	})
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// shortID truncates a full SHA256 image ID to a readable 12-character prefix.
func shortID(id string) string {
	const prefix = "sha256:"
	s := id
	if strings.HasPrefix(s, prefix) {
		s = s[len(prefix):]
	}
	if len(s) > 12 {
		return s[:12]
	}
	return s
}
