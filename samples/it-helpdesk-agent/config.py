from __future__ import annotations

import os
import re
from dataclasses import dataclass


def _env(name: str, default: str | None = None) -> str:
    val = os.environ.get(name, default)
    if val is None:
        raise RuntimeError(f"Missing required env var: {name}")
    return val


def _flag(name: str, default: bool = False) -> bool:
    return _env(name, "true" if default else "false").strip().lower() == "true"


_SERVER_NAME = re.compile(r"^[a-z][a-z0-9_]*$")


@dataclass(frozen=True)
class McpServer:
    """One MCP proxy the agent connects to.

    Every server is described by environment variables named after it
    (upper-cased), so adding a server never needs a code change here:

        <NAME>_MCP_URL          proxy URL (required)
        <NAME>_MCP_API_KEY      platform-issued key (required unless OAuth)
        <NAME>_MCP_AUTH_HEADER  header carrying the key (default X-API-Key)
        <NAME>_MCP_OAUTH        true to use the agent's AgentID token instead
                                of a key (default: the global MCP_OAUTH)
        <NAME>_MCP_DESCRIPTION  one line shown to the model for servers that
                                have no built-in prompt profile in agent.py
    """

    name: str
    url: str
    api_key: str
    auth_header: str
    oauth: bool
    description: str


def _load_mcp_servers(use_mcp: bool, global_oauth: bool) -> tuple[McpServer, ...]:
    if not use_mcp:
        return ()

    # MCP_SERVERS is a comma-separated list, e.g. "github,jira". It defaults to
    # "github" so an agent configured for the original single-server setup keeps
    # working unchanged.
    names = [n.strip().lower() for n in _env("MCP_SERVERS", "github").split(",")]
    names = [n for n in names if n]
    if not names:
        raise RuntimeError("USE_MCP is true but MCP_SERVERS is empty")
    if len(set(names)) != len(names):
        raise RuntimeError(f"MCP_SERVERS has duplicate entries: {names}")

    servers: list[McpServer] = []
    for name in names:
        if not _SERVER_NAME.match(name):
            raise RuntimeError(
                f"Invalid MCP server name {name!r}: use lowercase letters, digits "
                "and underscores, starting with a letter"
            )
        prefix = name.upper()
        url = _env(f"{prefix}_MCP_URL", "")
        api_key = _env(f"{prefix}_MCP_API_KEY", "")
        oauth = _flag(f"{prefix}_MCP_OAUTH", global_oauth)

        if not url:
            raise RuntimeError(f"USE_MCP is true but {prefix}_MCP_URL is not set")
        if not oauth and not api_key:
            raise RuntimeError(f"USE_MCP is true but {prefix}_MCP_API_KEY is not set")

        servers.append(
            McpServer(
                name=name,
                url=url,
                api_key=api_key,
                auth_header=_env(f"{prefix}_MCP_AUTH_HEADER", "X-API-Key"),
                oauth=oauth,
                description=_env(f"{prefix}_MCP_DESCRIPTION", ""),
            )
        )
    return tuple(servers)


@dataclass(frozen=True)
class Config:
    company_name: str
    tone: str
    max_tickets_per_query: int
    additional_guidance: str
    agent_version: str
    use_llm_provider: bool
    llm_provider_url: str
    llm_provider_key: str
    use_mcp: bool
    mcp_servers: tuple[McpServer, ...]
    mcp_oauth: bool
    agentid_client_id: str
    agentid_client_secret: str
    agentid_token_endpoint: str
    agentid_scopes: str
    issue_tracker_repo: str
    jira_project_key: str

    @property
    def agentid_ready(self) -> bool:
        return bool(
            self.agentid_client_id
            and self.agentid_client_secret
            and self.agentid_token_endpoint
        )

    def mcp_server(self, name: str) -> McpServer | None:
        for server in self.mcp_servers:
            if server.name == name:
                return server
        return None

    # Kept so any code written against the single-server config still works.
    @property
    def mcp_url(self) -> str:
        server = self.mcp_server("github") or (
            self.mcp_servers[0] if self.mcp_servers else None
        )
        return server.url if server else ""

    @property
    def mcp_api_key(self) -> str:
        server = self.mcp_server("github") or (
            self.mcp_servers[0] if self.mcp_servers else None
        )
        return server.api_key if server else ""

    @classmethod
    def from_env(cls) -> "Config":
        raw_max_tickets = _env("MAX_TICKETS_PER_QUERY", "20")
        try:
            max_tickets = int(raw_max_tickets)
        except ValueError:
            raise RuntimeError(
                f"MAX_TICKETS_PER_QUERY must be an integer, got: {raw_max_tickets!r}"
            ) from None

        use_llm_provider = _flag("USE_LLM_PROVIDER")
        llm_provider_url = _env("LLM_PROVIDER_URL", "")
        llm_provider_key = _env("LLM_PROVIDER_KEY", "")

        if use_llm_provider:
            if not llm_provider_url:
                raise RuntimeError(
                    "USE_LLM_PROVIDER is true but LLM_PROVIDER_URL is not set"
                )
            if not llm_provider_key:
                raise RuntimeError(
                    "USE_LLM_PROVIDER is true but LLM_PROVIDER_KEY is not set"
                )

        use_mcp = _flag("USE_MCP")

        # With MCP_OAUTH=true the proxies are reached with an AgentID bearer token
        # instead of an API key, for proxies whose Security tab is set to OAuth.
        # The AMP_AGENTID_* vars are injected by Agent Manager into every
        # platform-hosted agent's pod; they are not validated here because
        # provisioning finishes asynchronously after deploy.
        mcp_oauth = _flag("MCP_OAUTH")

        mcp_servers = _load_mcp_servers(use_mcp, mcp_oauth)
        enabled = {s.name for s in mcp_servers}

        # Which repository holds the IT team's known-issue tracker. Only required
        # when the github server is enabled. Without it the agent would search
        # issues across the whole of GitHub, which is both slow and wrong.
        issue_tracker_repo = _env("ISSUE_TRACKER_REPO", "")
        if "github" in enabled:
            if not issue_tracker_repo:
                raise RuntimeError(
                    "The github MCP server is enabled but ISSUE_TRACKER_REPO is not "
                    "set (expected owner/repo, e.g. acme/it-tooling)"
                )
            if "/" not in issue_tracker_repo:
                raise RuntimeError(
                    f"ISSUE_TRACKER_REPO must be owner/repo, got: {issue_tracker_repo!r}"
                )

        # Optional: restrict Jira searches to one project (for example "IT").
        jira_project_key = _env("JIRA_PROJECT_KEY", "").strip()

        return cls(
            company_name=_env("COMPANY_NAME", "AcmeCorp"),
            tone=_env("TONE", "professional and helpful"),
            max_tickets_per_query=max_tickets,
            additional_guidance=_env("ADDITIONAL_GUIDANCE", ""),
            # Echoed in /health and every chat response so a promotion or a
            # rollback is visible from the outside without reading the console.
            agent_version=_env("AGENT_VERSION", "dev"),
            use_llm_provider=use_llm_provider,
            llm_provider_url=llm_provider_url,
            llm_provider_key=llm_provider_key,
            use_mcp=use_mcp,
            mcp_servers=mcp_servers,
            mcp_oauth=mcp_oauth,
            agentid_client_id=_env("AMP_AGENTID_CLIENT_ID", ""),
            agentid_client_secret=_env("AMP_AGENTID_CLIENT_SECRET", ""),
            agentid_token_endpoint=_env("AMP_AGENTID_TOKEN_ENDPOINT", ""),
            agentid_scopes=_env("AMP_AGENTID_SCOPES", ""),
            issue_tracker_repo=issue_tracker_repo,
            jira_project_key=jira_project_key,
        )
