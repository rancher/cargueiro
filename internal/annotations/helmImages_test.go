package annotations

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHelmImages(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		want    []string
		wantErr string
	}{
		{
			name:  "the images in the order of the list",
			value: "- name: a\n  image: rancher/a:1\n- name: b\n  image: rancher/b:2\n",
			want:  []string{"rancher/a:1", "rancher/b:2"},
		},
		{
			name:  "an image of a subchart is one of them",
			value: "- name: a\n  image: rancher/a:1\n- name: b\n  image: rancher/b:2\n  dependency: sub\n",
			want:  []string{"rancher/a:1", "rancher/b:2"},
		},
		{
			name:  "properties that HIP-15 allows are ignored",
			value: "- name: a\n  image: rancher/a:1\n  os: linux\n  whitelisted: true\n",
			want:  []string{"rancher/a:1"},
		},
		{
			name:  "an image can repeat, the entries are kept",
			value: "- name: a\n  image: rancher/a:1\n- name: b\n  image: rancher/a:1\n",
			want:  []string{"rancher/a:1", "rancher/a:1"},
		},
		{
			name:  "an empty list has no images",
			value: "[]",
			want:  []string{},
		},
		{
			name:    "an empty value is not a list",
			value:   "",
			wantErr: "must be a YAML list",
		},
		{
			name:    "a map is not a list",
			value:   "image: rancher/a:1",
			wantErr: "must be a YAML list",
		},
		{
			name:    "an entry without an image",
			value:   "- name: a\n",
			wantErr: "helm.sh/images entry 1: has no image",
		},
		{
			name:    "an entry with an empty image, after one that has it",
			value:   "- name: a\n  image: rancher/a:1\n- name: b\n  image: \"\"\n",
			wantErr: "helm.sh/images entry 2: has no image",
		},
		{
			name:    "an entry that is empty",
			value:   "- name: a\n  image: rancher/a:1\n-\n",
			wantErr: "helm.sh/images entry 2: has no image",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := HelmImages(tt.value)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				require.Nil(t, got)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}
