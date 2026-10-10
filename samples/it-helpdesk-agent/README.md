# IT Helpdesk Agent

An L1 IT helpdesk agent for the fictional **AcmeCorp**. Employees ask it to reset
passwords, request software, check ticket status, look up outages, and check the IT
team's issue tracker for known problems.

Built with LangGraph (`create_react_agent`) on FastAPI, serving the Agent Manager
chat contract: `POST /chat` on port `8000`.

## What it demonstrates

Nine in-process tools over mock JSON cover the core helpdesk flows. On top of those,
| Capability | Why it's here |
|---|---|
| `USE_MCP` toggle loading tools from an MCP proxy | Chapter 3 connects the agent to GitHub through an Agent Manager MCP proxy |
| Known-issue triage rules in the system prompt | The agent checks the IT team's issue tracker before opening a ticket, and may only read it |
| Session memory (LangGraph checkpointer) | Multi-turn "verify me, then act" flows work across turns |
| `AGENT_VERSION` echoed in `/health` and every chat response | Makes a promotion or rollback visible from the outside |
| `scripts/seed_traffic.py` | Generates enough traces for a monitor to score |

With `USE_MCP=false` (the default) the agent runs entirely on its in-process tools,
which is all Chapters 1 and 2 need.

## Configuration

All configuration is environment variables. Only the LLM credential is required.

| Variable | Default | Purpose |
|---|---|---|
| `OPENAI_API_KEY` | — | Required unless `USE_LLM_PROVIDER=true` |
| `COMPANY_NAME` | `AcmeCorp` | Used in the system prompt |
| `TONE` | `professional and helpful` | Used in the system prompt |
| `ADDITIONAL_GUIDANCE` | *(empty)* | Extra prompt text appended verbatim |
| `MAX_TICKETS_PER_QUERY` | `20` | Caps ticket listings |
| `AGENT_VERSION` | `dev` | Echoed in `/health` and chat responses |
| `USE_LLM_PROVIDER` | `false` | Route through an Agent Manager LLM Service Provider |
| `LLM_PROVIDER_URL` / `LLM_PROVIDER_KEY` | — | Injected by Agent Manager; required when `USE_LLM_PROVIDER=true` |
| `USE_MCP` | `false` | Load tools from an MCP proxy in addition to the in-process ones |
| `GITHUB_MCP_URL` / `GITHUB_MCP_API_KEY` | — | Injected by Agent Manager when the `GitHub` MCP proxy is attached (set these names under **Environment Variable Names** when attaching; the console defaults to `GITHUB_URL` / `GITHUB_API_KEY`); required when `USE_MCP=true` |
| `ISSUE_TRACKER_REPO` | — | `owner/repo` of the IT team's known-issue tracker. Required when `USE_MCP=true`; issue searches are scoped to it |
| `DATA_MODE` | `mock` | `mock` uses the bundled fixtures; `jira` works only on real systems. See [Real data mode](#real-data-mode-jira) |
| `JIRA_PROJECT_KEY` | — | Jira project the agent searches; in `jira` mode, the only project it may change |
| `JIRA_CLOUD_ID` | — | Atlassian site hostname or cloud ID passed to every Jira tool call |
| `JIRA_ISSUE_TYPE` | `Task` | Issue type for new tickets (`jira` mode) |
| `JIRA_OUTAGE_LABEL` | `outage` | Label marking known-outage issues (`jira` mode) |
| `JIRA_L2_LABEL` | `l2` | Label on tasks routed to IT staff (`jira` mode) |
| `CONFLUENCE_SPACE_KEY` | — | Confluence space holding IT policies (`jira` mode); without it the agent says it has no policy source |

`LLM_PROVIDER_*` and `GITHUB_*` are **system-managed** — Agent Manager writes them
per environment and they are read-only in the console. You never paste an upstream
OpenAI or GitHub credential into the agent.

## Run it locally

```bash
python -m venv env && source env/bin/activate
pip install -r requirements.txt

export OPENAI_API_KEY=sk-...
python main.py
```

Then:

```bash
curl -s localhost:8000/health

curl -s localhost:8000/chat -H 'Content-Type: application/json' -d '{
  "session_id": "demo-1",
  "message": "I need my password reset. alice.chen@acmecorp.com, E-1001."
}'
```

Reuse the same `session_id` across calls to continue a conversation. Omit it and
each request starts its own.

## Test data

Mock data lives in `data/` and resets on restart; nothing is persisted.

- **Employees** — `E-1001` … `E-1010`. `E-1004`, `E-1006`, `E-1009`, `E-1010` are
  admin accounts, which the agent must refuse to reset and escalate instead.
- **Policies** — `POL-IT-001` … `POL-IT-008`, covering password resets, software
  access, escalation, privacy, and after-hours support.
- **Tickets**, **software catalog**, **system status** — supporting fixtures.

Useful for exercising the rules: `alice.chen@acmecorp.com` / `E-1001` is a normal
Engineering account; `david.kim@acmecorp.com` / `E-1004` is an admin account.

## Real data mode (Jira)

With `DATA_MODE=jira` the nine fixture-backed tools are switched off and nothing
comes from `data/`. The agent works only through the Jira (and Confluence) MCP
proxy:

| Need | Where it comes from |
|---|---|
| Who is asking | The requester's work email, resolved to one Jira account |
| Their tickets | Issues in `JIRA_PROJECT_KEY` they reported |
| New tickets | Created in `JIRA_PROJECT_KEY` as `JIRA_ISSUE_TYPE` |
| Known outages | Open issues in the project labelled `JIRA_OUTAGE_LABEL` |
| Password reset, software access, escalation | A task labelled `JIRA_L2_LABEL` plus `password-reset`, `access-request` or `security`, for IT staff |
| IT policies | Confluence pages in `CONFLUENCE_SPACE_KEY` |

The agent may create issues, edit their summary, description, priority and
labels, comment, and change status, only on the requester's own issues in
`JIRA_PROJECT_KEY`. `guard.py` enforces that in code on every Jira tool call,
whatever the model decides: deletes and Confluence writes are refused, and so
is any write that targets another project or does not name its target. The
model gets a `BLOCKED:` tool error and explains it.

Minimum configuration:

```env
USE_MCP=true
MCP_SERVERS=jira
JIRA_MCP_URL=<proxy url>
JIRA_MCP_API_KEY=<proxy key>
DATA_MODE=jira
JIRA_PROJECT_KEY=IT
JIRA_CLOUD_ID=yourcompany.atlassian.net
```

The Jira account behind the MCP proxy needs permission to browse, create, edit,
comment on and transition issues in that project, and to look up users. Setting
the reporter to the requester also needs the *Modify Reporter* permission;
without it the agent records the requester in the description instead.

## Layout

```
agent.py               LLM binding, system prompt, MCP tool loading, checkpointer
app.py                 FastAPI app — /chat and /health
config.py              Environment configuration
guard.py               Code-level limits on Jira writes (DATA_MODE=jira)
main.py                Entrypoint (python main.py)
tools.py               The nine in-process tools
clients/               Mock backends behind the tools
data/                  Mock JSON fixtures
scripts/seed_traffic.py  Scripted conversations for seeding traces
```

## Deploying it

The tutorial series covers deployment, model governance, MCP tools, evaluation,
and promotion to production, in that order. Start at
[Create Your First Agent](https://wso2.github.io/agent-manager/docs/tutorials/create-your-first-agent).
