package cmd

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"

	"github.com/rancherlabs/slsactl/pkg/imagecopy"
	"github.com/spf13/cobra"

	"github.com/rancher/cargueiro/internal/imagesig"
	"github.com/rancher/cargueiro/internal/oci"
	"github.com/rancher/cargueiro/internal/rules"
)

// copySignaturesFunc copies the signatures of the image at src to the same image at dst,
// each as host/repository:tag. It is slsactl's, which charts-build-scripts also used.
type copySignaturesFunc func(src, dst string) error

// copySignatures is the production copySignaturesFunc.
func copySignatures(src, dst string) error {
	return imagecopy.ImageAndSignature(src, dst)
}

func newCopyImageSignaturesCmd(opts *rootOptions, copyFn copySignaturesFunc) (*cobra.Command, error) {
	var from, to, productionUsername string
	var plainHTTP, dryRun bool
	var creds credentialFlags

	cmd := &cobra.Command{
		Use:  "copy-image-signatures <image>...",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return copyImageSignatures(cmd, opts, copyFn, args, from, to, productionUsername, plainHTTP, dryRun, &creds)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&from, "from", "", "registry the signatures are copied from: host[:port] (default: the staging images registry of rules.yaml)")
	flags.StringVar(&to, "to", "", "registry the signatures are copied to: host[:port] (default: the production images registry of rules.yaml)")
	flags.StringVar(&productionUsername, "production-username", "", "username for the destination registry (default: $"+envProductionUsername+")")
	flags.BoolVar(&plainHTTP, "plain-http", false, "talk HTTP to the registries instead of HTTPS, for local registries")
	flags.BoolVar(&dryRun, "dry-run", false, "say what would be copied and copy nothing")
	creds.addFlags(flags)
	if err := applyDoc(cmd, "copy-image-signatures"); err != nil {
		return nil, err
	}
	return cmd, nil
}

// signaturesBackend is the imagesig.Backend of the real registries.
type signaturesBackend struct {
	from, to       string
	fromOpts, toOp oci.FetchOptions
	copyFn         copySignaturesFunc
}

func (b signaturesBackend) side(s imagesig.Side) (string, oci.FetchOptions) {
	if s == imagesig.Source {
		return b.from, b.fromOpts
	}
	return b.to, b.toOp
}

func (b signaturesBackend) ref(s imagesig.Side, image imagesig.Image) (oci.ChartRef, oci.FetchOptions) {
	host, o := b.side(s)
	return oci.ChartRef{Host: host, Repository: image.Repository, Tag: image.Tag}, o
}

func (b signaturesBackend) Digest(ctx context.Context, s imagesig.Side, image imagesig.Image) (string, error) {
	ref, o := b.ref(s, image)
	return oci.ResolveDigestWithRetry(ctx, ref, o, oci.DefaultRetry())
}

func (b signaturesBackend) Signatures(ctx context.Context, s imagesig.Side, image imagesig.Image, digest string) (int, error) {
	ref, o := b.ref(s, image)
	return oci.CountSignatures(ctx, ref, digest, o)
}

func (b signaturesBackend) CopySignatures(_ context.Context, image imagesig.Image) error {
	err := b.copyFn(b.from+"/"+image.String(), b.to+"/"+image.String())
	if errors.Is(err, imagecopy.ErrNoSignaturesFound) {
		return nil
	}
	return err
}

// copyImageSignatures copies the signatures of the images given as repository:tag from the
// images registry of staging to the one of production, or the ones given.
func copyImageSignatures(cmd *cobra.Command, opts *rootOptions, copyFn copySignaturesFunc, args []string, from, to, productionUsername string, plainHTTP, dryRun bool, creds *credentialFlags) error {
	images := make([]imagesig.Image, 0, len(args))
	for _, a := range args {
		image, err := imagesig.Parse(a)
		if err != nil {
			return err
		}
		images = append(images, image)
	}

	cfg, err := rules.Parse(opts.rules)
	if err != nil {
		return err
	}
	fromHost, err := signaturesRegistryHost("--from", from, cfg.Registries().Images.Staging)
	if err != nil {
		return err
	}
	toHost, err := signaturesRegistryHost("--to", to, cfg.Registries().Images.Production)
	if err != nil {
		return err
	}

	guard, err := newGuard(opts.rules)
	if err != nil {
		return err
	}
	for _, image := range images {
		if err := guard.CheckImageRepository(oci.Target{Host: toHost, Path: path.Dir(image.Repository)}); err != nil {
			return err
		}
	}

	ctx := cmd.Context()
	source, err := creds.read(opts).connection(ctx, fromHost)
	if err != nil {
		return err
	}
	if productionUsername == "" {
		productionUsername = opts.getenv(envProductionUsername)
	}
	destination, err := resolveConnection(ctx, opts, toHost, productionUsername, opts.getenv(envProductionPassword))
	if err != nil {
		return err
	}

	if !dryRun {
		// slsactl logs in to the registries with the docker config, as it did for
		// charts-build-scripts. The credentials are the ones of the other commands, so they
		// go to a config of their own, for this run only, and each one to its own registry.
		restore, err := useDockerConfig(map[string]credentials{fromHost: source.credentials, toHost: destination.credentials})
		if err != nil {
			return err
		}
		defer restore()
	}

	backend := signaturesBackend{
		from: fromHost, to: toHost,
		fromOpts: fetchOptions(source, plainHTTP), toOp: fetchOptions(destination, plainHTTP),
		copyFn: copyFn,
	}
	return imagesig.Run(ctx, images, backend, dryRun, cmd.OutOrStdout())
}

// signaturesRegistryHost is the host of a registry given as host[:port], or of the registry of the
// rules, which is host/path, when none was given.
func signaturesRegistryHost(flag, given, fromRules string) (string, error) {
	if given == "" {
		target, err := oci.ParseTarget(fromRules)
		if err != nil {
			return "", fmt.Errorf("the registry of the rules: %w", err)
		}
		return target.Host, nil
	}
	target, err := oci.ParseTarget(given)
	if err != nil {
		return "", fmt.Errorf("%s: %w", flag, err)
	}
	if target.Path != "" {
		return "", fmt.Errorf("%s takes only host[:port]: the images carry their own path", flag)
	}
	return target.Host, nil
}

// useDockerConfig points DOCKER_CONFIG to a temporary config that has the credentials of
// each registry that has any, and returns the function that puts everything back and removes
// it. A registry with none is left out, so it is read anonymously.
func useDockerConfig(byHost map[string]credentials) (func(), error) {
	auths := map[string]map[string]string{}
	for host, c := range byHost {
		if c.Username == "" && c.Password == "" {
			continue
		}
		auths[host] = map[string]string{"auth": base64.StdEncoding.EncodeToString([]byte(c.Username + ":" + c.Password))}
	}
	data, err := json.Marshal(map[string]any{"auths": auths})
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "cargueiro-docker-config-")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), data, 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	previous, had := os.LookupEnv("DOCKER_CONFIG")
	if err := os.Setenv("DOCKER_CONFIG", dir); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	return func() {
		if had {
			_ = os.Setenv("DOCKER_CONFIG", previous)
		} else {
			_ = os.Unsetenv("DOCKER_CONFIG")
		}
		_ = os.RemoveAll(dir)
	}, nil
}
