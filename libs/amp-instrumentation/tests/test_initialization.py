# Copyright (c) 2025, WSO2 LLC. (https://www.wso2.com).
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

"""Tests for instrumentation initialization logic."""

import os
import pytest
from opentelemetry import trace
from opentelemetry.sdk.trace import TracerProvider
from amp_instrumentation._bootstrap import initialization
from amp_instrumentation._bootstrap import constants as env_vars
from amp_instrumentation.conversation import ConversationIdSpanProcessor


class TestGetRequiredEnvVar:
    """Test the _get_required_env_var helper function."""

    def test_missing_variable_raises_error(self, clean_environment):
        """Test that missing variable raises ConfigurationError."""
        with pytest.raises(initialization.ConfigurationError) as exc_info:
            initialization._get_required_env_var("MISSING_VAR")
        assert "MISSING_VAR" in str(exc_info.value)
        assert "is required but not set" in str(exc_info.value)

    def test_empty_variable_raises_error(self, clean_environment):
        """Test that empty variable raises ConfigurationError."""
        os.environ["EMPTY_VAR"] = ""
        with pytest.raises(initialization.ConfigurationError) as exc_info:
            initialization._get_required_env_var("EMPTY_VAR")
        assert "EMPTY_VAR" in str(exc_info.value)

    def test_whitespace_only_variable_raises_error(self, clean_environment):
        """Test that whitespace-only variable raises ConfigurationError."""
        os.environ["WHITESPACE_VAR"] = "   "
        with pytest.raises(initialization.ConfigurationError) as exc_info:
            initialization._get_required_env_var("WHITESPACE_VAR")
        assert "WHITESPACE_VAR" in str(exc_info.value)


class TestInitializeInstrumentation:
    """Test the initialize_instrumentation function."""

    def test_successful_initialization(self, clean_environment, mock_traceloop):
        """Test successful initialization with all required env vars."""
        # Set required environment variables
        os.environ[env_vars.AMP_OTEL_ENDPOINT] = "https://otel.example.com"
        os.environ[env_vars.AMP_AGENT_API_KEY] = "test-key"

        # Reset initialization state
        initialization._initialized = False

        # Call initialization
        initialization.initialize_instrumentation()

        # Verify Traceloop was initialized
        assert mock_traceloop.initialized is True
        assert mock_traceloop.init_kwargs["api_endpoint"] == "https://otel.example.com"
        assert mock_traceloop.init_kwargs["headers"]["x-amp-api-key"] == "test-key"

    def test_initialization_with_version(self, clean_environment, mock_traceloop):
        """Test initialization with agent version resource attribute."""
        # Set required environment variables plus version
        os.environ[env_vars.AMP_OTEL_ENDPOINT] = "https://otel.example.com"
        os.environ[env_vars.AMP_AGENT_API_KEY] = "test-key"
        os.environ[env_vars.AMP_AGENT_VERSION] = "1.2.3"

        # Reset initialization state
        initialization._initialized = False

        # Call initialization
        initialization.initialize_instrumentation()

        # Verify Traceloop was initialized with version resource attribute
        assert mock_traceloop.initialized is True
        assert "resource_attributes" in mock_traceloop.init_kwargs
        assert (
            mock_traceloop.init_kwargs["resource_attributes"][
                "agent-manager/agent-version"
            ]
            == "1.2.3"
        )

    def test_initialization_without_version(self, clean_environment, mock_traceloop):
        """Test initialization without agent version (optional)."""
        # Set required environment variables (no version)
        os.environ[env_vars.AMP_OTEL_ENDPOINT] = "https://otel.example.com"
        os.environ[env_vars.AMP_AGENT_API_KEY] = "test-key"

        # Reset initialization state
        initialization._initialized = False

        # Call initialization
        initialization.initialize_instrumentation()

        # Verify Traceloop was initialized with empty resource attributes
        assert mock_traceloop.initialized is True
        assert "resource_attributes" in mock_traceloop.init_kwargs
        assert mock_traceloop.init_kwargs["resource_attributes"] == {}


class TestConversationIdProcessor:
    """Test that the auto path registers the conversation ID processor."""

    def test_registered_once_on_traceloops_provider_after_init(
        self, clean_environment, mock_traceloop, monkeypatch
    ):
        """Test the processor is added to the global provider once, after Traceloop.init()."""
        os.environ[env_vars.AMP_OTEL_ENDPOINT] = "https://otel.example.com"
        os.environ[env_vars.AMP_AGENT_API_KEY] = "test-key"

        provider = TracerProvider()
        added = []
        add_span_processor = provider.add_span_processor

        def spy(processor):
            added.append((processor, mock_traceloop.initialized))
            add_span_processor(processor)

        monkeypatch.setattr(provider, "add_span_processor", spy)
        monkeypatch.setattr(trace, "get_tracer_provider", lambda: provider)

        for _ in range(2):
            monkeypatch.setattr(initialization, "_initialized", False)
            initialization.initialize_instrumentation()

        assert [type(p) for p, _ in added] == [ConversationIdSpanProcessor]
        assert added[0][1] is True
        assert "processor" not in mock_traceloop.init_kwargs

    def test_provider_without_span_processors_is_skipped(
        self, clean_environment, mock_traceloop, monkeypatch
    ):
        """Test init still succeeds when Traceloop left no SDK provider behind."""
        os.environ[env_vars.AMP_OTEL_ENDPOINT] = "https://otel.example.com"
        os.environ[env_vars.AMP_AGENT_API_KEY] = "test-key"
        monkeypatch.setattr(trace, "get_tracer_provider", trace.ProxyTracerProvider)
        monkeypatch.setattr(initialization, "_initialized", False)

        initialization.initialize_instrumentation()

        assert mock_traceloop.initialized is True
