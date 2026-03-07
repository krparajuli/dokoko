package dockerimageclerk_test

import (
	"context"
	"errors"
	"testing"

	dockerimageclerk "dokoko.ai/dokoko/internal/docker/images/clerk"
	dockerimageactor "dokoko.ai/dokoko/internal/docker/images/actor"
	dockerimagestate "dokoko.ai/dokoko/internal/docker/images/state"
	"dokoko.ai/dokoko/pkg/logger"
	dockerimage "github.com/docker/docker/api/types/image"
)

// ── fakeActor ────────────────────────────────────────────────────────────────

type fakeActor struct {
	pullFn    func(ctx context.Context, ref string, opts dockerimage.PullOptions) (*dockerimageactor.Ticket, error)
	removeFn  func(ctx context.Context, imageID string, opts dockerimage.RemoveOptions) (*dockerimageactor.Ticket, error)
	tagFn     func(ctx context.Context, source, target string) (*dockerimageactor.Ticket, error)
	listFn    func(ctx context.Context, opts dockerimage.ListOptions) <-chan dockerimageactor.ListResult
	inspectFn func(ctx context.Context, imageID string) <-chan dockerimageactor.InspectResult
	existsFn  func(ctx context.Context, ref string) <-chan dockerimageactor.ExistsResult
}

func closedTicket(changeID string) *dockerimageactor.Ticket {
	done := make(chan struct{})
	close(done)
	return &dockerimageactor.Ticket{ChangeID: changeID, Done: done}
}

func (f *fakeActor) Pull(ctx context.Context, ref string, opts dockerimage.PullOptions) (*dockerimageactor.Ticket, error) {
	if f.pullFn != nil {
		return f.pullFn(ctx, ref, opts)
	}
	return closedTicket("chg-pull"), nil
}

func (f *fakeActor) Remove(ctx context.Context, imageID string, opts dockerimage.RemoveOptions) (*dockerimageactor.Ticket, error) {
	if f.removeFn != nil {
		return f.removeFn(ctx, imageID, opts)
	}
	return closedTicket("chg-remove"), nil
}

func (f *fakeActor) Tag(ctx context.Context, source, target string) (*dockerimageactor.Ticket, error) {
	if f.tagFn != nil {
		return f.tagFn(ctx, source, target)
	}
	return closedTicket("chg-tag"), nil
}

func (f *fakeActor) List(ctx context.Context, opts dockerimage.ListOptions) <-chan dockerimageactor.ListResult {
	if f.listFn != nil {
		return f.listFn(ctx, opts)
	}
	ch := make(chan dockerimageactor.ListResult, 1)
	ch <- dockerimageactor.ListResult{}
	return ch
}

func (f *fakeActor) Inspect(ctx context.Context, imageID string) <-chan dockerimageactor.InspectResult {
	if f.inspectFn != nil {
		return f.inspectFn(ctx, imageID)
	}
	ch := make(chan dockerimageactor.InspectResult, 1)
	ch <- dockerimageactor.InspectResult{}
	return ch
}

func (f *fakeActor) Exists(ctx context.Context, ref string) <-chan dockerimageactor.ExistsResult {
	if f.existsFn != nil {
		return f.existsFn(ctx, ref)
	}
	ch := make(chan dockerimageactor.ExistsResult, 1)
	ch <- dockerimageactor.ExistsResult{Present: true}
	return ch
}

// ── helpers ───────────────────────────────────────────────────────────────────

func silentLogger() *logger.Logger { return logger.New(logger.LevelSilent) }

func newTestClerk(act *fakeActor) (*dockerimageclerk.Clerk, *dockerimagestate.State, *dockerimagestate.Store) {
	st := dockerimagestate.New(silentLogger())
	store := dockerimagestate.NewStore(silentLogger())
	cl := dockerimageclerk.New(act, st, store, silentLogger())
	return cl, st, store
}

// ── Refresh ───────────────────────────────────────────────────────────────────

func TestRefresh_PopulatesStore(t *testing.T) {
	act := &fakeActor{
		listFn: func(_ context.Context, _ dockerimage.ListOptions) <-chan dockerimageactor.ListResult {
			ch := make(chan dockerimageactor.ListResult, 1)
			ch <- dockerimageactor.ListResult{Images: []dockerimage.Summary{
				{ID: "sha256:aabbcc112233", RepoTags: []string{"ubuntu:22.04"}, Size: 1024},
				{ID: "sha256:ddeeff445566", RepoTags: []string{"alpine:latest"}, Size: 512},
			}}
			return ch
		},
	}
	cl, _, store := newTestClerk(act)

	if err := cl.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	if store.Size() != 2 {
		t.Errorf("store size: got %d, want 2", store.Size())
	}

	rec, ok := store.Get("sha256:aabbcc112233")
	if !ok {
		t.Fatal("expected sha256:aabbcc112233 in store")
	}
	if rec.Status != dockerimagestate.ImageStatusPresent {
		t.Errorf("status: got %q, want present", rec.Status)
	}
	if len(rec.RepoTags) == 0 || rec.RepoTags[0] != "ubuntu:22.04" {
		t.Errorf("RepoTags: got %v", rec.RepoTags)
	}
}

func TestRefresh_ReconcilesMissingImages(t *testing.T) {
	// First refresh: two images.
	firstList := []dockerimage.Summary{
		{ID: "sha256:aaaa", RepoTags: []string{"img-a:latest"}},
		{ID: "sha256:bbbb", RepoTags: []string{"img-b:latest"}},
	}
	// Second refresh: only one image (img-a vanished out-of-band).
	secondList := []dockerimage.Summary{
		{ID: "sha256:bbbb", RepoTags: []string{"img-b:latest"}},
	}

	call := 0
	act := &fakeActor{
		listFn: func(_ context.Context, _ dockerimage.ListOptions) <-chan dockerimageactor.ListResult {
			ch := make(chan dockerimageactor.ListResult, 1)
			if call == 0 {
				ch <- dockerimageactor.ListResult{Images: firstList}
			} else {
				ch <- dockerimageactor.ListResult{Images: secondList}
			}
			call++
			return ch
		},
	}
	cl, _, store := newTestClerk(act)

	if err := cl.Refresh(context.Background()); err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	if err := cl.Refresh(context.Background()); err != nil {
		t.Fatalf("second refresh: %v", err)
	}

	rec, ok := store.Get("sha256:aaaa")
	if !ok {
		t.Fatal("expected sha256:aaaa in store after second refresh")
	}
	if rec.Status != dockerimagestate.ImageStatusDeletedOutOfBand {
		t.Errorf("img-a status: got %q, want deleted_out_of_band", rec.Status)
	}
}

func TestRefresh_PropagatesListError(t *testing.T) {
	act := &fakeActor{
		listFn: func(_ context.Context, _ dockerimage.ListOptions) <-chan dockerimageactor.ListResult {
			ch := make(chan dockerimageactor.ListResult, 1)
			ch <- dockerimageactor.ListResult{Err: errors.New("daemon unavailable")}
			return ch
		},
	}
	cl, _, _ := newTestClerk(act)

	if err := cl.Refresh(context.Background()); err == nil {
		t.Error("expected error from Refresh on list failure")
	}
}

// ── Mutation delegation ───────────────────────────────────────────────────────

func TestPull_DelegatesToActor(t *testing.T) {
	var calledWith string
	act := &fakeActor{
		pullFn: func(_ context.Context, ref string, _ dockerimage.PullOptions) (*dockerimageactor.Ticket, error) {
			calledWith = ref
			return closedTicket("chg-1"), nil
		},
	}
	cl, _, _ := newTestClerk(act)

	ticket, err := cl.Pull(context.Background(), "busybox:latest", dockerimage.PullOptions{})
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if ticket == nil {
		t.Fatal("expected non-nil ticket")
	}
	if calledWith != "busybox:latest" {
		t.Errorf("Pull called with %q, want %q", calledWith, "busybox:latest")
	}
}

func TestPull_PropagatesError(t *testing.T) {
	act := &fakeActor{
		pullFn: func(_ context.Context, _ string, _ dockerimage.PullOptions) (*dockerimageactor.Ticket, error) {
			return nil, errors.New("queue full")
		},
	}
	cl, _, _ := newTestClerk(act)

	_, err := cl.Pull(context.Background(), "busybox:latest", dockerimage.PullOptions{})
	if err == nil {
		t.Error("expected error propagated from actor")
	}
}

func TestRemove_DelegatesToActor(t *testing.T) {
	var calledWith string
	act := &fakeActor{
		removeFn: func(_ context.Context, imageID string, _ dockerimage.RemoveOptions) (*dockerimageactor.Ticket, error) {
			calledWith = imageID
			return closedTicket("chg-rm"), nil
		},
	}
	cl, _, _ := newTestClerk(act)

	_, err := cl.Remove(context.Background(), "sha256:aabbcc", dockerimage.RemoveOptions{})
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if calledWith != "sha256:aabbcc" {
		t.Errorf("Remove called with %q, want sha256:aabbcc", calledWith)
	}
}

func TestTag_DelegatesToActor(t *testing.T) {
	var src, dst string
	act := &fakeActor{
		tagFn: func(_ context.Context, source, target string) (*dockerimageactor.Ticket, error) {
			src, dst = source, target
			return closedTicket("chg-tag"), nil
		},
	}
	cl, _, _ := newTestClerk(act)

	_, err := cl.Tag(context.Background(), "ubuntu:22.04", "ubuntu:prod")
	if err != nil {
		t.Fatalf("Tag: %v", err)
	}
	if src != "ubuntu:22.04" || dst != "ubuntu:prod" {
		t.Errorf("Tag args: got (%q, %q)", src, dst)
	}
}

// ── Accessors ─────────────────────────────────────────────────────────────────

func TestState_ReturnsSameInstance(t *testing.T) {
	cl, st, _ := newTestClerk(&fakeActor{})
	if cl.State() != st {
		t.Error("State() returned wrong instance")
	}
}

func TestStore_ReturnsSameInstance(t *testing.T) {
	cl, _, store := newTestClerk(&fakeActor{})
	if cl.Store() != store {
		t.Error("Store() returned wrong instance")
	}
}
