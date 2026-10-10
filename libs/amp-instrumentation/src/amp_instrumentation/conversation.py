# Copyright (c) 2026, WSO2 LLC. (https://www.wso2.com).
#
# WSO2 LLC. licenses this file to you under the Apache License,
# Version 2.0 (the "License"); you may not use this file except
# in compliance with the License.
# You may obtain a copy of the License at
#
# http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing,
# software distributed under the License is distributed on an
# "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
# KIND, either express or implied.  See the License for the
# specific language governing permissions and limitations
# under the License.

"""
Copies a trace's conversation ID onto its local root span.

agent-manager-observer reads a trace's conversation ID from the root span first
and falls back to the chain and LLM spans only when the root has none, which
costs extra upstream calls per trace. Frameworks such as LangGraph put the
thread on the spans they create, not on a server span or manual agent span that
wraps them, so that root carries no ID.

``ConversationIdSpanProcessor`` reads the ID from each span that ends and, while
the trace's local root is still open, sets ``gen_ai.conversation.id`` on it:

- Keys are read in the observer's order (``CONVERSATION_ID_KEYS``). The first
  non-empty string, or int as its decimal string, is the ID; bools and floats
  are ignored.
- A root the observer already reads an ID from, a whole-number float included,
  is left alone, and the first ID copied is never overwritten. The observer reads
  ``gen_ai.conversation.id`` before ``session.id``, so adding it to such a root
  could change the answer.
- The root ends after its children, so the attribute is in place when it is
  exported. A child that ends after the root changes nothing.

Limitations:

- A local root with a remote parent is not the trace's root, which lives in
  another process. It gets the attribute anyway, but the observer's root read
  does not see it; the trace's own root still has to carry the ID.
- Concurrent local roots of one trace share a slot: the first one tracked gets
  the ID.
"""

import logging
import threading
import weakref
from typing import Mapping, Optional

from opentelemetry.sdk.trace import ReadableSpan, Span, SpanProcessor

logger = logging.getLogger(__name__)

CONVERSATION_ID_KEY = "gen_ai.conversation.id"

# Same keys, same order as conversationIDKeys in agent-manager-observer/opensearch/process.go.
CONVERSATION_ID_KEYS = (
    CONVERSATION_ID_KEY,
    "session.id",
    "langfuse.session.id",
    "traceloop.association.properties.session_id",
    "traceloop.association.properties.conversation_id",
    "traceloop.association.properties.thread_id",
    "langsmith.metadata.session_id",
    "langsmith.metadata.thread_id",
)


def _conversation_id(attributes: Mapping) -> Optional[str]:
    """Return the first usable conversation ID in ``attributes``, or None."""
    for key in CONVERSATION_ID_KEYS:
        value = attributes.get(key)
        if isinstance(value, str):
            if value:
                return value
        elif isinstance(value, int) and not isinstance(value, bool):
            return str(value)
    return None


def _root_has_id(attributes: Mapping) -> bool:
    """Whether the observer reads an ID from the root; it also takes whole-number floats."""
    if _conversation_id(attributes) is not None:
        return True
    return any(
        isinstance(value, float) and value.is_integer()
        for value in (attributes.get(key) for key in CONVERSATION_ID_KEYS)
    )


class ConversationIdSpanProcessor(SpanProcessor):
    """Copies a conversation ID from ended spans onto their trace's open local root."""

    def __init__(self) -> None:
        # Weak, so a root that never ends and is dropped by the app can't pin an entry.
        self._roots: "weakref.WeakValueDictionary[int, Span]" = (
            weakref.WeakValueDictionary()
        )
        self._lock = threading.Lock()

    def on_start(self, span, parent_context=None) -> None:
        parent = span.parent
        if parent is not None and not parent.is_remote:
            return
        with self._lock:
            self._roots.setdefault(span.context.trace_id, span)

    def on_end(self, span: ReadableSpan) -> None:
        # The SDK doesn't guard processors, so an error here would reach span.end().
        try:
            self._on_end(span)
        except Exception:
            logger.debug("Could not copy the conversation ID", exc_info=True)

    def _on_end(self, span: ReadableSpan) -> None:
        context = span.context
        with self._lock:
            root = self._roots.get(context.trace_id)
            if root is None:
                return
            # on_end gets a snapshot of the span, so the root is matched by span ID.
            if root.get_span_context().span_id == context.span_id:
                self._roots.pop(context.trace_id, None)
                return
            if not root.is_recording() or _root_has_id(root.attributes or {}):
                return
            conversation_id = _conversation_id(span.attributes or {})
            if conversation_id is not None:
                root.set_attribute(CONVERSATION_ID_KEY, conversation_id)


# Providers that already carry the processor; Traceloop and init_otel() can both reach one.
_registered: "weakref.WeakSet[object]" = weakref.WeakSet()
_registered_lock = threading.Lock()


def add_conversation_id_processor(provider) -> bool:
    """Add the processor to ``provider`` once; False if it can't take one or already has it."""
    if not hasattr(provider, "add_span_processor"):
        logger.debug(
            "Tracer provider takes no span processors; conversation ID copy skipped"
        )
        return False
    with _registered_lock:
        if provider in _registered:
            return False
        provider.add_span_processor(ConversationIdSpanProcessor())
        _registered.add(provider)
    return True
