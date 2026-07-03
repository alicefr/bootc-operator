package registry

import (
	"context"
	"fmt"
	"strings"

	"github.com/containers/image/v5/docker"
	"github.com/containers/image/v5/types"
)

// Resolver resolves image tags to digests using containers/image.
type Resolver struct {
	// AllowInsecure enables fallback to HTTP when the HTTPS connection
	// to the registry fails.
	AllowInsecure bool
}

func (r *Resolver) Resolve(ctx context.Context, ref string) (string, error) {
	if i := strings.LastIndex(ref, "@"); i >= 0 {
		return ref[i+1:], nil
	}

	imgRef, err := docker.ParseReference("//" + ref)
	if err != nil {
		return "", fmt.Errorf("parsing reference %q: %w", ref, err)
	}

	var sys *types.SystemContext
	if r.AllowInsecure {
		sys = &types.SystemContext{
			DockerInsecureSkipTLSVerify: types.OptionalBoolTrue,
		}
	}

	digest, err := docker.GetDigest(ctx, sys, imgRef)
	if err != nil {
		return "", fmt.Errorf("fetching digest for %q: %w", ref, err)
	}
	return digest.String(), nil
}
