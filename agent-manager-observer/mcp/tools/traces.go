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
	"fmt"

	gomcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/wso2/agent-manager/agent-manager-observer/controllers"
	"github.com/wso2/agent-manager/agent-manager-observer/rbac"
)

const (
	// defaultTraceListLimit/maxTraceListLimit mirror defaultLimit/maxLimit
	// used by GetTraceOverviews in handlers/handlers.go.
	defaultTraceListLimit = 10
	maxTraceListLimit     = 1000

	// defaultTraceExportLimit/maxTraceExportLimit mirror the (100, maxLimit)
	// pair ExportTraces uses in handlers/handlers.go.
	defaultTraceExportLimit = 100
	maxTraceExportLimit     = 1000

	// traceDetailsSpanLimit is the span-list size get_trace_details requests
	// from GetTraceSpans. The tool intentionally has no "limit" input (its
	// job is "show me this trace", not a paginated span list), so it always
	// asks for the controller's hard per-request cap rather than the REST
	// route's paginated default of 10.
	traceDetailsSpanLimit = controllers.MaxSpansPerRequest

	// traceDetailsSortOrder is the fixed sort order get_trace_details
	// requests, mirroring GetTraceSpans' REST default ("asc" — chronological).
	traceDetailsSortOrder = "asc"
)

// tracesInput backs get_traces (-> ExportTraces) and is the base of
// listTracesInput: both REST routes accept the same scope, time-window,
// paging and filter inputs. Set filters AND together.
type tracesInput struct {
	Organization   string `json:"organization" jsonschema:"required"`
	Project        string `json:"project" jsonschema:"required"`
	Agent          string `json:"agent" jsonschema:"required"`
	Environment    string `json:"environment" jsonschema:"required"`
	StartTime      string `json:"start_time,omitempty"`
	EndTime        string `json:"end_time,omitempty"`
	Limit          *int   `json:"limit,omitempty"`
	SortOrder      string `json:"sort_order,omitempty"`
	Status         string `json:"status,omitempty" jsonschema:"Only traces whose root span is 'error' or 'ok'. ANDs with the other filters."`
	MinDurationMs  *int64 `json:"min_duration_ms,omitempty" jsonschema:"Only traces lasting at least this many milliseconds. ANDs with the other filters."`
	MinTokens      *int64 `json:"min_tokens,omitempty" jsonschema:"Only traces using at least this many tokens in total; a trace with no token usage never matches. ANDs with the other filters."`
	MinSpanCount   *int64 `json:"min_span_count,omitempty" jsonschema:"Only traces with at least this many spans. ANDs with the other filters."`
	Model          string `json:"model,omitempty" jsonschema:"Only traces where a model name contains this value, ignoring case (gpt-4o also matches gpt-4o-mini). ANDs with the other filters."`
	ConversationID string `json:"conversation_id,omitempty" jsonschema:"Only traces with exactly this conversation ID. ANDs with the other filters."`
}

// listTracesInput backs list_traces (-> GetTraceOverviews), which also takes
// include_models and a paging cursor.
type listTracesInput struct {
	tracesInput
	IncludeModels bool   `json:"include_models,omitempty" jsonschema:"Fill models on every trace. Costs one extra upstream call per trace; the model filter implies it."`
	Cursor        string `json:"cursor,omitempty" jsonschema:"nextCursor from a previous list_traces result, to get the next page. Send the same time window, sort order and filters."`
}

// traceDetailsInput mirrors REST GetTraceSpans: only organization + trace_id
// are required. project/agent/environment are optional scoping filters (the
// REST route treats them as optionalStr), so a valid organization + trace_id
// lookup succeeds without them.
type traceDetailsInput struct {
	Organization string `json:"organization" jsonschema:"required"`
	TraceID      string `json:"trace_id" jsonschema:"required"`
	Project      string `json:"project,omitempty"`
	Agent        string `json:"agent,omitempty"`
	Environment  string `json:"environment,omitempty"`
	StartTime    string `json:"start_time,omitempty"`
	EndTime      string `json:"end_time,omitempty"`
}

// spanDetailsInput has no organization: GetSpanDetail looks a span up by
// trace/span ID alone and never consults organization/namespace scoping.
type spanDetailsInput struct {
	TraceID string `json:"trace_id" jsonschema:"required"`
	SpanID  string `json:"span_id" jsonschema:"required"`
}

func (t *Toolsets) registerTraceTools(server *gomcp.Server) {
	authorize := t.guard(rbac.TraceRead)

	gomcp.AddTool(server, &gomcp.Tool{
		Name: "list_traces",
		Description: "Returns a summary view of recent traces for an agent within a time window. " +
			"A trace is a single end-to-end execution record for an agent request. " +
			"Filters are applied server-side across the whole window, not just one page. " +
			"The result reports lookedBackTo (how far back the search got) and truncated (the search stopped early); " +
			"pass nextCursor back as cursor to continue.",
	}, withToolLogging("list_traces", listTraces(t.Tracing, authorize)))

	gomcp.AddTool(server, &gomcp.Tool{
		Name: "get_traces",
		Description: "Returns the traces for an agent including full span details within a time window. " +
			"A trace is a single end-to-end execution record for an agent which contains spans that record the internal steps of an execution. " +
			"Filters are applied server-side across the whole window. " +
			"The result reports lookedBackTo (how far back a filtered search got) and truncated (the search stopped early or a trace's spans were cut).",
	}, withToolLogging("get_traces", getTraces(t.Tracing, authorize)))

	gomcp.AddTool(server, &gomcp.Tool{
		Name:        "get_trace_details",
		Description: "Return the metadata plus its span list for one trace",
	}, withToolLogging("get_trace_details", getTraceDetails(t.Tracing, authorize)))

	gomcp.AddTool(server, &gomcp.Tool{
		Name: "get_span_details",
		Description: "Return the execution details for a single span. " +
			"A span is a single step within a trace execution, such as an LLM call, tool invocation, or retriever lookup, capturing its timing, inputs, outputs, and attributes",
	}, withToolLogging("get_span_details", getSpanDetails(t.Tracing, authorize)))
}

// scopedTraceInput is the common subset of listTracesInput/traceDetailsInput
// needed to validate and build the shared portion of TraceQueryParams.
type scopedTraceInput struct {
	Organization string
	Project      string
	Agent        string
	Environment  string
}

func requireTraceScope(organization, project, agent, environment string) (scopedTraceInput, error) {
	org, err := requireField(organization, "organization")
	if err != nil {
		return scopedTraceInput{}, err
	}
	project, err = requireField(project, "project")
	if err != nil {
		return scopedTraceInput{}, err
	}
	agent, err = requireField(agent, "agent")
	if err != nil {
		return scopedTraceInput{}, err
	}
	environment, err = requireField(environment, "environment")
	if err != nil {
		return scopedTraceInput{}, err
	}
	return scopedTraceInput{Organization: org, Project: project, Agent: agent, Environment: environment}, nil
}

// queryParams validates the inputs list_traces and get_traces share.
func (in tracesInput) queryParams(defaultLimit, maxLimit int) (controllers.TraceQueryParams, error) {
	scope, err := requireTraceScope(in.Organization, in.Project, in.Agent, in.Environment)
	if err != nil {
		return controllers.TraceQueryParams{}, err
	}
	startTime, endTime, err := resolveTraceTimeWindow(in.StartTime, in.EndTime)
	if err != nil {
		return controllers.TraceQueryParams{}, err
	}
	sortOrder, err := validateSortOrder(in.SortOrder, "desc")
	if err != nil {
		return controllers.TraceQueryParams{}, err
	}
	limit, err := validateLimit(in.Limit, defaultLimit, maxLimit)
	if err != nil {
		return controllers.TraceQueryParams{}, err
	}
	filters, err := in.filters()
	if err != nil {
		return controllers.TraceQueryParams{}, err
	}
	return controllers.TraceQueryParams{
		Organization: scope.Organization,
		Project:      &scope.Project,
		Agent:        &scope.Agent,
		Environment:  &scope.Environment,
		StartTime:    startTime,
		EndTime:      endTime,
		Limit:        limit,
		SortOrder:    sortOrder,
		Filters:      filters,
	}, nil
}

// filters validates the filter inputs with the HTTP handler's rules.
func (in tracesInput) filters() (controllers.TraceFilters, error) {
	status, err := controllers.ParseTraceStatus(in.Status)
	if err != nil {
		return controllers.TraceFilters{}, err
	}
	for _, m := range []struct {
		name string
		val  *int64
	}{{"min_duration_ms", in.MinDurationMs}, {"min_tokens", in.MinTokens}, {"min_span_count", in.MinSpanCount}} {
		if m.val == nil {
			continue
		}
		if err := controllers.CheckMinThreshold(m.name, *m.val); err != nil {
			return controllers.TraceFilters{}, err
		}
	}
	if err := controllers.CheckFilterValue("model", in.Model); err != nil {
		return controllers.TraceFilters{}, err
	}
	if err := controllers.CheckFilterValue("conversation_id", in.ConversationID); err != nil {
		return controllers.TraceFilters{}, err
	}
	return controllers.TraceFilters{
		Status:         status,
		MinDurationMs:  in.MinDurationMs,
		MinTokens:      in.MinTokens,
		MinSpanCount:   in.MinSpanCount,
		Model:          in.Model,
		ConversationID: in.ConversationID,
	}, nil
}

// listTraces handles list_traces: one page of trace overviews, filtered and paged by cursor.
func listTraces(tracing *controllers.TracingController, authorize func(*gomcp.CallToolRequest) error) func(context.Context, *gomcp.CallToolRequest, listTracesInput) (*gomcp.CallToolResult, any, error) {
	return func(ctx context.Context, req *gomcp.CallToolRequest, input listTracesInput) (*gomcp.CallToolResult, any, error) {
		if err := authorize(req); err != nil {
			return nil, nil, err
		}
		params, err := input.queryParams(defaultTraceListLimit, maxTraceListLimit)
		if err != nil {
			return nil, nil, err
		}
		params.Include.Models = input.IncludeModels
		if input.Cursor != "" {
			if params.Cursor, err = controllers.DecodeTraceCursor(input.Cursor); err != nil {
				return nil, nil, fmt.Errorf("invalid cursor")
			}
		}

		result, err := tracing.GetTraceOverviews(ctx, params)
		if err != nil {
			return nil, nil, wrapToolError("list_traces", err)
		}
		return handleToolResult(result, nil)
	}
}

// getTraces handles get_traces: full traces with their spans, filtered.
func getTraces(tracing *controllers.TracingController, authorize func(*gomcp.CallToolRequest) error) func(context.Context, *gomcp.CallToolRequest, tracesInput) (*gomcp.CallToolResult, any, error) {
	return func(ctx context.Context, req *gomcp.CallToolRequest, input tracesInput) (*gomcp.CallToolResult, any, error) {
		if err := authorize(req); err != nil {
			return nil, nil, err
		}
		params, err := input.queryParams(defaultTraceExportLimit, maxTraceExportLimit)
		if err != nil {
			return nil, nil, err
		}

		result, err := tracing.ExportTraces(ctx, params)
		if err != nil {
			return nil, nil, wrapToolError("get_traces", err)
		}
		return handleToolResult(result, nil)
	}
}

func getTraceDetails(tracing *controllers.TracingController, authorize func(*gomcp.CallToolRequest) error) func(context.Context, *gomcp.CallToolRequest, traceDetailsInput) (*gomcp.CallToolResult, any, error) {
	return func(ctx context.Context, req *gomcp.CallToolRequest, input traceDetailsInput) (*gomcp.CallToolResult, any, error) {
		if err := authorize(req); err != nil {
			return nil, nil, err
		}
		organization, err := requireField(input.Organization, "organization")
		if err != nil {
			return nil, nil, err
		}
		traceID, err := requireField(input.TraceID, "trace_id")
		if err != nil {
			return nil, nil, err
		}

		startTime, endTime, err := resolveTraceTimeWindow(input.StartTime, input.EndTime)
		if err != nil {
			return nil, nil, err
		}

		params := controllers.TraceQueryParams{
			Organization: organization,
			Project:      optionalScope(input.Project),
			Agent:        optionalScope(input.Agent),
			Environment:  optionalScope(input.Environment),
			StartTime:    startTime,
			EndTime:      endTime,
			Limit:        traceDetailsSpanLimit,
			SortOrder:    traceDetailsSortOrder,
		}

		result, err := tracing.GetTraceSpans(ctx, traceID, params)
		if err != nil {
			return nil, nil, wrapToolError("get_trace_details", err)
		}
		return handleToolResult(result, nil)
	}
}

func getSpanDetails(tracing *controllers.TracingController, authorize func(*gomcp.CallToolRequest) error) func(context.Context, *gomcp.CallToolRequest, spanDetailsInput) (*gomcp.CallToolResult, any, error) {
	return func(ctx context.Context, req *gomcp.CallToolRequest, input spanDetailsInput) (*gomcp.CallToolResult, any, error) {
		if err := authorize(req); err != nil {
			return nil, nil, err
		}
		traceID, err := requireField(input.TraceID, "trace_id")
		if err != nil {
			return nil, nil, err
		}
		spanID, err := requireField(input.SpanID, "span_id")
		if err != nil {
			return nil, nil, err
		}

		result, err := tracing.GetSpanDetail(ctx, traceID, spanID)
		if err != nil {
			return nil, nil, wrapToolError("get_span_details", err)
		}
		return handleToolResult(result, nil)
	}
}
