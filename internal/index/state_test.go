package index

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// fleet returns the given versions of the fleet chart.
func fleet(versions ...string) []Ref {
	refs := make([]Ref, 0, len(versions))
	for _, v := range versions {
		refs = append(refs, Ref{Chart: "fleet", Version: v})
	}
	return refs
}

func TestPlan(t *testing.T) {
	tests := []struct {
		name  string
		state State
		want  Plan
	}{
		{
			name:  "nothing at all",
			state: State{},
			want:  Plan{},
		},
		{
			name: "every version is already published",
			state: State{
				Entries:   fleet("1", "2"),
				Published: fleet("1", "2"),
				Archives:  fleet("1", "2"),
			},
			want: Plan{},
		},
		{
			name: "an entry that is not published yet",
			state: State{
				Entries:   fleet("1", "2"),
				Published: fleet("1"),
				Archives:  fleet("1"),
			},
			want: Plan{Add: fleet("2")},
		},
		{
			name: "everything is new on the first run",
			state: State{
				Entries: fleet("2", "1"),
			},
			want: Plan{Add: fleet("1", "2")},
		},
		{
			name: "new versions come sorted by chart and then by version",
			state: State{
				Entries: []Ref{
					{Chart: "fleet", Version: "2"},
					{Chart: "alerting", Version: "9"},
					{Chart: "fleet", Version: "1"},
				},
			},
			want: Plan{Add: []Ref{
				{Chart: "alerting", Version: "9"},
				{Chart: "fleet", Version: "1"},
				{Chart: "fleet", Version: "2"},
			}},
		},
		{
			name: "the same version listed twice is one version",
			state: State{
				Entries: fleet("1", "1"),
			},
			want: Plan{Add: fleet("1")},
		},
		{
			name: "a published version that is withdrawn is taken out of the index",
			state: State{
				Entries:   fleet("1", "2"),
				Withdrawn: fleet("2"),
				Published: fleet("1", "2"),
				Archives:  fleet("1", "2"),
			},
			want: Plan{Withdraw: fleet("2")},
		},
		{
			name: "a version that is withdrawn already is left alone",
			state: State{
				Entries:   fleet("1", "2"),
				Withdrawn: fleet("2"),
				Published: fleet("1"),
				Archives:  fleet("1", "2"),
			},
			want: Plan{},
		},
		{
			name: "a legacy version that is not published yet is added, with no archive",
			state: State{
				Entries: fleet("1"),
				Legacy:  fleet("0.9", "0.8"),
			},
			want: Plan{Add: fleet("1"), AddLegacy: fleet("0.8", "0.9")},
		},
		{
			name: "a legacy version that is published needs no entry in entries/ and no archive",
			state: State{
				Entries:   fleet("1"),
				Legacy:    fleet("0.9"),
				Published: fleet("0.9", "1"),
				Archives:  fleet("1"),
			},
			want: Plan{},
		},
		{
			name: "versions to add and to withdraw at once",
			state: State{
				Entries:   fleet("1", "2", "3"),
				Withdrawn: fleet("1"),
				Published: fleet("1", "2"),
				Archives:  fleet("1", "2"),
			},
			want: Plan{
				Add:      fleet("3"),
				Withdraw: fleet("1"),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.state.Plan()

			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestPlanFails(t *testing.T) {
	tests := []struct {
		name    string
		state   State
		wantErr []string
	}{
		{
			name: "a published version lost its entry",
			state: State{
				Entries:   fleet("1"),
				Published: fleet("1", "2"),
				Archives:  fleet("1", "2"),
			},
			wantErr: []string{"fleet 2 is in index.yaml but has no entry: entries are never removed"},
		},
		{
			name: "an archive with no entry",
			state: State{
				Entries:  fleet("1"),
				Archives: fleet("1", "2"),
			},
			wantErr: []string{
				"fleet 1 is not in index.yaml yet but already has an archive in assets/",
				"assets/ has an archive of fleet 2 but there is no entry for it",
			},
		},
		{
			name: "a published version lost its archive",
			state: State{
				Entries:   fleet("1"),
				Published: fleet("1"),
			},
			wantErr: []string{"fleet 1 is in index.yaml but has no archive in assets/"},
		},
		{
			name: "a new entry that already has an archive, which is also a withdrawn version that is no longer withdrawn",
			state: State{
				Entries:  fleet("1"),
				Archives: fleet("1"),
			},
			wantErr: []string{"fleet 1 is not in index.yaml yet but already has an archive in assets/"},
		},
		{
			name: "a withdrawn version that was never published",
			state: State{
				Entries:   fleet("1"),
				Withdrawn: fleet("1"),
			},
			wantErr: []string{"fleet 1 is withdrawn but was never published: it is not in index.yaml and has no archive in assets/"},
		},
		{
			name: "a withdrawn version that lost its archive",
			state: State{
				Entries:   fleet("1"),
				Withdrawn: fleet("1"),
				Published: fleet("1"),
			},
			wantErr: []string{"fleet 1 is in index.yaml but has no archive in assets/"},
		},
		{
			name: "a version in entries/ and in legacy/",
			state: State{
				Entries: fleet("1"),
				Legacy:  fleet("1"),
			},
			wantErr: []string{"fleet 1 is in entries/ and in legacy/"},
		},
		{
			name: "a legacy version that has an archive",
			state: State{
				Legacy:    fleet("0.9"),
				Published: fleet("0.9"),
				Archives:  fleet("0.9"),
			},
			wantErr: []string{"assets/ has an archive of fleet 0.9, which is a legacy version: it has none"},
		},
		{
			name: "a published version with no entry in either folder",
			state: State{
				Entries:   fleet("1"),
				Published: fleet("0.9", "1"),
				Archives:  fleet("1"),
			},
			wantErr: []string{"fleet 0.9 is in index.yaml but has no entry: entries are never removed"},
		},
		{
			name: "every version at fault is named",
			state: State{
				Entries:   fleet("1", "3"),
				Published: fleet("1", "2", "3"),
				Archives:  fleet("1", "2"),
			},
			wantErr: []string{
				"fleet 2 is in index.yaml but has no entry: entries are never removed",
				"fleet 3 is in index.yaml but has no archive in assets/",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.state.Plan()

			require.Error(t, err)
			require.Equal(t, Plan{}, got)
			for _, msg := range tt.wantErr {
				require.ErrorContains(t, err, msg)
			}
		})
	}
}
