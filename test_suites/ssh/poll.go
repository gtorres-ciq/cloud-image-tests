// Copyright 2026 Google LLC.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package ssh

import (
	"fmt"
	"time"
)

const (
	pollDeadline = 5 * time.Minute
	pollInterval = 10 * time.Second
)

// pollUntil retries fn every interval until it succeeds or the deadline expires;
func pollUntil(deadline, interval time.Duration, what string, fn func() error) error {
	var last error
	for start := time.Now(); time.Since(start) < deadline; {
		if last = fn(); last == nil {
			return nil
		}
		time.Sleep(interval)
	}
	return fmt.Errorf("%s did not converge within %v: %w", what, deadline, last)
}
