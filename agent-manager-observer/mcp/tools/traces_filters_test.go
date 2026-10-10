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

package tools

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	gomcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/wso2/agent-manager/agent-manager-observer/controllers"
	"github.com/wso2/agent-manager/agent-manager-observer/observer"
)

const filterTestModel = "gpt-4o-mini-2024-07-18"

// withFilterTraces gives fake three traces a minute apart, newest first:
// trace-1's root errored, and trace-2 has an LLM leaf on filterTestModel.
func withFilterTraces(fake *fakeObserverClient) *fakeObserverClient {
	now := time.Now().UTC()
	fake.spans = map[string][]observer.SpanInfo{}
	for i, id := range []string{"trace-0", "trace-1", "trace-2"} {
		start := now.Add(-time.Duration(i+1) * time.Minute)
		root := observer.SpanInfo{
			SpanID: id + "-root", SpanName: "invoke_agent", StartTime: start, EndTime: start.Add(time.Second),
			Status: &observer.SpanStatus{Code: "ok"}, Attributes: map[string]interface{}{},
		}
		if id == "trace-1" {
			root.Status.Code = "error"
		}
		spans := []observer.SpanInfo{root}
		if id == "trace-2" {
			spans = append(spans, observer.SpanInfo{
				SpanID: id + "-leaf", SpanName: "openai.chat", ParentSpanID: root.SpanID, StartTime: start, EndTime: start,
				Attributes: map[string]interface{}{"gen_ai.response.model": filterTestModel},
			})
		}
		fake.spans[id] = spans
		fake.traces = append(fake.traces, observer.TraceInfo{
			TraceID: id, RootSpanID: root.SpanID, RootSpanName: root.SpanName, SpanCount: len(spans),
			StartTime: start, EndTime: root.EndTime, DurationNs: time.Second.Nanoseconds(),
		})
	}
	return fake
}

// callTraceTool calls a trace tool with the base scope plus extra and decodes its JSON result.
func callTraceTool(t *testing.T, session *gomcp.ClientSession, tool string, extra map[string]any) map[string]any {
	t.Helper()
	args := map[string]any{
		"organization": testOrgName,
		"project":      testProjectName,
		"agent":        testAgentName,
		"environment":  testEnvName,
	}
	maps.Copy(args, extra)
	result, err := session.CallTool(context.Background(), &gomcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool failed: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success, got tool error: %+v", result.Content)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(result.Content[0].(*gomcp.TextContent).Text), &out); err != nil {
		t.Fatalf("result is not JSON: %v", err)
	}
	return out
}

// resultTraces returns each result trace's ID and its models.
func resultTraces(t *testing.T, out map[string]any) (ids []string, models map[string][]any) {
	t.Helper()
	traces, ok := out["traces"].([]any)
	if !ok {
		t.Fatalf("result has no traces array: %v", out)
	}
	models = map[string][]any{}
	for _, tr := range traces {
		m := tr.(map[string]any)
		id := m["traceId"].(string)
		ids = append(ids, id)
		if ms, ok := m["models"].([]any); ok {
			models[id] = ms
		}
	}
	return ids, models
}

// spanListsWithAttributes counts the QueryTraceSpans calls with and without attributes.
func spanListsWithAttributes(t *testing.T, fake *fakeObserverClient) (with, without int) {
	t.Helper()
	for _, call := range fake.calls["QueryTraceSpans"] {
		req := call.([]any)[1].(observer.TracesQueryRequest)
		if req.IncludeAttributes {
			with++
		} else {
			without++
		}
	}
	return with, without
}

// Filters reach the controller: status filters the result, and a model filter
// turns models on as include_models does, so span lists carry attributes.
func TestListTracesFilters(t *testing.T) {
	cases := []struct {
		name    string
		args    map[string]any
		wantIDs []string
		// wantAttrs: Include.Models is on, so span lists carry attributes.
		wantAttrs bool
	}{
		{"no filters", nil, []string{"trace-0", "trace-1", "trace-2"}, false},
		{"status error", map[string]any{"status": "error"}, []string{"trace-1"}, false},
		{"model implies include_models", map[string]any{"model": "GPT-4O-MINI"}, []string{"trace-2"}, true},
		{"include_models", map[string]any{"include_models": true}, []string{"trace-0", "trace-1", "trace-2"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			session, fake := setupTestServer(t)
			withFilterTraces(fake)

			out := callTraceTool(t, session, "list_traces", tc.args)

			ids, models := resultTraces(t, out)
			if !slices.Equal(ids, tc.wantIDs) {
				t.Errorf("traces = %v, want %v", ids, tc.wantIDs)
			}
			with, without := spanListsWithAttributes(t, fake)
			if tc.wantAttrs && (with == 0 || without != 0) {
				t.Errorf("span lists with attributes = %d, without = %d; want only with", with, without)
			}
			if !tc.wantAttrs && (with != 0 || without == 0) {
				t.Errorf("span lists with attributes = %d, without = %d; want only without", with, without)
			}
			if got := models["trace-2"]; slices.Contains(tc.wantIDs, "trace-2") && !slices.Equal(got, []any{filterTestModel}) {
				t.Errorf("trace-2 models = %v, want [%s]", got, filterTestModel)
			}
			for _, key := range []string{"lookedBackTo", "truncated"} {
				if _, ok := out[key]; !ok {
					t.Errorf("result has no %q: %v", key, out)
				}
			}
		})
	}
}

// nextCursor from one call pages on in the next, filtered or not.
func TestListTracesCursorPages(t *testing.T) {
	cases := []struct {
		name    string
		args    map[string]any
		wantIDs []string
	}{
		{"no filters", nil, []string{"trace-0", "trace-1", "trace-2"}},
		{"status ok", map[string]any{"status": "ok"}, []string{"trace-0", "trace-2"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			session, fake := setupTestServer(t)
			withFilterTraces(fake)

			args := map[string]any{"limit": 1}
			maps.Copy(args, tc.args)
			var ids []string
			for page := 0; page < 10; page++ {
				out := callTraceTool(t, session, "list_traces", args)
				pageIDs, _ := resultTraces(t, out)
				ids = append(ids, pageIDs...)
				cursor, _ := out["nextCursor"].(string)
				if cursor == "" {
					break
				}
				args["cursor"] = cursor
			}
			if !slices.Equal(ids, tc.wantIDs) {
				t.Errorf("paged traces = %v, want %v", ids, tc.wantIDs)
			}
		})
	}
}

// get_traces exports only the matches and reports how far the search got.
func TestGetTracesStatusFilter(t *testing.T) {
	session, fake := setupTestServer(t)
	withFilterTraces(fake)

	out := callTraceTool(t, session, "get_traces", map[string]any{"status": "error"})

	if ids, _ := resultTraces(t, out); !slices.Equal(ids, []string{"trace-1"}) {
		t.Errorf("traces = %v, want [trace-1]", ids)
	}
	for _, call := range fake.calls["QueryTraceSpans"] {
		args := call.([]any)
		if args[1].(observer.TracesQueryRequest).IncludeAttributes && args[0] != "trace-1" {
			t.Errorf("full spans fetched for %v, want only trace-1", args[0])
		}
	}
	if lookedBackTo, _ := out["lookedBackTo"].(string); lookedBackTo == "" {
		t.Errorf("lookedBackTo is empty: %v", out)
	}
	if _, ok := out["truncated"]; !ok {
		t.Errorf("result has no truncated: %v", out)
	}
}

// Bad filters and cursors are rejected before any upstream call.
func TestTraceFilterInputsRejected(t *testing.T) {
	long := strings.Repeat("x", controllers.MaxFilterValueLen+1)
	deepCursor := controllers.TraceCursor{Rank: 1001, Time: time.Now()}.Encode()
	cases := []struct {
		name  string
		tools []string
		args  map[string]any
	}{
		{"unknown status", []string{"list_traces", "get_traces"}, map[string]any{"status": "failed"}},
		{"negative min_duration_ms", []string{"list_traces", "get_traces"}, map[string]any{"min_duration_ms": -1}},
		{"negative min_tokens", []string{"list_traces", "get_traces"}, map[string]any{"min_tokens": -1}},
		{"negative min_span_count", []string{"list_traces", "get_traces"}, map[string]any{"min_span_count": -1}},
		{"long model", []string{"list_traces", "get_traces"}, map[string]any{"model": long}},
		{"long conversation_id", []string{"list_traces", "get_traces"}, map[string]any{"conversation_id": long}},
		{"malformed cursor", []string{"list_traces"}, map[string]any{"cursor": "not-a-cursor"}},
		{"cursor past depth cap", []string{"list_traces"}, map[string]any{"cursor": deepCursor}},
		{"cursor on get_traces", []string{"get_traces"}, map[string]any{"cursor": "anything"}},
	}
	for _, tc := range cases {
		for _, tool := range tc.tools {
			t.Run(tc.name+"/"+tool, func(t *testing.T) {
				session, fake := setupTestServer(t)
				args := map[string]any{
					"organization": testOrgName,
					"project":      testProjectName,
					"agent":        testAgentName,
					"environment":  testEnvName,
				}
				maps.Copy(args, tc.args)

				result, err := session.CallTool(context.Background(), &gomcp.CallToolParams{Name: tool, Arguments: args})
				if err == nil && (result == nil || !result.IsError) {
					t.Fatal("expected an error, got success")
				}
				if calls := fake.calls["QueryTraces"]; len(calls) != 0 {
					t.Errorf("QueryTraces calls = %d, want 0", len(calls))
				}
			})
		}
	}
}
