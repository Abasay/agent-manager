from __future__ import annotations

import asyncio
import logging
import uuid
from typing import Any

from fastapi import FastAPI, HTTPException
from langchain_core.messages import AIMessage, HumanMessage, ToolMessage
from pydantic import BaseModel

from agent import build_agent, load_mcp_tools
from config import Config

logging.basicConfig(level=logging.INFO)
log = logging.getLogger("it-helpdesk")

CONFIG = Config.from_env()

MCP_TOOLS = asyncio.run(load_mcp_tools(CONFIG))

AGENT = build_agent(CONFIG, MCP_TOOLS)
log.info(
    "IT helpdesk agent ready (company=%s, version=%s, llm_provider=%s, mcp_tools=%d)",
    CONFIG.company_name,
    CONFIG.agent_version,
    "agent-manager" if CONFIG.use_llm_provider else "openai-direct",
    len(MCP_TOOLS),
)


class ChatRequest(BaseModel):
    message: str
    session_id: str | None = None
    context: dict[str, Any] | None = None


class ChatResponse(BaseModel):
    response: str
    session_id: str | None = None
    agent_version: str | None = None


app = FastAPI(title="IT Helpdesk Agent", version="0.2.0")


@app.get("/health")
def health() -> dict[str, Any]:
    return {
        "status": "ok",
        "company": CONFIG.company_name,
        "agent_version": CONFIG.agent_version,
        "mcp_enabled": CONFIG.use_mcp,
        "mcp_tool_count": len(MCP_TOOLS),
        "data_mode": CONFIG.data_mode,
    }


async def _close_dangling_tool_calls(config: dict[str, Any]) -> None:
    """Give every unanswered tool call in the thread an error result.

    If a turn fails between the model asking for a tool and the tool answering,
    the saved conversation keeps a tool call with no result, and the model API
    rejects every later turn of that session (INVALID_CHAT_HISTORY). Closing the
    call lets the conversation carry on.
    """
    state = await AGENT.aget_state(config)
    messages = (state.values or {}).get("messages", [])
    answered = {m.tool_call_id for m in messages if isinstance(m, ToolMessage)}
    dangling = [
        call
        for m in messages
        if isinstance(m, AIMessage)
        for call in (m.tool_calls or [])
        if call["id"] not in answered
    ]
    if not dangling:
        return
    log.warning("Closing %d unanswered tool call(s) in this session", len(dangling))
    await AGENT.aupdate_state(
        config,
        {
            "messages": [
                ToolMessage(
                    content="ERROR: this call did not complete.",
                    tool_call_id=call["id"],
                    name=call["name"],
                )
                for call in dangling
            ]
        },
        as_node="tools",
    )


@app.post("/chat", response_model=ChatResponse)
async def chat(req: ChatRequest) -> ChatResponse:
    # The checkpointer keys conversation state on thread_id. A caller that omits
    # session_id gets a fresh one, so each such request is its own conversation.
    session_id = req.session_id or str(uuid.uuid4())
    config = {"configurable": {"thread_id": session_id}}

    try:
        await _close_dangling_tool_calls(config)
        # ainvoke, not invoke: MCP tools are async-only, and LangGraph runs the
        # in-process sync tools in a threadpool either way.
        result = await AGENT.ainvoke(
            {"messages": [HumanMessage(content=req.message)]},
            config=config,
        )
    except Exception as exc:  # noqa: BLE001
        log.exception("agent invocation failed")
        raise HTTPException(status_code=500, detail=str(exc)) from exc

    final: Any = None
    for m in reversed(result.get("messages", [])):
        if isinstance(m, AIMessage):
            final = m.content
            break
    if final is None:
        final = "(no response)"
    if isinstance(final, list):
        final = "\n".join(
            part.get("text", "") if isinstance(part, dict) else str(part) for part in final
        )
    return ChatResponse(
        response=str(final),
        session_id=session_id,
        agent_version=CONFIG.agent_version,
    )
