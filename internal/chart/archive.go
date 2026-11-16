package chart

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
)

// limits bound what is read from an archive, whose content is not trusted.
type limits struct {
	// file is the largest Chart.yaml or values.yaml.
	file int64
	// stream is the most that is read from the decompressed archive, which
	// stops a small archive that expands to something huge.
	stream int64
}

func defaultLimits() limits {
	return limits{
		file:   2 << 20,
		stream: 32 << 20,
	}
}

var errTooLarge = errors.New("the archive is too large once decompressed")

// LoadArchive reads Chart.yaml and values.yaml from a chart archive (.tgz), by
// exact path, in memory, and the same two files of every subchart under
// charts/, found recursively, as Dependencies. Helm always writes a dependency
// as a directory when it packages a chart, whether it was a directory or a
// packaged .tgz in the source tree, so a nested .tgz is never found inside an
// archive. The rest of the archive is skipped.
func LoadArchive(r io.Reader) (*Files, error) {
	return loadArchive(r, defaultLimits())
}

// ArchiveChartYAML returns Chart.yaml as it is written in a chart archive
// (.tgz), with every field it has. It is read the way LoadArchive reads it.
func ArchiveChartYAML(r io.Reader) ([]byte, error) {
	root, err := readArchive(r, defaultLimits())
	if err != nil {
		return nil, err
	}
	return root.chartData, nil
}

func loadArchive(r io.Reader, lim limits) (*Files, error) {
	root, err := readArchive(r, lim)
	if err != nil {
		return nil, err
	}
	return root.toFiles("")
}

// archiveNode is the Chart.yaml and values.yaml content of one chart in an
// archive, before they are parsed, and the same for its subcharts, keyed by
// name, found recursively.
type archiveNode struct {
	chartData, valuesData []byte
	haveChart, haveValues bool
	children              map[string]*archiveNode
}

// child returns the node for a direct subchart called name, creating it if
// this is the first file found for it.
func (n *archiveNode) child(name string) *archiveNode {
	if n.children == nil {
		n.children = make(map[string]*archiveNode)
	}
	c, ok := n.children[name]
	if !ok {
		c = &archiveNode{}
		n.children[name] = c
	}
	return c
}

// toFiles turns n, and its children recursively, into Files. label is where n
// is in the archive, used in parse errors: "" for the chart at the top, or
// "charts/<name>/" for a subchart.
func (n *archiveNode) toFiles(label string) (*Files, error) {
	files, err := newFiles(label+"Chart.yaml", n.chartData, label+"values.yaml", n.valuesData)
	if err != nil {
		return nil, err
	}

	deps := make(map[string]*Files)
	for name, child := range n.children {
		if !child.haveChart {
			continue
		}
		sub, err := child.toFiles(label + "charts/" + name + "/")
		if err != nil {
			return nil, err
		}
		deps[name] = sub
	}
	if len(deps) > 0 {
		files.Dependencies = deps
	}
	return files, nil
}

// readArchive reads every Chart.yaml and values.yaml in a chart archive: the
// ones at the top of the chart, and those of every subchart under charts/,
// found recursively.
func readArchive(r io.Reader, lim limits) (*archiveNode, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("the archive is not gzip: %w", err)
	}
	defer func() { _ = gz.Close() }() // nothing to flush: the archive is only read

	tr := tar.NewReader(&cappedReader{r: gz, left: lim.stream})

	root := &archiveNode{}
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read the archive: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}

		dependency, file, ok := chartPathOf(hdr.Name)
		if !ok {
			continue
		}
		node := root
		for _, name := range dependency {
			node = node.child(name)
		}

		var dst *[]byte
		var have *bool
		switch file {
		case "Chart.yaml":
			dst, have = &node.chartData, &node.haveChart
		case "values.yaml":
			dst, have = &node.valuesData, &node.haveValues
		default:
			continue
		}
		if *have {
			return nil, fmt.Errorf("%s appears twice in the archive", hdr.Name)
		}

		data, err := io.ReadAll(io.LimitReader(tr, lim.file+1))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", hdr.Name, err)
		}
		if int64(len(data)) > lim.file {
			return nil, fmt.Errorf("%s is larger than %d bytes", hdr.Name, lim.file)
		}
		*dst, *have = data, true
	}

	if !root.haveChart {
		return nil, errors.New("the archive has no Chart.yaml")
	}
	return root, nil
}

// chartPathOf splits a tar entry's path into the names of the subcharts it
// belongs to, the way they are found recursively ([]string{"sub", "nested"}
// for "fleet/charts/sub/charts/nested/Chart.yaml", nil for the chart's own
// "fleet/Chart.yaml"), and the file name. ok is false for anything else, such
// as a file that is not inside the chart's own top-level directory at all, or
// one under a subchart that is not paired with "charts/<name>".
func chartPathOf(name string) (dependency []string, file string, ok bool) {
	parts := strings.Split(path.Clean(name), "/")
	if len(parts) < 2 {
		return nil, "", false
	}
	parts = parts[1:] // the chart's own top-level directory, not used further

	file = parts[len(parts)-1]
	parts = parts[:len(parts)-1]

	for len(parts) >= 2 {
		if parts[0] != "charts" {
			return nil, "", false
		}
		dependency = append(dependency, parts[1])
		parts = parts[2:]
	}
	if len(parts) != 0 {
		return nil, "", false
	}
	return dependency, file, true
}

// cappedReader fails with errTooLarge once left bytes have been read.
type cappedReader struct {
	r    io.Reader
	left int64
}

func (c *cappedReader) Read(p []byte) (int, error) {
	if c.left <= 0 {
		return 0, errTooLarge
	}
	if int64(len(p)) > c.left {
		p = p[:c.left]
	}
	n, err := c.r.Read(p)
	c.left -= int64(n)
	return n, err
}
