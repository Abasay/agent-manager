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

package traces

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	amsvc "github.com/wso2/agent-manager/cli/pkg/clients/amsvc/gen"
	"github.com/wso2/agent-manager/cli/pkg/clients/observersvc"
	"github.com/wso2/agent-manager/cli/pkg/cmdutil"
	"github.com/wso2/agent-manager/cli/pkg/config"
)

// filterKeys are the query params the filter flags send.
var filterKeys = []string{"status", "minDurationMs", "minTokens", "minSpanCount", "model", "conversationId", "include"}

// capturedRequest is the last request the fake observer saw.
type capturedRequest struct {
	calls int
	path  string
	query url.Values
}

// newCapturingTraceClient serves body for every request and records it.
func newCapturingTraceClient(t *testing.T, body any) (func(context.Context) (*observersvc.Client, error), *capturedRequest, func()) {
	t.Helper()
	captured := &capturedRequest{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured.calls++
		captured.path = r.URL.Path
		captured.query = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	}))
	client, err := observersvc.NewClient(server.URL)
	if err != nil {
		server.Close()
		t.Fatalf("new client: %v", err)
	}
	return func(context.Context) (*observersvc.Client, error) { return client, nil }, captured, server.Close
}

// runTracesCmd runs `traces my-agent` with args against a fake observer.
func runTracesCmd(t *testing.T, body any, args ...string) (*capturedRequest, error) {
	t.Helper()
	ios, _, _ := newTraceTestIO(false)
	traceClient, captured, closeTraces := newCapturingTraceClient(t, body)
	defer closeTraces()
	am, closeAM := newAMTestClient(t, http.StatusOK)
	defer closeAM()

	f := &cmdutil.Factory{
		IOStreams:    ios,
		Observer:     traceClient,
		AgentManager: func(context.Context) (*amsvc.ClientWithResponses, error) { return am, nil },
		Config: func() (*config.Config, error) {
			return &config.Config{
				CurrentInstance: "default",
				Instances:       map[string]config.Instance{"default": {URL: "http://test", CurrentOrg: "acme"}},
			}, nil
		},
	}
	root := &cobra.Command{Use: "amctl", SilenceErrors: true, SilenceUsage: true}
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return cmdutil.FlagErrorWrap(err) })
	cmdutil.EnableOrgOverride(root, f)
	cmdutil.EnableProjectOverride(root, f)
	root.AddCommand(NewTracesCmd(f))
	root.SetArgs(append([]string{"traces", "my-agent", "--project", "triage", "--env", "dev"}, args...))
	return captured, root.Execute()
}

func TestTracesCmd_FilterFlagsReachTheServer(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want map[string]string
	}{
		{name: "no filters", args: nil, want: map[string]string{}},
		{name: "status", args: []string{"--status", "error"}, want: map[string]string{"status": "error"}},
		{name: "min-duration", args: []string{"--min-duration", "1.5s"}, want: map[string]string{"minDurationMs": "1500"}},
		{name: "min-duration zero", args: []string{"--min-duration", "0s"}, want: map[string]string{"minDurationMs": "0"}},
		{name: "min-tokens zero still filters", args: []string{"--min-tokens", "0"}, want: map[string]string{"minTokens": "0"}},
		{name: "min-spans", args: []string{"--min-spans", "25"}, want: map[string]string{"minSpanCount": "25"}},
		// The server fills models for a model filter itself.
		{name: "model", args: []string{"--model", "gpt-4o"}, want: map[string]string{"model": "gpt-4o"}},
		{name: "conversation", args: []string{"--conversation", "c-1"}, want: map[string]string{"conversationId": "c-1"}},
		{name: "show-models", args: []string{"--show-models"}, want: map[string]string{"include": "models"}},
		{
			name: "all together",
			args: []string{"--status", "ok", "--min-duration", "2s", "--min-tokens", "10", "--min-spans", "3", "--model", "claude", "--conversation", "c-2", "--show-models"},
			want: map[string]string{
				"status": "ok", "minDurationMs": "2000", "minTokens": "10", "minSpanCount": "3",
				"model": "claude", "conversationId": "c-2", "include": "models",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			captured, err := runTracesCmd(t, observersvc.TraceOverviewResponse{}, tc.args...)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if captured.path != "/api/v1/traces" {
				t.Errorf("path = %q, want /api/v1/traces", captured.path)
			}
			assertFilterQuery(t, captured.query, tc.want)
		})
	}
}

func TestTracesCmd_RejectsInvalidFilterFlags(t *testing.T) {
	for _, args := range [][]string{
		{"--status", "failed"},
		{"--min-duration", "-1s"},
		{"--min-duration", "30"},
		{"--min-tokens", "-1"},
		{"--min-tokens", "many"},
		{"--min-spans", "-1"},
		{"--condition", "high_latency", "--max-latency", "-100"},
		{"--condition", "high_token_usage", "--max-tokens", "-1"},
		{"--condition", "excessive_steps", "--max-spans", "-1"},
		{"--condition", "tool_call_fails", "--show-models"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			captured, err := runTracesCmd(t, observersvc.TraceOverviewResponse{}, args...)
			if err == nil {
				t.Fatal("expected an error")
			}
			if captured.calls != 0 {
				t.Error("observer was called despite an invalid flag")
			}
		})
	}
}

func TestTracesCmd_ConditionMapsToServerFilter(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want map[string]string
	}{
		{name: "error_status", args: []string{"--condition", "error_status"}, want: map[string]string{"status": "error"}},
		// The client-side checks were "above the threshold", so the minimum is one more.
		{name: "high_latency", args: []string{"--condition", "high_latency"}, want: map[string]string{"minDurationMs": "30001"}},
		{name: "high_latency custom", args: []string{"--condition", "high_latency", "--max-latency", "500"}, want: map[string]string{"minDurationMs": "501"}},
		{name: "high_token_usage", args: []string{"--condition", "high_token_usage"}, want: map[string]string{"minTokens": "10001"}},
		{name: "high_token_usage custom", args: []string{"--condition", "high_token_usage", "--max-tokens", "0"}, want: map[string]string{"minTokens": "1"}},
		{name: "excessive_steps", args: []string{"--condition", "excessive_steps"}, want: map[string]string{"minSpanCount": "41"}},
		{name: "excessive_steps custom", args: []string{"--condition", "excessive_steps", "--max-spans", "5"}, want: map[string]string{"minSpanCount": "6"}},
		{
			name: "combines with other filters",
			args: []string{"--condition", "error_status", "--min-tokens", "100"},
			want: map[string]string{"status": "error", "minTokens": "100"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			captured, err := runTracesCmd(t, observersvc.TraceOverviewResponse{}, tc.args...)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if captured.calls != 1 || captured.path != "/api/v1/traces" {
				t.Errorf("calls = %d, path = %q; want one list call", captured.calls, captured.path)
			}
			assertFilterQuery(t, captured.query, tc.want)
		})
	}
}

func TestTracesCmd_ToolCallFailsStaysClientSide(t *testing.T) {
	captured, err := runTracesCmd(t, observersvc.TraceExportResponse{}, "--condition", "tool_call_fails", "--status", "error")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if captured.path != "/api/v1/traces/export" {
		t.Errorf("path = %q, want the export endpoint", captured.path)
	}
	assertFilterQuery(t, captured.query, map[string]string{"status": "error"})
}

func TestTracesCmd_RejectsConditionWithItsFlag(t *testing.T) {
	for _, args := range [][]string{
		{"--condition", "error_status", "--status", "ok"},
		{"--condition", "high_latency", "--min-duration", "1s"},
		{"--condition", "high_token_usage", "--min-tokens", "0"},
		{"--condition", "excessive_steps", "--min-spans", "10"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			captured, err := runTracesCmd(t, observersvc.TraceOverviewResponse{}, args...)
			if err == nil {
				t.Fatal("expected a conflict error")
			}
			if !strings.Contains(err.Error(), args[1]) || !strings.Contains(err.Error(), args[2]) {
				t.Errorf("error should name both flags, got %q", err.Error())
			}
			if captured.calls != 0 {
				t.Error("observer was called despite the conflict")
			}
		})
	}
}

func TestListTraces_ModelColumn(t *testing.T) {
	resp := observersvc.TraceOverviewResponse{
		TotalCount: 1,
		Traces: []observersvc.TraceOverview{{
			TraceID:        "abc123",
			RootSpanName:   "handle_request",
			ConversationID: "conv-42",
			Models:         []string{"gpt-4o-mini-2024-07-18", "claude-sonnet"},
		}},
	}
	for _, tc := range []struct {
		name          string
		opts          ListTracesOptions
		wantModel     bool
		wantIncModels bool
	}{
		{name: "hidden by default", opts: ListTracesOptions{}, wantModel: false},
		{name: "show-models", opts: ListTracesOptions{ShowModels: true}, wantModel: true, wantIncModels: true},
		{name: "model filter implies it", opts: ListTracesOptions{Model: "gpt-4o"}, wantModel: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ios, out, _ := newTraceTestIO(false)
			client, captured, closeFn := newCapturingTraceClient(t, resp)
			defer closeFn()

			o := tc.opts
			o.IO, o.TraceClient, o.Scope = ios, client, traceBaseScope()
			o.AgentName, o.Limit = "my-agent", 10
			if err := runListTraces(context.Background(), &o); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			got := out.String()
			if !strings.Contains(got, "CONVERSATION") || !strings.Contains(got, "conv-42") {
				t.Errorf("Conversation column should always show, got %q", got)
			}
			if strings.Contains(got, "MODEL") != tc.wantModel {
				t.Errorf("Model column shown = %v, want %v: %q", !tc.wantModel, tc.wantModel, got)
			}
			if tc.wantModel && !strings.Contains(got, "gpt-4o-mini-2024-07-18 +1") {
				t.Errorf("Model column should show the first model and the count of the rest, got %q", got)
			}
			if (captured.query.Get("include") == "models") != tc.wantIncModels {
				t.Errorf("include = %q, want models sent = %v", captured.query.Get("include"), tc.wantIncModels)
			}
		})
	}
}

func TestListTraces_TruncatedNotice(t *testing.T) {
	trace := observersvc.TraceOverview{TraceID: "abc123", RootSpanName: "handle_request"}
	for _, tc := range []struct {
		name string
		resp observersvc.TraceOverviewResponse
		want string
	}{
		{
			name: "search stopped early",
			resp: observersvc.TraceOverviewResponse{Traces: []observersvc.TraceOverview{trace}, Truncated: true, NextCursor: "c"},
			want: "Showing matches from the traces searched so far; narrow the time range to see more.\n",
		},
		{
			name: "no matches yet",
			resp: observersvc.TraceOverviewResponse{Truncated: true, NextCursor: "c"},
			want: "Showing matches from the traces searched so far; narrow the time range to see more.\n",
		},
		{
			name: "depth cap",
			resp: observersvc.TraceOverviewResponse{Traces: []observersvc.TraceOverview{trace}, Truncated: true},
			want: "The list stops here: the remaining traces are more than 1,000 traces into this time range; narrow the time range to see more.\n",
		},
		{
			name: "complete",
			resp: observersvc.TraceOverviewResponse{Traces: []observersvc.TraceOverview{trace}},
			want: "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ios, out, errOut := newTraceTestIO(false)
			client, _, closeFn := newCapturingTraceClient(t, tc.resp)
			defer closeFn()

			err := runListTraces(context.Background(), &ListTracesOptions{
				IO: ios, TraceClient: client, Scope: traceBaseScope(),
				AgentName: "my-agent", Limit: 10, Status: statusError,
			})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if errOut.String() != tc.want {
				t.Errorf("stderr = %q, want %q", errOut.String(), tc.want)
			}
			if strings.Contains(out.String(), "narrow the time range") {
				t.Errorf("the notice belongs on stderr, stdout = %q", out.String())
			}
		})
	}
}

func TestListTraces_JSONCarriesTruncatedWithoutCursor(t *testing.T) {
	ios, out, errOut := newTraceTestIO(true)
	client, _, closeFn := newCapturingTraceClient(t, observersvc.TraceOverviewResponse{
		Traces:       []observersvc.TraceOverview{},
		LookedBackTo: "2026-05-12T03:00:00Z",
		Truncated:    true,
		NextCursor:   "c",
	})
	defer closeFn()

	err := runListTraces(context.Background(), &ListTracesOptions{
		IO: ios, TraceClient: client, Scope: traceBaseScope(),
		AgentName: "my-agent", Limit: 10, Status: statusError,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	data, _ := decodeEnvelope(t, out.String())["data"].(map[string]any)
	if data["truncated"] != true || data["lookedBackTo"] != "2026-05-12T03:00:00Z" {
		t.Errorf("data should carry truncated and lookedBackTo, got %v", data)
	}
	if _, ok := data["nextCursor"]; ok {
		t.Errorf("data should not carry a cursor the CLI can't take, got %v", data)
	}
	if errOut.Len() != 0 {
		t.Errorf("JSON mode should print no notice, stderr = %q", errOut.String())
	}
}

// assertFilterQuery checks the filter params are exactly want.
func assertFilterQuery(t *testing.T, q url.Values, want map[string]string) {
	t.Helper()
	for _, key := range filterKeys {
		wantVal, set := want[key]
		if !set {
			if q.Has(key) {
				t.Errorf("%s = %q, want unset", key, q.Get(key))
			}
			continue
		}
		if q.Get(key) != wantVal {
			t.Errorf("%s = %q, want %q", key, q.Get(key), wantVal)
		}
	}
}
