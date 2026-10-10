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
	"time"

	"github.com/spf13/cobra"

	"github.com/wso2/agent-manager/cli/pkg/cmdutil"
	"github.com/wso2/agent-manager/cli/pkg/render"
)

// NewTracesCmd creates the `agent traces` command.
func NewTracesCmd(f *cmdutil.Factory) *cobra.Command {
	opts := &ListTracesOptions{
		IO:           f.IOStreams,
		TraceClient:  f.Observer,
		AMClient:     f.AgentManager,
		ResolveScope: f.ResolveOrgProject,
		ResolveAgent: f.ResolveAgent,
		ResolveEnv:   f.ResolveEnvironment,
		MakeScope:    f.EnvScope,
	}
	var (
		since       string
		minDuration time.Duration
		minTokens   int64
		minSpans    int64
	)

	cmd := &cobra.Command{
		Use:   "traces <agent>",
		Short: "List and manage traces for an agent",
		Long: "List traces for an agent.\n\n" +
			"Filters run on the server across the whole --since window, not just one page, and AND together. " +
			"If the server stops searching early, a notice on stderr says so; a narrower time range shows more.\n\n" +
			"--condition is a shorthand for one filter: error_status is --status error, " +
			"high_latency is a duration above --max-latency, high_token_usage is tokens above --max-tokens, " +
			"and excessive_steps is spans above --max-spans. tool_call_fails is checked in the CLI " +
			"over one page of exported traces, since the server has no tool filter yet.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			org, proj, err := opts.ResolveScope(cmd, true, true)
			if err != nil {
				return render.Error(opts.IO, render.Scope{}, err)
			}
			agentName, _, err := opts.ResolveAgent(args)
			if err != nil {
				return render.Error(opts.IO, render.Scope{}, err)
			}
			env, err := opts.ResolveEnv(cmd)
			if err != nil {
				return render.Error(opts.IO, render.Scope{}, err)
			}

			scope := opts.MakeScope(org, proj, agentName, env)
			opts.Org, opts.Proj, opts.AgentName, opts.Env, opts.Scope = org, proj, agentName, env, scope

			opts.StartTime, opts.EndTime, err = cmdutil.ResolveSinceWindow(since)
			if err != nil {
				return render.Error(opts.IO, scope, cmdutil.FlagErrorf("--since: %v", err))
			}

			if opts.Limit < 1 || opts.Limit > 100 {
				return render.Error(opts.IO, scope, cmdutil.FlagErrorf("--limit must be between 1 and 100"))
			}

			if cmd.Flags().Changed("min-duration") {
				opts.MinDurationMs = int64Ptr(minDuration.Milliseconds())
			}
			if cmd.Flags().Changed("min-tokens") {
				opts.MinTokens = &minTokens
			}
			if cmd.Flags().Changed("min-spans") {
				opts.MinSpans = &minSpans
			}
			if err := resolveFilters(opts); err != nil {
				return render.Error(opts.IO, scope, err)
			}

			if err := preflightEnv(cmd.Context(), opts.AMClient, org, env); err != nil {
				return render.Error(opts.IO, scope, err)
			}

			if opts.Condition == conditionToolCallFails {
				return runToolCallFails(cmd.Context(), opts)
			}
			return runListTraces(cmd.Context(), opts)
		},
	}
	cmd.Flags().StringVar(&since, "since", "24h", "Time window (e.g. 1h, 30m, 7d)")
	cmd.Flags().IntVar(&opts.Limit, "limit", 10, "Max traces to return (1-100)")
	cmd.Flags().StringVar(&opts.SortOrder, "sort", "desc", "Sort order: asc or desc")
	cmd.Flags().StringVar(&opts.Status, "status", "", "Only traces with this status: error or ok")
	cmd.Flags().DurationVar(&minDuration, "min-duration", 0, "Only traces lasting at least this long (e.g. 500ms, 30s)")
	cmd.Flags().Int64Var(&minTokens, "min-tokens", 0, "Only traces using at least this many tokens")
	cmd.Flags().Int64Var(&minSpans, "min-spans", 0, "Only traces with at least this many spans")
	cmd.Flags().StringVar(&opts.Model, "model", "", "Only traces where a model name contains this value, ignoring case (implies --show-models)")
	cmd.Flags().StringVar(&opts.ConversationID, "conversation", "", "Only traces with exactly this conversation ID")
	cmd.Flags().BoolVar(&opts.ShowModels, "show-models", false, "Add a Model column (costs the server one extra call per trace)")
	cmd.Flags().StringVar(&opts.Condition, "condition", "", "Shorthand filter: error_status, high_latency, high_token_usage, tool_call_fails, excessive_steps")
	cmd.Flags().IntVar(&opts.MaxLatency, "max-latency", 30000, "Latency threshold in ms; high_latency matches traces above it")
	cmd.Flags().IntVar(&opts.MaxTokens, "max-tokens", 10000, "Token threshold; high_token_usage matches traces above it")
	cmd.Flags().IntVar(&opts.MaxSpans, "max-spans", 40, "Span count threshold; excessive_steps matches traces above it")
	cmdutil.AddEnvFlag(cmd)
	cmd.ValidArgsFunction = func(cmd *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return cmdutil.CompleteAgents(cmd, f), cobra.ShellCompDirectiveNoFileComp
	}

	cmd.AddCommand(NewExportCmd(f))
	return cmd
}
