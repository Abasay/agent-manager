# GitHub PR Agent

A LangGraph agent that works on pull requests through Agent Manager's GitHub
MCP proxy: it finds and analyzes PRs, posts reviews, creates branches and PRs,
updates them, and merges them once checks pass and you confirm.

Built on FastAPI, serving the Agent Manager chat contract: `POST /chat` on port
`8000`. The agent holds no GitHub credential; the MCP proxy attaches it.

## What it can do

| Ask | What happens |
|---|---|
| "What PRs are open in agent-manager?" | Lists open PRs with status |
| "Analyze PR 12" | Reads the PR, files, diff, checks and reviews; reports a summary, risks with file and line, missing tests, check results and a recommendation |
| "Post that as a review" | Posts it as COMMENT or REQUEST_CHANGES |
| "Add a CONTRIBUTING note to the README and open a PR" | Creates a branch, commits, opens a draft PR, returns the link |
| "Mark PR 12 ready and request review from alice" | Updates the PR |
| "Merge #12" | Checks it is open, not draft, mergeable and green, then merges with the head SHA it checked |

## Guardrails in code

`guard.py` checks every GitHub tool call before it reaches GitHub, whatever the
model decides or a PR's text asks for. A refused call comes back to the model as
`BLOCKED: ...` and it tells you why.

- **Repository allowlist.** Calls must target a repo in `GITHUB_REPOS`. Searches
  must be scoped with `repo:` qualifiers from that list; `org:` and `user:` are refused.
- **Protected branches.** No commits straight to `GITHUB_PROTECTED_BRANCHES`
  (default `main,master`); changes go on a branch and through a PR.
- **Never:** deleting files, forking, creating repositories or gists, triggering
  workflows.
- **Merging** needs `GITHUB_ALLOW_MERGE=true`, an allowed merge method, the head
  SHA the agent reviewed, and **your message in that turn must say "merge" and
  the PR number** (for example `merge #12`). Text inside a PR or comment can
  never cause a merge.
- **Approving** is off unless `GITHUB_ALLOW_APPROVE=true`.

Also turn on branch protection in GitHub (required reviews and checks on
`main`). The agent then cannot merge anything GitHub itself would not allow.

Separately, a slow or failed GitHub call becomes a readable error instead of
failing the chat, and results over `MAX_TOOL_RESULT_CHARS` are cut with a note.

## Configuration

| Variable | Default | Purpose |
|---|---|---|
| `GITHUB_MCP_URL` / `GITHUB_MCP_API_KEY` | — | Injected when the GitHub MCP proxy is attached. Set these names under **Environment Variable Names** when attaching (the console suggests `GITHUB_URL` / `GITHUB_API_KEY`) |
| `GITHUB_MCP_AUTH_HEADER` | `X-API-Key` | Header the proxy expects the key in |
| `GITHUB_REPOS` | — | **Required.** `owner/repo`, comma-separated: the only repositories the agent may use |
| `GITHUB_PROTECTED_BRANCHES` | `main,master` | Branches the agent may never commit to directly |
| `GITHUB_ALLOW_MERGE` | `true` | Allow merging (still needs your explicit `merge #N`) |
| `GITHUB_MERGE_METHODS` | `squash,merge,rebase` | Allowed merge methods |
| `GITHUB_DEFAULT_MERGE_METHOD` | first allowed | Used unless you ask for another |
| `GITHUB_ALLOW_APPROVE` | `false` | Allow the agent to submit APPROVE reviews |
| `USE_LLM_PROVIDER` | `false` | Route model calls through an Agent Manager LLM provider (`LLM_PROVIDER_URL` / `LLM_PROVIDER_KEY` are injected) |
| `LLM_PROVIDER_AUTH_HEADER` | `API-Key` | Header the LLM proxy expects the key in, if its security names a different one |
| `OPENAI_API_KEY` / `OPENAI_BASE_URL` | — | Direct model access when not using the LLM provider |
| `LLM_MODEL` | `gpt-4o-mini` | Model name |
| `MCP_TOOL_TIMEOUT` | `25` | Seconds one GitHub call may take; keep it under the gateway route timeout |
| `MAX_TOOL_RESULT_CHARS` | `40000` | Cap on one tool result (large diffs) |
| `ADDITIONAL_GUIDANCE` | *(empty)* | Extra prompt text, for example your review checklist |
| `AGENT_VERSION` | `dev` | Echoed in `/health` and chat responses |

The GitHub token behind the MCP proxy needs, on the listed repositories:
**Contents: read and write**, **Pull requests: read and write**, **Commit
statuses / Checks: read**, and **Metadata: read**. A fine-grained token limited
to those repositories is best.

## Deploy on Agent Manager

| Field | Value |
|---|---|
| GitHub Repository | your fork of this repository |
| App Path | `samples/github-pr-agent` |
| Language / Version | Python 3.11 |
| Start Command | `python main.py` |
| Port | `8000` |
| Interface | Chat Agent |

Then attach the GitHub MCP proxy (variable names `GITHUB_MCP_URL` /
`GITHUB_MCP_API_KEY`), set `GITHUB_REPOS` and either `USE_LLM_PROVIDER=true` or
`OPENAI_API_KEY`, and deploy. `/health` shows the repos and the number of GitHub
tools loaded; `mcp_tool_count: 0` means the proxy was not reachable.

## Run it locally

```bash
python -m venv env && source env/bin/activate
pip install -r requirements.txt
export OPENAI_API_KEY=sk-... GITHUB_MCP_URL=... GITHUB_MCP_API_KEY=... GITHUB_REPOS=owner/repo
python main.py

curl -s localhost:8000/chat -H 'Content-Type: application/json' \
  -d '{"session_id": "s1", "message": "What PRs are open?"}'
```

## Layout

```
agent.py     Prompt, model, GitHub MCP tool loading, timeout and size limits
app.py       FastAPI app: /chat and /health
config.py    Environment configuration
guard.py     Code-level limits on GitHub calls
main.py      Entrypoint (python main.py)
```
