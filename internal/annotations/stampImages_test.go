package annotations

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/rancher/cargueiro/internal/chart"
)

func TestGenerateHelmImages(t *testing.T) {
	tests := []struct {
		name        string
		annotations map[string]string
		values      map[string]any
		want        string
		wantChanged bool
	}{
		{
			name:        "one image",
			annotations: map[string]string{},
			values: map[string]any{
				"image": map[string]any{"repository": "rancher/rancher-webhook", "tag": "v0.11.4"},
			},
			want:        "- name: rancher-webhook\n  image: rancher/rancher-webhook:v0.11.4\n",
			wantChanged: true,
		},
		{
			name:        "nested images, in the order of the keys, each named after its repository",
			annotations: map[string]string{},
			values: map[string]any{
				"image": map[string]any{
					"operator":     map[string]any{"repository": "rancher/compliance-operator", "tag": "v1.5.4"},
					"securityScan": map[string]any{"repository": "rancher/security-scan", "tag": "v0.10.4"},
					"sonobuoy":     map[string]any{"repository": "rancher/mirrored-sonobuoy-sonobuoy", "tag": "v0.57.5"},
				},
				"kubectl": map[string]any{"repository": "rancher/kuberlr-kubectl", "tag": "v8.1.2"},
			},
			want: "- name: compliance-operator\n  image: rancher/compliance-operator:v1.5.4\n" +
				"- name: security-scan\n  image: rancher/security-scan:v0.10.4\n" +
				"- name: mirrored-sonobuoy-sonobuoy\n  image: rancher/mirrored-sonobuoy-sonobuoy:v0.57.5\n" +
				"- name: kuberlr-kubectl\n  image: rancher/kuberlr-kubectl:v8.1.2\n",
			wantChanged: true,
		},
		{
			name:        "a repository with a registry and a port",
			annotations: map[string]string{},
			values: map[string]any{
				"image": map[string]any{"repository": "localhost:5000/rancher/a", "tag": "v1"},
			},
			want:        "- name: a\n  image: localhost:5000/rancher/a:v1\n",
			wantChanged: true,
		},
		{
			name:        "a repository with no path",
			annotations: map[string]string{},
			values: map[string]any{
				"image": map[string]any{"repository": "busybox", "tag": "1.36"},
			},
			want:        "- name: busybox\n  image: busybox:1.36\n",
			wantChanged: true,
		},
		{
			name:        "the same image in two places is listed once",
			annotations: map[string]string{},
			values: map[string]any{
				"a": map[string]any{"repository": "rancher/x", "tag": "v1"},
				"b": map[string]any{"repository": "rancher/x", "tag": "v1"},
			},
			want:        "- name: x\n  image: rancher/x:v1\n",
			wantChanged: true,
		},
		{
			name: "an annotation that exists is kept and extended",
			annotations: map[string]string{
				"helm.sh/images": "- name: fleet\n  image: rancher/fleet:v0.16.3\n",
			},
			values: map[string]any{
				"fleet": map[string]any{"repository": "rancher/fleet", "tag": "v0.16.3"},
				"agent": map[string]any{"repository": "rancher/fleet-agent", "tag": "v0.16.3"},
			},
			want: "- name: fleet\n  image: rancher/fleet:v0.16.3\n" +
				"- name: fleet-agent\n  image: rancher/fleet-agent:v0.16.3\n",
			wantChanged: true,
		},
		{
			name: "an image the annotation lists under another name is not added again",
			annotations: map[string]string{
				"helm.sh/images": "- name: controller\n  image: rancher/fleet:v0.16.3\n",
			},
			values: map[string]any{
				"fleet": map[string]any{"repository": "rancher/fleet", "tag": "v0.16.3"},
			},
			want:        "- name: controller\n  image: rancher/fleet:v0.16.3\n",
			wantChanged: false,
		},
		{
			name: "an entry that comes from a template stays",
			annotations: map[string]string{
				"helm.sh/images": "- name: hook\n  image: rancher/hook:v1\n",
			},
			values: map[string]any{
				"fleet": map[string]any{"repository": "rancher/fleet", "tag": "v1"},
			},
			want: "- name: hook\n  image: rancher/hook:v1\n" +
				"- name: fleet\n  image: rancher/fleet:v1\n",
			wantChanged: true,
		},
		{
			name: "an annotation that does not end with a newline",
			annotations: map[string]string{
				"helm.sh/images": "- name: a\n  image: rancher/a:v1",
			},
			values: map[string]any{
				"b": map[string]any{"repository": "rancher/b", "tag": "v1"},
			},
			want:        "- name: a\n  image: rancher/a:v1\n- name: b\n  image: rancher/b:v1\n",
			wantChanged: true,
		},
		{
			name:        "no images in values.yaml",
			annotations: map[string]string{},
			values:      map[string]any{"replicas": 1},
			want:        "",
			wantChanged: false,
		},
		{
			name:        "no values.yaml content",
			annotations: nil,
			values:      nil,
			want:        "",
			wantChanged: false,
		},
		{
			name:        "a block without an image configured is skipped",
			annotations: map[string]string{},
			values: map[string]any{
				"image": map[string]any{"repository": "", "tag": ""},
			},
			want:        "",
			wantChanged: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, changed, problems := GenerateHelmImages(tt.annotations, tt.values, nil)

			require.Empty(t, problems)
			require.Equal(t, tt.wantChanged, changed)
			require.Equal(t, tt.want, got)
			if tt.want != "" {
				require.Empty(t, checkDependenciesAnnotationStandard(map[string]string{"helm.sh/images": got}),
					"what is generated passes the check")
			}
		})
	}
}

func TestGenerateHelmImagesWithDependencies(t *testing.T) {
	tests := []struct {
		name         string
		annotations  map[string]string
		values       map[string]any
		dependencies map[string]*chart.Files
		want         string
		wantChanged  bool
	}{
		{
			name:        "an image in a subchart gets a dependency entry",
			annotations: map[string]string{},
			values:      map[string]any{},
			dependencies: map[string]*chart.Files{
				"sub": {Values: map[string]any{"image": map[string]any{"repository": "rancher/sub", "tag": "v1"}}},
			},
			want:        "- name: sub\n  image: rancher/sub:v1\n  dependency: sub\n",
			wantChanged: true,
		},
		{
			name:        "the chart's own images come before the subchart's",
			annotations: map[string]string{},
			values: map[string]any{
				"image": map[string]any{"repository": "rancher/top", "tag": "v1"},
			},
			dependencies: map[string]*chart.Files{
				"sub": {Values: map[string]any{"image": map[string]any{"repository": "rancher/sub", "tag": "v1"}}},
			},
			want: "- name: top\n  image: rancher/top:v1\n" +
				"- name: sub\n  image: rancher/sub:v1\n  dependency: sub\n",
			wantChanged: true,
		},
		{
			name:        "an image a nested subchart already lists is not added again",
			annotations: map[string]string{},
			values:      map[string]any{},
			dependencies: map[string]*chart.Files{
				"sub": {
					Dependencies: map[string]*chart.Files{
						"nested": {Values: map[string]any{"image": map[string]any{"repository": "rancher/nested", "tag": "v1"}}},
					},
				},
			},
			want:        "- name: nested\n  image: rancher/nested:v1\n  dependency: sub/nested\n",
			wantChanged: true,
		},
		{
			name: "a subchart image the annotation already lists is not added again",
			annotations: map[string]string{
				"helm.sh/images": "- name: sub\n  image: rancher/sub:v1\n  dependency: sub\n",
			},
			values: map[string]any{},
			dependencies: map[string]*chart.Files{
				"sub": {Values: map[string]any{"image": map[string]any{"repository": "rancher/sub", "tag": "v1"}}},
			},
			want:        "- name: sub\n  image: rancher/sub:v1\n  dependency: sub\n",
			wantChanged: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, changed, problems := GenerateHelmImages(tt.annotations, tt.values, tt.dependencies)

			require.Empty(t, problems)
			require.Equal(t, tt.wantChanged, changed)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestGenerateHelmImagesRefuses(t *testing.T) {
	tests := []struct {
		name         string
		annotations  map[string]string
		values       map[string]any
		dependencies map[string]*chart.Files
		want         []string
	}{
		{
			name:        "the tag latest",
			annotations: map[string]string{},
			values: map[string]any{
				"image": map[string]any{"repository": "rancher/rancher-webhook", "tag": "latest"},
			},
			want: []string{`values.yaml image: "tag" must not be "latest"`},
		},
		{
			name:        "an image with no tag",
			annotations: map[string]string{},
			values: map[string]any{
				"image": map[string]any{"repository": "rancher/a"},
			},
			want: []string{`values.yaml image: "repository" is set but "tag" is empty`},
		},
		{
			name:        "a tag that is not a text",
			annotations: map[string]string{},
			values: map[string]any{
				"image": map[string]any{"repository": "rancher/a", "tag": 1.5},
			},
			want: []string{`values.yaml image: "repository" and "tag" must be strings`},
		},
		{
			name:        "two images with the same name",
			annotations: map[string]string{},
			values: map[string]any{
				"a": map[string]any{"repository": "rancher/x", "tag": "v1"},
				"b": map[string]any{"repository": "other/x", "tag": "v2"},
			},
			want: []string{`values.yaml b: image "other/x:v2" would be named "x", which "rancher/x:v1" has already`},
		},
		{
			name: "a name the annotation has for another image",
			annotations: map[string]string{
				"helm.sh/images": "- name: fleet\n  image: rancher/old-fleet:v1\n",
			},
			values: map[string]any{
				"image": map[string]any{"repository": "rancher/fleet", "tag": "v2"},
			},
			want: []string{`values.yaml image: image "rancher/fleet:v2" would be named "fleet", which "rancher/old-fleet:v1" has already`},
		},
		{
			name: "an annotation that is not a list",
			annotations: map[string]string{
				"helm.sh/images": "fleet",
			},
			values: map[string]any{
				"image": map[string]any{"repository": "rancher/a", "tag": "v1"},
			},
			want: []string{`helm.sh/images: must be a YAML list of entries with a name and an image`},
		},
		{
			name: "an annotation with the tag latest",
			annotations: map[string]string{
				"helm.sh/images": "- name: a\n  image: rancher/a:latest\n",
			},
			values: map[string]any{
				"image": map[string]any{"repository": "rancher/b", "tag": "v1"},
			},
			want: []string{`helm.sh/images entry 1: image "rancher/a:latest" must not have the tag "latest"`},
		},
		{
			name:        "every problem is reported",
			annotations: map[string]string{},
			values: map[string]any{
				"a": map[string]any{"repository": "rancher/a", "tag": "latest"},
				"b": map[string]any{"repository": "rancher/b"},
			},
			want: []string{
				`values.yaml a: "tag" must not be "latest"`,
				`values.yaml b: "repository" is set but "tag" is empty`,
			},
		},
		{
			// Seen in the real rancher-monitoring chart: two subcharts vendor
			// different versions of an image whose repository ends the same way
			// (.../busybox), so they would both be named "busybox". GenerateHelmImages
			// cannot guess which one should keep the short name, so it refuses
			// instead of silently picking one; a chart owner resolves this by adding
			// one of the two to the annotation by hand, with a name that tells them
			// apart, before stamping.
			name:        "two different subcharts vendor different tags of an image with the same short name",
			annotations: map[string]string{},
			values:      map[string]any{},
			dependencies: map[string]*chart.Files{
				"grafana":  {Values: map[string]any{"image": map[string]any{"repository": "rancher/mirrored-library-busybox", "tag": "1.31.1"}}},
				"hardened": {Values: map[string]any{"image": map[string]any{"repository": "rancher/mirrored-library-busybox", "tag": "1.37.0"}}},
			},
			want: []string{`charts/hardened/values.yaml image: image "rancher/mirrored-library-busybox:1.37.0" would be named "mirrored-library-busybox", which "rancher/mirrored-library-busybox:1.31.1" has already`},
		},
		{
			name:        "a subchart image collides with a name the chart's own image would take",
			annotations: map[string]string{},
			values: map[string]any{
				"image": map[string]any{"repository": "rancher/other/busybox", "tag": "v1"},
			},
			dependencies: map[string]*chart.Files{
				"sub": {Values: map[string]any{"image": map[string]any{"repository": "rancher/busybox", "tag": "v2"}}},
			},
			want: []string{`charts/sub/values.yaml image: image "rancher/busybox:v2" would be named "busybox", which "rancher/other/busybox:v1" has already`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, changed, problems := GenerateHelmImages(tt.annotations, tt.values, tt.dependencies)

			require.Equal(t, tt.want, problems)
			require.Empty(t, got)
			require.False(t, changed)
		})
	}
}

func TestImageName(t *testing.T) {
	tests := []struct {
		image string
		want  string
	}{
		{image: "rancher/fleet-agent:v0.16.3", want: "fleet-agent"},
		{image: "localhost:5000/rancher/a:v1", want: "a"},
		{image: "docker.io/library/nginx:1.0", want: "nginx"},
		{image: "busybox:1.36", want: "busybox"},
		{image: "localhost:5000/a", want: "a"},
	}
	for _, tt := range tests {
		t.Run(tt.image, func(t *testing.T) {
			require.Equal(t, tt.want, imageName(tt.image))
		})
	}
}
