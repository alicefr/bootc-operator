// SPDX-License-Identifier: Apache-2.0

package image

import (
	"testing"

	. "github.com/onsi/gomega"

	bootcv1alpha1 "github.com/bootc-dev/bootc-operator/api/v1alpha1"
)

func TestInfoMatchesDigest(t *testing.T) {
	const (
		platformDigest     = "sha256:db1b41aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1"
		manifestListDigest = "sha256:ff63bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb2"
		otherDigest        = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
		registry           = "registry.stage.redhat.io/rhel10/rhel-bootc-eks"
	)

	tests := []struct {
		name   string
		info   *bootcv1alpha1.ImageInfo
		digest string
		want   bool
	}{
		{
			name:   "nil info returns false",
			info:   nil,
			digest: platformDigest,
			want:   false,
		},
		{
			name: "matches platform-specific ImageDigest",
			info: &bootcv1alpha1.ImageInfo{
				Image:       registry + "@" + manifestListDigest,
				ImageDigest: platformDigest,
			},
			digest: platformDigest,
			want:   true,
		},
		{
			name: "matches manifest-list digest from pullspec",
			info: &bootcv1alpha1.ImageInfo{
				Image:       registry + "@" + manifestListDigest,
				ImageDigest: platformDigest,
			},
			digest: manifestListDigest,
			want:   true,
		},
		{
			name: "no match",
			info: &bootcv1alpha1.ImageInfo{
				Image:       registry + "@" + manifestListDigest,
				ImageDigest: platformDigest,
			},
			digest: otherDigest,
			want:   false,
		},
		{
			name: "image without digest (tag only)",
			info: &bootcv1alpha1.ImageInfo{
				Image:       registry + ":latest",
				ImageDigest: platformDigest,
			},
			digest: otherDigest,
			want:   false,
		},
		{
			name: "image without digest matches via ImageDigest",
			info: &bootcv1alpha1.ImageInfo{
				Image:       registry + ":latest",
				ImageDigest: platformDigest,
			},
			digest: platformDigest,
			want:   true,
		},
		{
			name: "invalid image reference",
			info: &bootcv1alpha1.ImageInfo{
				Image:       ":::invalid",
				ImageDigest: "sha256:aaa",
			},
			digest: otherDigest,
			want:   false,
		},
		{
			name: "empty image string falls back to ImageDigest",
			info: &bootcv1alpha1.ImageInfo{
				Image:       "",
				ImageDigest: platformDigest,
			},
			digest: platformDigest,
			want:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)
			g.Expect(InfoMatchesDigest(tt.info, tt.digest)).To(Equal(tt.want))
		})
	}
}
