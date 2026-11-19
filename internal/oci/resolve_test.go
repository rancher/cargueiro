package oci

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"
)

func TestResolveDigest(t *testing.T) {
	f := newFakeRegistry()
	f.addChart(t, validArchive(t))
	manifest := f.responses["/v2/"+testRepository+"/manifests/"+testTag]

	got, err := ResolveDigest(context.Background(), serve(t, f), FetchOptions{PlainHTTP: true})

	require.NoError(t, err)
	require.Equal(t, digest.FromBytes(manifest.body).String(), got)
}

func TestResolveDigestFails(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(t *testing.T, f *fakeRegistry)
		wantErr string
	}{
		{
			name:    "the tag does not exist",
			setup:   func(*testing.T, *fakeRegistry) {},
			wantErr: "was not found in the registry",
		},
		{
			name: "the registry grants no access without credentials",
			setup: func(_ *testing.T, f *fakeRegistry) {
				f.status["/v2/"+testRepository+"/manifests/"+testTag] = http.StatusUnauthorized
			},
			wantErr: "granted no access",
		},
		{
			name: "the registry forbids the read",
			setup: func(_ *testing.T, f *fakeRegistry) {
				f.status["/v2/"+testRepository+"/manifests/"+testTag] = http.StatusForbidden
			},
			wantErr: "granted no access",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeRegistry()
			tt.setup(t, f)

			got, err := ResolveDigest(context.Background(), serve(t, f), FetchOptions{PlainHTTP: true})

			require.ErrorContains(t, err, tt.wantErr)
			require.Empty(t, got)
		})
	}
}

func TestResolveDigestCannotReachTheRegistry(t *testing.T) {
	f := newFakeRegistry()
	f.addChart(t, validArchive(t))
	srv := newServer(f)
	ref := ChartRef{Host: srv.Listener.Addr().String(), Repository: testRepository, Tag: testTag}
	srv.Close()

	got, err := ResolveDigest(context.Background(), ref, FetchOptions{PlainHTTP: true})

	require.ErrorContains(t, err, "fetch "+ref.String())
	require.Empty(t, got)
}

// busyRegistry answers 503 to the first busy reads of a manifest, and then
// passes the requests on to registry. It counts the reads of manifests.
type busyRegistry struct {
	registry http.Handler
	busy     int32
	reads    atomic.Int32
}

func (b *busyRegistry) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodHead || r.Method == http.MethodGet {
		if b.reads.Add(1) <= b.busy {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
	}
	b.registry.ServeHTTP(w, r)
}

func TestResolveDigestWithRetry(t *testing.T) {
	tests := []struct {
		name      string
		busy      int32
		attempts  int
		prepare   func(t *testing.T, f *fakeRegistry)
		wantReads int32
		wantErr   string
		wantWaits []time.Duration
	}{
		{
			name:      "the registry answers at once",
			attempts:  3,
			prepare:   func(t *testing.T, f *fakeRegistry) { f.addChart(t, validArchive(t)) },
			wantReads: 1,
		},
		{
			name:      "the registry is busy twice and then answers",
			busy:      2,
			attempts:  3,
			prepare:   func(t *testing.T, f *fakeRegistry) { f.addChart(t, validArchive(t)) },
			wantReads: 3,
			wantWaits: []time.Duration{time.Second, 2 * time.Second},
		},
		{
			name:      "the registry stays busy, and the attempts end",
			busy:      10,
			attempts:  3,
			prepare:   func(t *testing.T, f *fakeRegistry) { f.addChart(t, validArchive(t)) },
			wantReads: 3,
			wantErr:   "fetch ",
			wantWaits: []time.Duration{time.Second, 2 * time.Second},
		},
		{
			name:      "a tag that is not there is not tried again",
			attempts:  3,
			prepare:   func(*testing.T, *fakeRegistry) {},
			wantReads: 1,
			wantErr:   "was not found in the registry",
		},
		{
			name:     "a refusal is not tried again",
			attempts: 3,
			prepare: func(_ *testing.T, f *fakeRegistry) {
				f.status["/v2/"+testRepository+"/manifests/"+testTag] = http.StatusForbidden
			},
			wantReads: 1,
			wantErr:   "granted no access",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeRegistry()
			tt.prepare(t, f)
			busy := &busyRegistry{registry: f, busy: tt.busy}
			srv := httptest.NewServer(busy)
			t.Cleanup(srv.Close)
			ref := ChartRef{Host: srv.Listener.Addr().String(), Repository: testRepository, Tag: testTag}

			var waits []time.Duration
			retry := Retry{
				Attempts: tt.attempts,
				Wait:     time.Second,
				Sleep:    func(context.Context, time.Duration) error { return nil },
				OnRetry:  func(_ int, _ error, wait time.Duration) { waits = append(waits, wait) },
			}

			got, err := ResolveDigestWithRetry(context.Background(), ref, FetchOptions{PlainHTTP: true}, retry)

			require.Equal(t, tt.wantReads, busy.reads.Load())
			require.Equal(t, tt.wantWaits, waits)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				require.Empty(t, got)
				return
			}
			require.NoError(t, err)
			require.NotEmpty(t, got)
		})
	}
}
