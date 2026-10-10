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

"""Tests for the conversation ID span processor."""

import gc
import threading
import time
from concurrent.futures import ThreadPoolExecutor
from typing import Generator

import pytest
from opentelemetry import trace
from opentelemetry.sdk.trace import TracerProvider
from opentelemetry.sdk.trace.export import SimpleSpanProcessor
from opentelemetry.sdk.trace.export.in_memory_span_exporter import (
    InMemorySpanExporter,
)
from opentelemetry.trace import SpanKind
from opentelemetry.trace.propagation.tracecontext import TraceContextTextMapPropagator

from amp_instrumentation.conversation import (
    ConversationIdSpanProcessor,
    add_conversation_id_processor,
)

KEY = "gen_ai.conversation.id"
THREAD_ID = "traceloop.association.properties.thread_id"

# The observer's order (conversationIDKeys in agent-manager-observer/opensearch/process.go).
OBSERVER_KEYS = [
    "gen_ai.conversation.id",
    "session.id",
    "langfuse.session.id",
    "traceloop.association.properties.session_id",
    "traceloop.association.properties.conversation_id",
    "traceloop.association.properties.thread_id",
    "langsmith.metadata.session_id",
    "langsmith.metadata.thread_id",
]

TRACEPARENT = "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"


@pytest.fixture
def processor() -> ConversationIdSpanProcessor:
    return ConversationIdSpanProcessor()


@pytest.fixture
def span_exporter() -> InMemorySpanExporter:
    return InMemorySpanExporter()


@pytest.fixture
def tracer(processor, span_exporter) -> Generator[trace.Tracer, None, None]:
    """A tracer on an SDK provider exporting to memory, with the processor installed."""
    provider = TracerProvider()
    provider.add_span_processor(SimpleSpanProcessor(span_exporter))
    provider.add_span_processor(processor)
    yield provider.get_tracer("test")
    provider.shutdown()


def child_of(tracer, parent, name="child"):
    return tracer.start_span(name, context=trace.set_span_in_context(parent))


def exported(span_exporter, name) -> dict:
    """Attributes of the exported span called ``name``."""
    (span,) = [s for s in span_exporter.get_finished_spans() if s.name == name]
    return dict(span.attributes)


def finish_child(tracer, parent, attributes) -> None:
    """Start a child of ``parent``, set ``attributes`` after it starts, and end it."""
    child = child_of(tracer, parent)
    for key, value in attributes.items():
        child.set_attribute(key, value)
    child.end()


def remote_context():
    return TraceContextTextMapPropagator().extract({"traceparent": TRACEPARENT})


class TestCopy:
    """The ID reaches the exported root."""

    def test_id_set_on_child_after_it_starts(self, tracer, span_exporter):
        root = tracer.start_span("POST /chat", kind=SpanKind.SERVER)
        child = child_of(tracer, root, "LangGraph.workflow")
        child.set_attribute(THREAD_ID, "t-1")
        child.end()
        root.end()

        assert exported(span_exporter, "POST /chat")[KEY] == "t-1"

    def test_id_from_a_nested_descendant(self, tracer, span_exporter):
        root = tracer.start_span("POST /chat")
        chain = child_of(tracer, root, "chain")
        llm = child_of(tracer, chain, "llm")
        llm.set_attribute(THREAD_ID, "t-deep")
        llm.end()
        chain.end()
        root.end()

        assert exported(span_exporter, "POST /chat")[KEY] == "t-deep"

    def test_int_thread_id_becomes_a_decimal_string(self, tracer, span_exporter):
        root = tracer.start_span("root")
        child = child_of(tracer, root)
        child.set_attribute(THREAD_ID, 556)
        child.end()
        root.end()

        assert exported(span_exporter, "root")[KEY] == "556"

    def test_child_with_gen_ai_conversation_id_is_copied(self, tracer, span_exporter):
        root = tracer.start_span("root")
        child = child_of(tracer, root)
        child.set_attribute(KEY, "adk-1")
        child.end()
        root.end()

        assert exported(span_exporter, "root")[KEY] == "adk-1"

    @pytest.mark.parametrize("index", range(len(OBSERVER_KEYS)))
    def test_keys_are_read_in_observer_order(self, tracer, span_exporter, index):
        root = tracer.start_span("root")
        child = child_of(tracer, root)
        for key in OBSERVER_KEYS[index:]:
            child.set_attribute(key, "from-" + key)
        child.end()
        root.end()

        assert exported(span_exporter, "root")[KEY] == "from-" + OBSERVER_KEYS[index]

    def test_empty_string_falls_through_to_the_next_key(self, tracer, span_exporter):
        root = tracer.start_span("root")
        child = child_of(tracer, root)
        child.set_attribute(KEY, "")
        child.set_attribute("session.id", "s-1")
        child.end()
        root.end()

        assert exported(span_exporter, "root")[KEY] == "s-1"

    @pytest.mark.parametrize("value", [True, False, 1.0, 556.0, 3.5])
    def test_bools_and_floats_are_ignored(self, tracer, span_exporter, value):
        root = tracer.start_span("root")
        child = child_of(tracer, root)
        child.set_attribute(THREAD_ID, value)
        child.end()
        root.end()

        assert KEY not in exported(span_exporter, "root")

    def test_bool_falls_through_to_the_next_key(self, tracer, span_exporter):
        root = tracer.start_span("root")
        child = child_of(tracer, root)
        child.set_attribute(KEY, True)
        child.set_attribute("session.id", "s-2")
        child.end()
        root.end()

        assert exported(span_exporter, "root")[KEY] == "s-2"

    def test_no_id_on_any_span(self, tracer, span_exporter):
        root = tracer.start_span("root")
        finish_child(tracer, root, {"some.other": "x"})
        root.end()

        assert not any(k in exported(span_exporter, "root") for k in OBSERVER_KEYS)

    def test_first_id_found_wins(self, tracer, span_exporter):
        root = tracer.start_span("root")
        finish_child(tracer, root, {"session.id": "first"})
        finish_child(tracer, root, {"session.id": "second"})
        root.end()

        assert exported(span_exporter, "root")[KEY] == "first"


class TestRootLeftAlone:
    """A root that already names a conversation is never touched."""

    def test_existing_gen_ai_conversation_id_is_unchanged(self, tracer, span_exporter):
        root = tracer.start_span("root")
        root.set_attribute(KEY, "root-id")
        finish_child(tracer, root, {"session.id": "child-id"})
        root.end()

        assert exported(span_exporter, "root")[KEY] == "root-id"

    @pytest.mark.parametrize("key", OBSERVER_KEYS[1:])
    def test_any_other_root_key_blocks_the_copy(self, tracer, span_exporter, key):
        root = tracer.start_span("root")
        root.set_attribute(key, "root-id")
        child = child_of(tracer, root)
        child.set_attribute(KEY, "child-id")
        child.end()
        root.end()

        attributes = exported(span_exporter, "root")
        assert attributes[key] == "root-id"
        assert KEY not in attributes

    def test_empty_root_key_does_not_block_the_copy(self, tracer, span_exporter):
        root = tracer.start_span("root")
        root.set_attribute("session.id", "")
        finish_child(tracer, root, {THREAD_ID: "child-id"})
        root.end()

        assert exported(span_exporter, "root")[KEY] == "child-id"

    def test_whole_float_root_key_blocks_the_copy(self, tracer, span_exporter):
        root = tracer.start_span("root")
        root.set_attribute("session.id", 556.0)
        finish_child(tracer, root, {THREAD_ID: "child-id"})
        root.end()

        attributes = exported(span_exporter, "root")
        assert attributes["session.id"] == 556.0
        assert KEY not in attributes

    def test_root_that_is_no_longer_recording_is_skipped(
        self, tracer, processor, monkeypatch
    ):
        root = tracer.start_span("root")
        set_calls = []
        monkeypatch.setattr(root, "is_recording", lambda: False)
        monkeypatch.setattr(root, "set_attribute", lambda *args: set_calls.append(args))
        finish_child(tracer, root, {"session.id": "s-1"})

        assert set_calls == []

    def test_child_ending_after_the_root_changes_nothing(self, tracer, span_exporter):
        root = tracer.start_span("root")
        late = child_of(tracer, root)
        late.set_attribute(THREAD_ID, "late-id")
        root.end()
        late.end()

        assert KEY not in exported(span_exporter, "root")

    def test_span_started_after_the_root_ended_is_not_a_root(
        self, tracer, processor, span_exporter
    ):
        root = tracer.start_span("root")
        root.end()
        late = child_of(tracer, root, "late")
        finish_child(tracer, late, {"session.id": "s-1"})
        late.end()

        assert KEY not in exported(span_exporter, "late")
        assert len(processor._roots) == 0


class TestRemoteParent:
    """A local root whose parent lives in another process."""

    def test_attribute_is_set_on_a_server_root_with_a_remote_parent(
        self, tracer, span_exporter
    ):
        root = tracer.start_span(
            "POST /chat", context=remote_context(), kind=SpanKind.SERVER
        )
        assert root.parent.is_remote
        finish_child(tracer, root, {THREAD_ID: "t-9"})
        root.end()

        assert exported(span_exporter, "POST /chat")[KEY] == "t-9"

    def test_second_local_root_of_a_trace_does_not_evict_the_first(
        self, tracer, span_exporter
    ):
        first = tracer.start_span("first", context=remote_context())
        second = tracer.start_span("second", context=remote_context())
        second.end()
        finish_child(tracer, first, {"session.id": "s-1"})
        first.end()

        assert exported(span_exporter, "first")[KEY] == "s-1"
        assert KEY not in exported(span_exporter, "second")


class TestTracking:
    """What the processor holds on to."""

    def test_map_is_empty_after_every_root_has_ended(self, tracer, processor):
        local = tracer.start_span("local")
        remote = tracer.start_span("remote", context=remote_context())
        finish_child(tracer, local, {"session.id": "a"})
        finish_child(tracer, remote, {"session.id": "b"})
        assert len(processor._roots) == 2

        local.end()
        remote.end()

        assert len(processor._roots) == 0

    def test_roots_that_never_end_are_not_held(self, tracer, processor):
        root = tracer.start_span("never-ends")
        assert len(processor._roots) == 1

        del root
        gc.collect()

        assert len(processor._roots) == 0

    def test_interleaved_traces_across_threads_keep_their_own_ids(
        self, tracer, span_exporter
    ):
        roots_open = threading.Barrier(2)
        ids_set = threading.Barrier(2)

        def run(name, thread_id):
            with tracer.start_as_current_span(name):
                roots_open.wait(timeout=5)
                with tracer.start_as_current_span("child") as child:
                    child.set_attribute(THREAD_ID, thread_id)
                    ids_set.wait(timeout=5)

        with ThreadPoolExecutor(max_workers=2) as pool:
            futures = [pool.submit(run, "a", "id-a"), pool.submit(run, "b", "id-b")]
            for future in futures:
                future.result()

        assert exported(span_exporter, "a")[KEY] == "id-a"
        assert exported(span_exporter, "b")[KEY] == "id-b"

    def test_children_ending_together_set_the_id_once(self, tracer, monkeypatch):
        root = tracer.start_span("root")
        set_calls = []
        set_attribute = root.set_attribute

        def slow_set_attribute(key, value):
            set_calls.append(value)
            time.sleep(0.05)
            set_attribute(key, value)

        monkeypatch.setattr(root, "set_attribute", slow_set_attribute)
        children = []
        for value in ("a", "b"):
            child = child_of(tracer, root)
            child.set_attribute("session.id", value)
            children.append(child)
        end_together = threading.Barrier(2)

        def end(child):
            end_together.wait(timeout=5)
            child.end()

        with ThreadPoolExecutor(max_workers=2) as pool:
            for future in [pool.submit(end, child) for child in children]:
                future.result()

        assert len(set_calls) == 1


class TestSafety:
    def test_failure_to_set_the_attribute_does_not_reach_the_caller(
        self, tracer, span_exporter, monkeypatch
    ):
        root = tracer.start_span("root")

        def fail(*args):
            raise RuntimeError("boom")

        monkeypatch.setattr(root, "set_attribute", fail)
        finish_child(tracer, root, {"session.id": "s-1"})
        root.end()

        assert KEY not in exported(span_exporter, "root")


class TestAddConversationIdProcessor:
    def test_adds_one_processor_per_provider(self, record_processors):
        provider = TracerProvider()
        added = record_processors(provider)

        assert add_conversation_id_processor(provider) is True
        assert add_conversation_id_processor(provider) is False
        assert [type(p) for p in added] == [ConversationIdSpanProcessor]

    def test_each_provider_gets_its_own(self, record_processors):
        first, second = TracerProvider(), TracerProvider()
        added_first = record_processors(first)
        added_second = record_processors(second)

        add_conversation_id_processor(first)
        add_conversation_id_processor(second)

        assert len(added_first) == 1
        assert len(added_second) == 1
        assert added_first[0] is not added_second[0]

    def test_provider_that_takes_no_processors_is_skipped(self):
        assert add_conversation_id_processor(trace.ProxyTracerProvider()) is False
