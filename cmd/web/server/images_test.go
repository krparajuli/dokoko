package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	dockerimageactor "dokoko.ai/dokoko/internal/docker/images/actor"
	dockerimagestate "dokoko.ai/dokoko/internal/docker/images/state"
	dockertypes "github.com/docker/docker/api/types"
	dockerimage "github.com/docker/docker/api/types/image"
)

// ── List images ───────────────────────────────────────────────────────────────

func TestListImages_ReturnsStoreRecords(t *testing.T) {
	store := dockerimagestate.NewStore(silentLogger())
	store.Register(dockerimagestate.RegisterParams{
		DockerID:  "sha256:aabbcc112233",
		RepoTags:  []string{"ubuntu:22.04"},
		Size:      1024,
		Origin:    dockerimagestate.OriginOutOfBand,
	})
	store.Register(dockerimagestate.RegisterParams{
		DockerID:  "sha256:ddeeff445566",
		RepoTags:  []string{"alpine:latest"},
		Size:      512,
		Origin:    dockerimagestate.OriginOutOfBand,
	})

	h := newTestHandler(&fakeManager{
		images:     &fakeImageClerk{store: store},
		containers: &fakeContainerActor{},
		volumes:    &fakeVolumeClerk{},
		networks:   &fakeNetworkClerk{},
		exec:       &fakeExecActor{},
		summ:       &fakeStateSumm{},
	})

	rec := httptest.NewRecorder()
	h.listImages(rec, newReq("GET", "/api/images", nil))

	assertStatus(t, rec, http.StatusOK)
	resp := parseResp(t, rec)
	if resp.Data == nil {
		t.Fatal("expected data field in response")
	}

	// Verify JSON contains both images
	dataStr := string(resp.Data)
	if !containsStr(dataStr, "aabbcc112233") {
		t.Errorf("response missing aabbcc112233: %s", dataStr)
	}
	if !containsStr(dataStr, "ddeeff445566") {
		t.Errorf("response missing ddeeff445566: %s", dataStr)
	}
}

func TestListImages_EmptyStore(t *testing.T) {
	h := newTestHandler(defaultFake())
	rec := httptest.NewRecorder()
	h.listImages(rec, newReq("GET", "/api/images", nil))
	assertStatus(t, rec, http.StatusOK)
}

// ── Pull image ────────────────────────────────────────────────────────────────

func TestPullImage_Dispatches(t *testing.T) {
	var gotRef string
	fake := defaultFake()
	fake.images = &fakeImageClerk{
		pullFn: func(_ context.Context, ref string, _ dockerimage.PullOptions) (*dockerimageactor.Ticket, error) {
			gotRef = ref
			return closedImageTicket("chg-pull"), nil
		},
	}

	rec := httptest.NewRecorder()
	h := newTestHandler(fake)
	h.pullImage(rec, newReq("POST", "/api/images/pull", jsonBody(map[string]string{"ref": "ubuntu:22.04"})))

	assertStatus(t, rec, http.StatusAccepted)
	if gotRef != "ubuntu:22.04" {
		t.Errorf("Pull called with %q, want ubuntu:22.04", gotRef)
	}
}

func TestPullImage_MissingRef(t *testing.T) {
	rec := httptest.NewRecorder()
	newTestHandler(defaultFake()).pullImage(rec, newReq("POST", "/api/images/pull", jsonBody(map[string]string{})))
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestPullImage_ForwardsPlatform(t *testing.T) {
	var gotOpts dockerimage.PullOptions
	fake := defaultFake()
	fake.images = &fakeImageClerk{
		pullFn: func(_ context.Context, _ string, opts dockerimage.PullOptions) (*dockerimageactor.Ticket, error) {
			gotOpts = opts
			return closedImageTicket("chg-pull"), nil
		},
	}

	h := newTestHandler(fake)
	rec := httptest.NewRecorder()
	h.pullImage(rec, newReq("POST", "/api/images/pull", jsonBody(map[string]string{
		"ref":      "ubuntu:22.04",
		"platform": "linux/arm64",
	})))

	assertStatus(t, rec, http.StatusAccepted)
	if gotOpts.Platform != "linux/arm64" {
		t.Errorf("platform: got %q, want linux/arm64", gotOpts.Platform)
	}
}

func TestPullImage_PullError(t *testing.T) {
	fake := defaultFake()
	fake.images = &fakeImageClerk{
		pullFn: func(_ context.Context, _ string, _ dockerimage.PullOptions) (*dockerimageactor.Ticket, error) {
			return nil, errors.New("registry unavailable")
		},
	}

	rec := httptest.NewRecorder()
	newTestHandler(fake).pullImage(rec, newReq("POST", "/api/images/pull", jsonBody(map[string]string{"ref": "ubuntu:22.04"})))
	assertStatus(t, rec, http.StatusInternalServerError)
}

// ── Remove image ──────────────────────────────────────────────────────────────

func TestRemoveImage_Dispatches(t *testing.T) {
	var gotID string
	fake := defaultFake()
	fake.images = &fakeImageClerk{
		removeFn: func(_ context.Context, imageID string, _ dockerimage.RemoveOptions) (*dockerimageactor.Ticket, error) {
			gotID = imageID
			return closedImageTicket("chg-rm"), nil
		},
	}

	rec := httptest.NewRecorder()
	newTestHandler(fake).removeImage(rec, newReq("POST", "/api/images/remove", jsonBody(map[string]string{"id": "sha256:abc"})))

	assertStatus(t, rec, http.StatusAccepted)
	if gotID != "sha256:abc" {
		t.Errorf("Remove called with %q, want sha256:abc", gotID)
	}
}

func TestRemoveImage_MissingID(t *testing.T) {
	rec := httptest.NewRecorder()
	newTestHandler(defaultFake()).removeImage(rec, newReq("POST", "/api/images/remove", jsonBody(map[string]string{})))
	assertStatus(t, rec, http.StatusBadRequest)
}

// ── Tag image ─────────────────────────────────────────────────────────────────

func TestTagImage_Dispatches(t *testing.T) {
	var src, dst string
	fake := defaultFake()
	fake.images = &fakeImageClerk{
		tagFn: func(_ context.Context, source, target string) (*dockerimageactor.Ticket, error) {
			src, dst = source, target
			return closedImageTicket("chg-tag"), nil
		},
	}

	rec := httptest.NewRecorder()
	newTestHandler(fake).tagImage(rec, newReq("POST", "/api/images/tag", jsonBody(map[string]string{
		"source": "ubuntu:22.04",
		"target": "ubuntu:prod",
	})))

	assertStatus(t, rec, http.StatusAccepted)
	if src != "ubuntu:22.04" || dst != "ubuntu:prod" {
		t.Errorf("Tag args: got (%q, %q)", src, dst)
	}
}

func TestTagImage_MissingFields(t *testing.T) {
	rec := httptest.NewRecorder()
	newTestHandler(defaultFake()).tagImage(rec, newReq("POST", "/api/images/tag", jsonBody(map[string]string{"source": "only"})))
	assertStatus(t, rec, http.StatusBadRequest)
}

// ── Refresh images ────────────────────────────────────────────────────────────

func TestRefreshImages_OK(t *testing.T) {
	rec := httptest.NewRecorder()
	newTestHandler(defaultFake()).refreshImages(rec, newReq("POST", "/api/images/refresh", nil))
	assertStatus(t, rec, http.StatusOK)
}

func TestRefreshImages_Error(t *testing.T) {
	fake := defaultFake()
	fake.images = &fakeImageClerk{
		refreshFn: func(_ context.Context) error { return errors.New("daemon down") },
	}

	rec := httptest.NewRecorder()
	newTestHandler(fake).refreshImages(rec, newReq("POST", "/api/images/refresh", nil))
	assertStatus(t, rec, http.StatusInternalServerError)
}

// ── Inspect image ─────────────────────────────────────────────────────────────

func TestInspectImage_OK(t *testing.T) {
	fake := defaultFake()
	fake.images = &fakeImageClerk{
		inspectFn: func(_ context.Context, id string) <-chan dockerimageactor.InspectResult {
			ch := make(chan dockerimageactor.InspectResult, 1)
			ch <- dockerimageactor.InspectResult{
				Info: dockertypes.ImageInspect{ID: id, Os: "linux"},
			}
			return ch
		},
	}

	r := newReq("GET", "/api/images/sha256:abc/inspect", nil)
	r.SetPathValue("id", "sha256:abc")
	rec := httptest.NewRecorder()
	newTestHandler(fake).inspectImage(rec, r)

	assertStatus(t, rec, http.StatusOK)
	resp := parseResp(t, rec)
	if !containsStr(string(resp.Data), "linux") {
		t.Errorf("inspect response missing OS: %s", resp.Data)
	}
}

func TestInspectImage_Error(t *testing.T) {
	fake := defaultFake()
	fake.images = &fakeImageClerk{
		inspectFn: func(_ context.Context, _ string) <-chan dockerimageactor.InspectResult {
			ch := make(chan dockerimageactor.InspectResult, 1)
			ch <- dockerimageactor.InspectResult{Err: errors.New("not found")}
			return ch
		},
	}

	r := newReq("GET", "/api/images/bad/inspect", nil)
	r.SetPathValue("id", "bad")
	rec := httptest.NewRecorder()
	newTestHandler(fake).inspectImage(rec, r)
	assertStatus(t, rec, http.StatusInternalServerError)
}

// ── helpers ───────────────────────────────────────────────────────────────────

func containsStr(haystack, needle string) bool {
	return len(haystack) > 0 && len(needle) > 0 &&
		(func() bool {
			for i := 0; i <= len(haystack)-len(needle); i++ {
				if haystack[i:i+len(needle)] == needle {
					return true
				}
			}
			return false
		})()
}
