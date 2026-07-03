// SPDX-License-Identifier: Apache-2.0

package e2eutil

import (
	"testing"
)

func TestMergeArgs(t *testing.T) {
	tests := []struct {
		name    string
		oldArgs []string
		newArgs []string
		want    []string
	}{
		{
			name:    "replace one arg",
			oldArgs: []string{"--foo=bar", "--baz=qux"},
			newArgs: []string{"--foo=newbar"},
			want:    []string{"--foo=newbar", "--baz=qux"},
		},
		{
			name:    "replace multiple args",
			oldArgs: []string{"--foo=bar", "--baz=qux", "--hello=world"},
			newArgs: []string{"--foo=newbar", "--hello=neworld"},
			want:    []string{"--foo=newbar", "--baz=qux", "--hello=neworld"},
		},
		{
			name:    "no replacements preserves all args",
			oldArgs: []string{"--foo=bar", "--baz=qux"},
			newArgs: []string{},
			want:    []string{"--foo=bar", "--baz=qux"},
		},
		{
			name:    "new arg not in old args",
			oldArgs: []string{"--foo=bar", "--baz=qux"},
			newArgs: []string{"--newopt=value"},
			want:    []string{"--foo=bar", "--baz=qux", "--newopt=value"},
		},
		{
			name:    "boolean flag appended",
			oldArgs: []string{"--foo=bar", "--baz=qux"},
			newArgs: []string{"--enable-feature"},
			want:    []string{"--foo=bar", "--baz=qux", "--enable-feature"},
		},
		{
			name:    "boolean flag replaces existing boolean",
			oldArgs: []string{"--foo=bar", "--enable-feature", "--baz=qux"},
			newArgs: []string{"--enable-feature"},
			want:    []string{"--foo=bar", "--enable-feature", "--baz=qux"},
		},
		{
			name:    "replace value arg with different value format",
			oldArgs: []string{"--interval=5m"},
			newArgs: []string{"--interval=10s"},
			want:    []string{"--interval=10s"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mergeArgs(tt.oldArgs, tt.newArgs)
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("index %d: got %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}
