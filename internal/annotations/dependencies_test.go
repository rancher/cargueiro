package annotations

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/rancher/cargueiro/internal/chart"
)

func TestScanDependenciesNone(t *testing.T) {
	images, problems := scanDependencies(nil)
	require.Empty(t, images)
	require.Empty(t, problems)
}

func TestScanDependenciesFindsImagesInASubchart(t *testing.T) {
	deps := map[string]*chart.Files{
		"sub": {
			Values: map[string]any{
				"image": map[string]any{"repository": "rancher/sub", "tag": "v1"},
			},
		},
	}

	images, problems := scanDependencies(deps)

	require.Empty(t, problems)
	require.Equal(t, []valuesImage{
		{Path: "image", Image: "rancher/sub:v1", Dependency: "sub"},
	}, images)
}

func TestScanDependenciesOrdersByName(t *testing.T) {
	deps := map[string]*chart.Files{
		"zeta":  {Values: map[string]any{"image": map[string]any{"repository": "rancher/z", "tag": "v1"}}},
		"alpha": {Values: map[string]any{"image": map[string]any{"repository": "rancher/a", "tag": "v1"}}},
	}

	images, _ := scanDependencies(deps)

	require.Equal(t, []valuesImage{
		{Path: "image", Image: "rancher/a:v1", Dependency: "alpha"},
		{Path: "image", Image: "rancher/z:v1", Dependency: "zeta"},
	}, images)
}

func TestScanDependenciesRecursesIntoNestedSubcharts(t *testing.T) {
	deps := map[string]*chart.Files{
		"sub": {
			Values: map[string]any{
				"image": map[string]any{"repository": "rancher/sub", "tag": "v1"},
			},
			Dependencies: map[string]*chart.Files{
				"nested": {
					Values: map[string]any{
						"image": map[string]any{"repository": "rancher/nested", "tag": "v2"},
					},
				},
			},
		},
	}

	images, _ := scanDependencies(deps)

	require.Equal(t, []valuesImage{
		{Path: "image", Image: "rancher/sub:v1", Dependency: "sub"},
		{Path: "image", Image: "rancher/nested:v2", Dependency: "sub/nested"},
	}, images)
}

func TestScanDependenciesReportsProblemsWithTheSubchartsPath(t *testing.T) {
	deps := map[string]*chart.Files{
		"sub": {
			Values: map[string]any{
				"image": map[string]any{"repository": "rancher/sub", "tag": "latest"},
			},
		},
	}

	_, problems := scanDependencies(deps)

	require.Equal(t, []string{`charts/sub/values.yaml image: "tag" must not be "latest"`}, problems)
}

func TestScanDependenciesReportsProblemsWithANestedSubchartsRealPath(t *testing.T) {
	deps := map[string]*chart.Files{
		"sub": {
			Dependencies: map[string]*chart.Files{
				"nested": {
					Values: map[string]any{
						"image": map[string]any{"repository": "rancher/nested", "tag": "latest"},
					},
				},
			},
		},
	}

	_, problems := scanDependencies(deps)

	require.Equal(t, []string{`charts/sub/charts/nested/values.yaml image: "tag" must not be "latest"`}, problems)
}
