// Copyright (c) 2026, WSO2 LLC. (https://www.wso2.com).
//
// WSO2 LLC. licenses this file to you under the Apache License,
// Version 2.0 (the "License"); you may not use this file except
// in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing,
// software distributed under the License is distributed on an
// "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
// KIND, either express or implied.  See the License for the
// specific language governing permissions and limitations
// under the License.

package config

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// TestReadOptionalBool covers the case an operator actually hit: a value
// padded with spaces must still parse, and one that cannot parse must fall
// back to the default loudly rather than look like a deliberate "false".
func TestReadOptionalBool(t *testing.T) {
	const env = "AMP_TEST_BOOL"
	tests := []struct {
		name     string
		value    string
		def      bool
		want     bool
		wantWarn bool
	}{
		{name: "unset uses default", value: "", def: true, want: true},
		{name: "plain true", value: "true", want: true},
		{name: "padded true is trimmed", value: " true ", want: true},
		{name: "false", value: "false", def: true, want: false},
		{name: "unparseable warns and uses default", value: "yes", def: false, want: false, wantWarn: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(env, tc.value)
			var buf bytes.Buffer
			orig := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
			t.Cleanup(func() { slog.SetDefault(orig) })

			r := &configReader{}
			if got := r.readOptionalBool(env, tc.def); got != tc.want {
				t.Errorf("readOptionalBool(%q) = %v, want %v", tc.value, got, tc.want)
			}
			if warned := strings.Contains(buf.String(), "ignoring unparseable boolean"); warned != tc.wantWarn {
				t.Errorf("warned = %v, want %v (log %q)", warned, tc.wantWarn, buf.String())
			}
			if tc.wantWarn && !strings.Contains(buf.String(), env) {
				t.Errorf("warning does not name the variable: %q", buf.String())
			}
		})
	}
}
