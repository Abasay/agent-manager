"""LangGraph IT helpdesk agent construction.

Builds a ReAct-style agent bound to the instance config.

When ``USE_LLM_PROVIDER=true``, requests are routed through the AM LLM
provider (which applies guardrails). Otherwise calls the model API directly
using OPENAI_API_KEY (and OPENAI_BASE_URL, if set).

The model name comes from LLM_MODEL (default gpt-4o-mini), so DeepSeek is just
LLM_MODEL=deepseek-chat plus the right key/base URL, with no code edits.

When ``USE_MCP=true``, tools discovered from an AM MCP proxy are merged with
the in-process tools. When it is off, the agent is exactly the v1 agent.
"""
from __future__ import annotations

import os
from typing import Any

from langchain_openai import ChatOpenAI
from langgraph.checkpoint.memory import InMemorySaver
from langgraph.prebuilt import create_react_agent

from config import Config
from tools import build_tools

MODEL = os.getenv("LLM_MODEL", "gpt-4o-mini")

SYSTEM_PROMPT_TEMPLATE = (
    "You are an IT helpdesk agent for {company_name}. "
    "You provide L1 technical support to employees.\n\n"
    "CAPABILITIES:\n"
    "- Look up employees and verify their identity\n"
    "- Check and create IT support tickets\n"
    "- Reset passwords (non-admin accounts only, after identity verification)\n"
    "- Request software access based on department eligibility\n"
    "- Check system status for outages and maintenance\n"
    "- Search IT policies\n"
    "- Escalate complex issues to L2 support\n"
    "{mcp_capabilities}"
    "\n"
    "RULES YOU MUST FOLLOW:\n"
    "1. IDENTITY FIRST: Before any write action (password reset, software access, "
    "ticket creation), verify the employee's identity using verify_identity. "
    "They must provide both their email and employee ID.\n"
    "2. CHECK BEFORE CREATE: Before creating a ticket, check system_status for "
    "known outages and get_open_tickets for duplicates.\n"
    "3. ADMIN ACCOUNTS: Never reset passwords for admin accounts (is_admin=true). "
    "Always escalate these to L2.\n"
    "4. POLICY CITATION: Search and cite the relevant IT policy before denying a "
    "request or performing a sensitive action.\n"
    "5. PRIVACY: Never disclose another employee's tickets, access, or personal info. "
    "Only show data belonging to the verified requester.\n"
    "6. ESCALATE WHEN UNSURE: If you cannot resolve an issue safely, escalate to L2 "
    "rather than guessing.\n"
    "{mcp_rules}"
    "\n"
    "Tone: {tone}. {additional_guidance}"
)

# Appended to the prompt only when MCP tools are loaded, so the base agent's
# behaviour — and the evaluators written against it — stay unchanged when the
# toggle is off.
MCP_CAPABILITIES = (
    "- Search the IT team's issue tracker for known problems that match what an "
    "employee is reporting\n"
)

# {issue_tracker_repo} is substituted at build time. Naming the repository in the
# prompt is what keeps searches scoped: without it the agent would search issues
# across all of GitHub, and a match in an unrelated project is not a known issue
# at AcmeCorp.
MCP_RULES = (
    "7. CHECK KNOWN ISSUES FIRST: When an employee reports something broken, "
    "search the IT team's issue tracker before creating a ticket, in addition to "
    "checking system_status and their open tickets (rule 2). The tracker is the "
    "repository {issue_tracker_repo} — always scope issue searches to it, for "
    "example by including 'repo:{issue_tracker_repo}' in the search query. Never "
    "report an issue from any other repository as a known issue. If a matching "
    "known issue exists, tell the employee its number and any workaround it "
    "documents instead of opening a duplicate ticket.\n"
    "8. THE ISSUE TRACKER IS READ-ONLY: You may search and read issues. Never "
    "create, comment on, edit, close, or reopen one — that is the engineering "
    "team's call, not L1's. If an issue needs changing, escalate to L2.\n"
)


async def load_mcp_tools(cfg: Config) -> list[Any]:
    """Discover tools from the AM MCP proxy.

    The proxy is reached with the platform-issued key in an ``X-API-Key`` header —
    the default for MCP proxies, and *not* the ``API-Key`` the LLM provider uses.
    Each proxy's header name is a Security setting, so check yours if the gateway
    answers 401. The agent never holds the upstream GitHub credential; the gateway
    attaches it on the way out.
    """
    if not cfg.use_mcp:
        return []

    from langchain_mcp_adapters.client import MultiServerMCPClient

    client = MultiServerMCPClient(
        {
            "github": {
                "url": cfg.mcp_url,
                "transport": "streamable_http",
                "headers": {"X-API-Key": cfg.mcp_api_key},
            }
        }
    )
    return list(await client.get_tools())


def build_llm(cfg: Config) -> ChatOpenAI:
    if cfg.use_llm_provider:
        # Via the AM gateway: the agent holds only the gateway key. The gateway
        # swaps in the real upstream key (e.g. DeepSeek) and applies guardrails.
        # The gateway accepts the key in "API-Key" (verified with curl). Override
        # with LLM_PROVIDER_AUTH_HEADER if your provider uses another header.
        header = os.getenv("LLM_PROVIDER_AUTH_HEADER", "API-Key")

        # This provider's context path is /chat/completions and the OpenAI client
        # appends /chat/completions itself, so the base URL must already end in it.
        # LLM_GATEWAY_URL (optional) overrides the platform-injected URL.
        base_url = (os.getenv("LLM_GATEWAY_URL") or cfg.llm_provider_url).rstrip("/")
        if not base_url.endswith("/chat/completions"):
            base_url += "/chat/completions"

        return ChatOpenAI(
            model=MODEL,
            temperature=0,
            base_url=base_url,
            api_key="not-used",
            default_headers={
                # LLM_GATEWAY_KEY (optional) overrides the platform-injected key.
                header: os.getenv("LLM_GATEWAY_KEY") or cfg.llm_provider_key,
                "Authorization": "",
            },
        )

    # Direct: OPENAI_API_KEY holds the upstream key. Set OPENAI_BASE_URL to
    # https://api.deepseek.com to talk to DeepSeek; leave it unset for OpenAI.
    return ChatOpenAI(
        model=MODEL,
        temperature=0,
        base_url=os.getenv("OPENAI_BASE_URL") or None,
    )


def build_agent(cfg: Config, mcp_tools: list[Any] | None = None) -> Any:
    llm = build_llm(cfg)

    tools = build_tools(cfg) + list(mcp_tools or [])
    system_prompt = SYSTEM_PROMPT_TEMPLATE.format(
        company_name=cfg.company_name,
        tone=cfg.tone,
        additional_guidance=cfg.additional_guidance,
        mcp_capabilities=MCP_CAPABILITIES if mcp_tools else "",
        mcp_rules=(
            MCP_RULES.format(issue_tracker_repo=cfg.issue_tracker_repo)
            if mcp_tools
            else ""
        ),
    )

    # Conversation state keyed on thread_id (the chat session_id). Without this
    # every turn arrives as a fresh message list, so "verify me, then reset my
    # password" cannot work across two turns.
    return create_react_agent(
        model=llm,
        tools=tools,
        prompt=system_prompt,
        checkpointer=InMemorySaver(),
    )