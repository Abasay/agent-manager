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


def _csv(raw: str) -> tuple[str, ...]:
    return tuple(dict.fromkeys(x.strip() for x in raw.split(",") if x.strip()))


_REPO = re.compile(r"^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$")
_MERGE_METHODS = ("merge", "squash", "rebase")


@dataclass(frozen=True)
class Config:
    agent_version: str
    use_llm_provider: bool
    llm_provider_url: str
    llm_provider_key: str
    github_mcp_url: str
    github_mcp_api_key: str
    github_mcp_auth_header: str
    repos: tuple[str, ...]
    protected_branches: tuple[str, ...]
    allow_merge: bool
    merge_methods: tuple[str, ...]
    default_merge_method: str
    allow_approve: bool
    additional_guidance: str

    @classmethod
    def from_env(cls) -> "Config":
        use_llm_provider = _flag("USE_LLM_PROVIDER")
        llm_provider_url = _env("LLM_PROVIDER_URL", "")
        llm_provider_key = _env("LLM_PROVIDER_KEY", "")
        if use_llm_provider and not (llm_provider_url and llm_provider_key):
            raise RuntimeError(
                "USE_LLM_PROVIDER is true but LLM_PROVIDER_URL or LLM_PROVIDER_KEY "
                "is not set"
            )

        # Injected by Agent Manager when the GitHub MCP proxy is attached. Set
        # these names under Environment Variable Names when attaching it.
        url = _env("GITHUB_MCP_URL", "")
        api_key = _env("GITHUB_MCP_API_KEY", "")
        if not url or not api_key:
            raise RuntimeError(
                "GITHUB_MCP_URL and GITHUB_MCP_API_KEY must be set: attach the GitHub "
                "MCP proxy and name its variables GITHUB_MCP_URL / GITHUB_MCP_API_KEY"
            )

        # The only repositories the agent may read or change, as owner/repo.
        repos = _csv(_env("GITHUB_REPOS", ""))
        if not repos:
            raise RuntimeError(
                "GITHUB_REPOS must list the repositories the agent may work on, "
                "comma-separated owner/repo (for example acme/api,acme/web)"
            )
        for repo in repos:
            if not _REPO.match(repo):
                raise RuntimeError(f"GITHUB_REPOS entries must be owner/repo, got {repo!r}")

        merge_methods = tuple(
            m.lower() for m in _csv(_env("GITHUB_MERGE_METHODS", "squash,merge,rebase"))
        )
        bad = [m for m in merge_methods if m not in _MERGE_METHODS]
        if bad or not merge_methods:
            raise RuntimeError(
                f"GITHUB_MERGE_METHODS must be some of {', '.join(_MERGE_METHODS)}, got {bad}"
            )
        default_merge = _env("GITHUB_DEFAULT_MERGE_METHOD", merge_methods[0]).lower()
        if default_merge not in merge_methods:
            raise RuntimeError(
                f"GITHUB_DEFAULT_MERGE_METHOD {default_merge!r} is not in GITHUB_MERGE_METHODS"
            )

        return cls(
            agent_version=_env("AGENT_VERSION", "dev"),
            use_llm_provider=use_llm_provider,
            llm_provider_url=llm_provider_url,
            llm_provider_key=llm_provider_key,
            github_mcp_url=url,
            github_mcp_api_key=api_key,
            github_mcp_auth_header=_env("GITHUB_MCP_AUTH_HEADER", "X-API-Key"),
            repos=repos,
            protected_branches=_csv(_env("GITHUB_PROTECTED_BRANCHES", "main,master")),
            allow_merge=_flag("GITHUB_ALLOW_MERGE", True),
            merge_methods=merge_methods,
            default_merge_method=default_merge,
            allow_approve=_flag("GITHUB_ALLOW_APPROVE", False),
            additional_guidance=_env("ADDITIONAL_GUIDANCE", ""),
        )
