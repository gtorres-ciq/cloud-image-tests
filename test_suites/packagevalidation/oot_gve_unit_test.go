// Copyright 2026 Google LLC.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package packagevalidation

import "testing"

func TestIsOOTGVEImage(t *testing.T) {
	tests := []struct {
		name  string
		image string
		want  bool
	}{
		{
			name:  "OOT GVE image",
			image: "rocky-linux-10-optimized-gcp-oot-gve-v20260911",
			want:  true,
		},
		{
			name:  "standard optimized image",
			image: "rocky-linux-10-optimized-gcp-v20260911",
			want:  false,
		},
		{
			name:  "lookalike derivative",
			image: "custom-rocky-linux-10-optimized-gcp-oot-gve-v20260911",
			want:  false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isOOTGVEImage(tc.image); got != tc.want {
				t.Fatalf("isOOTGVEImage(%q) = %t, want %t", tc.image, got, tc.want)
			}
		})
	}
}
