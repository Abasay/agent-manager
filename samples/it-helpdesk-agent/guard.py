"""Code-level limits on what the agent may change in Jira.

The system prompt tells the model what it may do; this interceptor makes sure
it cannot do more, whatever the model decides or a Jira issue's text asks for.
It runs on every call to the ``jira`` MCP server when DATA_MODE=jira:

- Read tools (get, search, lookup, list, fetch, ...) pass through.
- Delete and remove tools are refused, and so is any Confluence write:
  policies are read-only.
- Every other write (create, edit, update, transition, comment, assign, ...)
  must name its target, and that target must be in JIRA_PROJECT_KEY: either a
  project argument equal to it or an issue key like ``IT-123``. A write whose
  target cannot be identified is refused rather than guessed at.

Tool names differ between Jira MCP servers (Atlassian's own uses camelCase like
``createJiraIssue``; others use ``jira_create_issue``), so tools are classified
by the verbs in their names and arguments are matched by common key spellings.
A refusal goes back to the model as a tool error so it can explain or correct.
"""

from __future__ import annotations

import logging
import re
from typing import Any, Awaitable, Callable

from mcp.types import CallToolResult, TextContent

log = logging.getLogger("it-helpdesk")

_READ_PREFIXES = ("get", "search", "lookup", "list", "fetch", "find", "read", "query")
_BLOCKED_VERBS = ("delete", "remove", "archive", "purge")
_WRITE_VERBS = (
    "create", "edit", "update", "transition", "comment", "add", "assign",
    "link", "set", "move", "worklog", "attach",
)

_PROJECT_KEYS = ("projectKey", "project_key", "projectIdOrKey", "project")
_ISSUE_KEYS = ("issueIdOrKey", "issue_id_or_key", "issueKey", "issue_key", "issue", "key")
_ISSUE_KEY = re.compile(r"^([A-Z][A-Z0-9_]+)-\d+$")


def _words(tool_name: str) -> list[str]:
    """Split camelCase, snake_case and kebab-case names into lowercase words."""
    spaced = re.sub(r"([a-z0-9])([A-Z])", r"\1 \2", tool_name)
    return [w for w in re.split(r"[^A-Za-z0-9]+", spaced.lower()) if w]


def classify(tool_name: str) -> str:
    """Return "read", "blocked" or "write" for a tool name.

    The first verb in the name decides, so ``jira_get_link_types`` is a read
    while ``addCommentToJiraIssue`` is a write. A name with no known verb
    (``atlassianUserInfo``) is a read.
    """
    words = _words(tool_name)
    for w in words:
        if w.startswith(_BLOCKED_VERBS):
            return "blocked"
        if w.startswith(_READ_PREFIXES):
            return "read"
        if w.startswith(_WRITE_VERBS):
            if "confluence" in words or "page" in words:
                return "blocked"
            return "write"
    return "read"


def _project_of(value: Any) -> str | None:
    if isinstance(value, str) and value.strip():
        return value.strip().upper()
    if isinstance(value, dict):
        for k in ("key", "id"):
            if isinstance(value.get(k), str):
                return value[k].strip().upper()
    return None


def _targets(args: dict[str, Any]) -> tuple[list[str], list[str]]:
    """Collect (projects, issue refs) named anywhere a write might name them."""
    projects: list[str] = []
    issues: list[str] = []
    scopes = [args]
    if isinstance(args.get("fields"), dict):
        scopes.append(args["fields"])
    for scope in scopes:
        for k in _PROJECT_KEYS:
            if k in scope:
                p = _project_of(scope[k])
                if p:
                    projects.append(p)
        for k in _ISSUE_KEYS:
            v = scope.get(k)
            if isinstance(v, (str, int)) and str(v).strip():
                issues.append(str(v).strip().upper())
    return projects, issues


def check(tool_name: str, args: dict[str, Any], project_key: str) -> str | None:
    """Return why a call is refused, or None when it may proceed."""
    kind = classify(tool_name)
    if kind == "read":
        return None
    if kind == "blocked":
        return (
            f"{tool_name} is not permitted for the helpdesk agent: it may not delete "
            "or remove anything, and it may not change Confluence."
        )

    projects, issues = _targets(args or {})
    for p in projects:
        if p != project_key:
            return f"Writes are limited to Jira project {project_key}; got project {p}."
    for ref in issues:
        m = _ISSUE_KEY.match(ref)
        if not m:
            return (
                f"Refer to the issue by its full key ({project_key}- followed by "
                f"the number), not {ref!r}, so the project can be checked."
            )
        if m.group(1) != project_key:
            return f"Writes are limited to Jira project {project_key}; {ref} is not in it."
    if not projects and not issues:
        return (
            f"{tool_name} did not say which project or issue it changes. Pass project "
            f"{project_key} or the full key of an issue in it."
        )
    return None


Handler = Callable[[Any], Awaitable[Any]]


def jira_write_guard(project_key: str) -> Callable[[Any, Handler], Awaitable[Any]]:
    """Build an MCP tool-call interceptor enforcing the rules above."""

    async def interceptor(request: Any, handler: Handler) -> Any:
        if request.server_name != "jira":
            return await handler(request)
        reason = check(request.name, request.args, project_key)
        if reason is None:
            return await handler(request)
        log.warning("Blocked Jira call %s: %s", request.name, reason)
        return CallToolResult(
            content=[TextContent(type="text", text=f"BLOCKED: {reason}")],
            isError=True,
        )

    return interceptor
