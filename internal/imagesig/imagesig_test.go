package imagesig

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/rancher/cargueiro/internal/oci"
)

func TestParse(t *testing.T) {
	for _, tc := range []struct {
		in      string
		want    Image
		wantErr bool
	}{
		{in: "rancher/fleet:v0.17.0", want: Image{Repository: "rancher/fleet", Tag: "v0.17.0"}},
		{in: "rancher/rancher-webhook:v0.12.1", want: Image{Repository: "rancher/rancher-webhook", Tag: "v0.12.1"}},
		{in: "fleet", wantErr: true},
		{in: "rancher/fleet", wantErr: true},
		{in: "rancher/fleet:", wantErr: true},
		{in: ":v1", wantErr: true},
		{in: "rancher/fleet@sha256:abc", wantErr: true},
		{in: "host:5000/fleet", wantErr: true},
	} {
		got, err := Parse(tc.in)
		if (err != nil) != tc.wantErr {
			t.Errorf("Parse(%q) error = %v, want error %v", tc.in, err, tc.wantErr)
			continue
		}
		if !tc.wantErr && got != tc.want {
			t.Errorf("Parse(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// fakeBackend keeps what each registry has for an image: its digest and its signatures.
type fakeBackend struct {
	digest map[Side]map[string]string
	sigs   map[Side]map[string]int
	// copyAdds is how many signatures a copy puts in the destination.
	copyAdds int
	copied   []string
	copyErr  error
}

func newFake() *fakeBackend {
	return &fakeBackend{
		digest: map[Side]map[string]string{Source: {}, Destination: {}},
		sigs:   map[Side]map[string]int{Source: {}, Destination: {}},
	}
}

func (f *fakeBackend) put(image string, srcDigest, dstDigest string, srcSigs, dstSigs int) {
	if srcDigest != "" {
		f.digest[Source][image] = srcDigest
	}
	if dstDigest != "" {
		f.digest[Destination][image] = dstDigest
	}
	f.sigs[Source][image] = srcSigs
	f.sigs[Destination][image] = dstSigs
}

func (f *fakeBackend) Digest(_ context.Context, s Side, i Image) (string, error) {
	d, ok := f.digest[s][i.String()]
	if !ok {
		return "", fmt.Errorf("read %s: %w", i, oci.ErrNotFound)
	}
	return d, nil
}

func (f *fakeBackend) Signatures(_ context.Context, s Side, i Image, _ string) (int, error) {
	return f.sigs[s][i.String()], nil
}

func (f *fakeBackend) CopySignatures(_ context.Context, i Image) error {
	if f.copyErr != nil {
		return f.copyErr
	}
	f.copied = append(f.copied, i.String())
	f.sigs[Destination][i.String()] += f.copyAdds
	return nil
}

func run(t *testing.T, f *fakeBackend, dry bool, images ...string) (string, error) {
	t.Helper()
	var list []Image
	for _, s := range images {
		i, err := Parse(s)
		if err != nil {
			t.Fatal(err)
		}
		list = append(list, i)
	}
	var out bytes.Buffer
	err := Run(context.Background(), list, f, dry, &out)
	return out.String(), err
}

func TestRunCopiesTheSignatures(t *testing.T) {
	f := newFake()
	f.copyAdds = 2
	f.put("rancher/fleet:v1", "sha256:a", "sha256:a", 2, 0)
	out, err := run(t, f, false, "rancher/fleet:v1")
	if err != nil {
		t.Fatal(err)
	}
	if len(f.copied) != 1 || !strings.Contains(out, "signatures copied rancher/fleet:v1 (source 2, destination before 0, after 2)") {
		t.Fatalf("copied=%v out=%q", f.copied, out)
	}
}

func TestRunDryRunCopiesNothing(t *testing.T) {
	f := newFake()
	f.put("rancher/fleet:v1", "sha256:a", "sha256:a", 2, 0)
	out, err := run(t, f, true, "rancher/fleet:v1")
	if err != nil {
		t.Fatal(err)
	}
	if len(f.copied) != 0 || !strings.Contains(out, "would copy the signatures of rancher/fleet:v1 (source 2, destination 0)") {
		t.Fatalf("copied=%v out=%q", f.copied, out)
	}
}

func TestRunSourceWithoutSignaturesIsNotAnError(t *testing.T) {
	f := newFake()
	f.put("rancher/x:v1", "sha256:a", "sha256:a", 0, 0)
	out, err := run(t, f, false, "rancher/x:v1")
	if err != nil || len(f.copied) != 0 || !strings.Contains(out, "the source has none") {
		t.Fatalf("err=%v copied=%v out=%q", err, f.copied, out)
	}
}

func TestRunRefusesWhatItDoesNotCopy(t *testing.T) {
	f := newFake()
	f.put("rancher/nosrc:v1", "", "sha256:a", 0, 0)
	f.put("rancher/nodst:v1", "sha256:a", "", 2, 0)
	f.put("rancher/other:v1", "sha256:a", "sha256:b", 2, 0)
	_, err := run(t, f, false, "rancher/nosrc:v1", "rancher/nodst:v1", "rancher/other:v1")
	if err == nil {
		t.Fatal("want an error")
	}
	for _, want := range []string{
		"3 of 3 images",
		"rancher/nosrc:v1: the image is not in the source registry",
		"rancher/nodst:v1: the image is not in the destination registry",
		"rancher/other:v1: the tag names sha256:a in the source registry and sha256:b in the destination one",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	if len(f.copied) != 0 {
		t.Fatalf("copied %v, want nothing", f.copied)
	}
}

func TestRunDoesTheOthersWhenOneFails(t *testing.T) {
	f := newFake()
	f.copyAdds = 1
	f.put("rancher/bad:v1", "sha256:a", "", 1, 0)
	f.put("rancher/good:v1", "sha256:b", "sha256:b", 1, 0)
	_, err := run(t, f, false, "rancher/bad:v1", "rancher/good:v1")
	if err == nil || !strings.Contains(err.Error(), "1 of 2 images") {
		t.Fatalf("err = %v", err)
	}
	if len(f.copied) != 1 || f.copied[0] != "rancher/good:v1" {
		t.Fatalf("copied = %v", f.copied)
	}
}

func TestRunFailsWhenNothingArrives(t *testing.T) {
	f := newFake()
	f.copyAdds = 0
	f.put("rancher/fleet:v1", "sha256:a", "sha256:a", 2, 0)
	_, err := run(t, f, false, "rancher/fleet:v1")
	if err == nil || !strings.Contains(err.Error(), "after the copy, as before it") {
		t.Fatalf("err = %v", err)
	}
}

func TestRunAlreadyCopiedIsFine(t *testing.T) {
	f := newFake()
	f.copyAdds = 0
	f.put("rancher/fleet:v1", "sha256:a", "sha256:a", 2, 2)
	out, err := run(t, f, false, "rancher/fleet:v1")
	if err != nil || !strings.Contains(out, "after 2") {
		t.Fatalf("err=%v out=%q", err, out)
	}
}

func TestRunReportsACopyError(t *testing.T) {
	f := newFake()
	f.copyErr = errors.New("registry said no")
	f.put("rancher/fleet:v1", "sha256:a", "sha256:a", 2, 0)
	_, err := run(t, f, false, "rancher/fleet:v1")
	if err == nil || !strings.Contains(err.Error(), "copy the signatures: registry said no") {
		t.Fatalf("err = %v", err)
	}
}
