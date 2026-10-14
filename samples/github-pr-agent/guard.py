"""Code-level limits on what the agent may do on GitHub.

The system prompt says what the agent should do; this interceptor makes sure it
cannot do more, whatever the model decides or a pull request's text asks for.
It runs on every GitHub MCP tool call:

- Repository allowlist. Any call naming owner/repo must target GITHUB_REPOS.
  Search queries must be scoped with repo: qualifiers from that list, and may
  not use org: or user: qualifiers. A write that names no repository is refused.
- Never allowed: deleting files, forking, creating repositories or gists, and
  triggering, re-running or cancelling workflows.
- No direct pushes to protected branches (GITHUB_PROTECTED_BRANCHES): changes
  go on a branch and through a pull request.
- Merging needs GITHUB_ALLOW_MERGE, an allowed merge method, the head SHA the
  agent reviewed (expectedHeadSha), and a user message in this turn that says
  "merge" and names the pull request number. Text inside a PR or comment can
  therefore never cause a merge on its own.
- Approving a pull request needs GITHUB_ALLOW_APPROVE.

A refusal goes back to the model as a tool error starting with BLOCKED.
"""

from __future__ import annotations

import logging
import re
from contextvars import ContextVar
from typing import Any, Awaitable, Callable

from mcp.types import CallToolResult, TextContent

from config import Config

log = logging.getLogger("github-pr-agent")

# The message the user sent in the current /chat turn. app.py sets it before
# running the agent; tool calls run in copies of that context, so they see it.
USER_MESSAGE: ContextVar[str] = ContextVar("user_message", default="")

_READ_WORDS = {"get", "list", "search", "read"}
_WRITE_WORDS = {
    "create", "update", "push", "merge", "add", "write", "delete", "fork",
    "trigger", "enable", "disable", "resolve", "assign", "dismiss", "rerun",
    "cancel", "mark", "remove", "star", "unstar",
}
_NEVER = {
    "delete_file", "create_repository", "fork_repository", "create_gist",
    "update_gist", "actions_run_trigger",
}
_MERGE_TOOLS = {"merge_pull_request", "enable_pr_auto_merge"}
_BRANCH_WRITES = {"push_files", "create_or_update_file", "create_branch"}
_QUALIFIER = re.compile(r"(?<![\w-])(repo|org|user):(\S+)", re.IGNORECASE)


def is_read(tool_name: str) -> bool:
    words = set(tool_name.lower().split("_"))
    return bool(words & _READ_WORDS) and not (words & _WRITE_WORDS)


def _merge_confirmed(message: str, number: Any) -> bool:
    """True when the user's own message says merge and names this PR."""
    if not re.search(r"\bmerge\b", message, re.IGNORECASE):
        return False
    try:
        n = int(number)
    except (TypeError, ValueError):
        return False
    return re.search(rf"(?<![\w/]){n}\b", message) is not None


def check(tool_name: str, args: dict[str, Any], cfg: Config, user_message: str) -> str | None:
    """Return why a call is refused, or None when it may proceed."""
    name = tool_name.lower()
    args = args or {}
    allowed = {r.lower() for r in cfg.repos}
    listed = ", ".join(cfg.repos)

    if name in _NEVER or "delete" in name.split("_"):
        return f"{tool_name} is not permitted for this agent."

    owner, repo = args.get("owner"), args.get("repo")
    if owner and repo:
        if f"{owner}/{repo}".lower() not in allowed:
            return f"{owner}/{repo} is not one of the repositories this agent may use ({listed})."
    elif "query" in args:
        quals = _QUALIFIER.findall(str(args.get("query") or ""))
        if any(kind.lower() != "repo" for kind, _ in quals):
            return "Searches may not use org: or user: qualifiers; scope them with repo:."
        repos = [value.lower() for _, value in quals]
        if not repos:
            return f"Scope the search with repo: qualifiers for {listed}."
        outside = [r for r in repos if r not in allowed]
        if outside:
            return f"{', '.join(outside)} is not one of the repositories this agent may use ({listed})."
    elif not is_read(name):
        return f"{tool_name} did not say which repository it changes; pass owner and repo."

    if name in _BRANCH_WRITES:
        branch = str(args.get("branch") or "")
        if branch.lower() in {b.lower() for b in cfg.protected_branches}:
            return (
                f"Direct writes to {branch} are not permitted. Create a new branch, "
                "commit there, and open a pull request."
            )

    if name in _MERGE_TOOLS:
        if not cfg.allow_merge:
            return "Merging is turned off for this agent (GITHUB_ALLOW_MERGE=false)."
        method = str(args.get("merge_method") or args.get("mergeMethod") or "").lower()
        if method and method not in cfg.merge_methods:
            return f"Merge method {method} is not allowed; use one of {', '.join(cfg.merge_methods)}."
        if name == "merge_pull_request" and not args.get("expectedHeadSha"):
            return (
                "Pass expectedHeadSha: the head commit SHA of the pull request you "
                "checked, so nothing unreviewed is merged."
            )
        number = args.get("pullNumber") or args.get("pull_number")
        if not _merge_confirmed(user_message, number):
            return (
                f"Merging needs the user's go-ahead in their own message. Ask them to "
                f"reply 'merge #{number}' to confirm."
            )

    if name == "pull_request_review_write" and str(args.get("event") or "").upper() == "APPROVE":
        if not cfg.allow_approve:
            return (
                "This agent may not approve pull requests. Submit the review as "
                "COMMENT, or REQUEST_CHANGES if there are blocking problems."
            )
    return None


Handler = Callable[[Any], Awaitable[Any]]


def github_guard(cfg: Config) -> Callable[[Any, Handler], Awaitable[Any]]:
    """Build an MCP tool-call interceptor enforcing the rules above."""

    async def interceptor(request: Any, handler: Handler) -> Any:
        reason = check(request.name, request.args, cfg, USER_MESSAGE.get())
        if reason is None:
            return await handler(request)
        log.warning("Blocked GitHub call %s: %s", request.name, reason)
        return CallToolResult(
            content=[TextContent(type="text", text=f"BLOCKED: {reason}")],
            isError=True,
        )

    return interceptor
