/**
 * Copyright (c) 2026, WSO2 LLC. (https://www.wso2.com).
 *
 * WSO2 LLC. licenses this file to you under the Apache License,
 * Version 2.0 (the "License"); you may not use this file except
 * in compliance with the License.
 * You may obtain a copy of the License at
 *
 * http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing,
 * software distributed under the License is distributed on an
 * "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
 * KIND, either express or implied.  See the License for the
 * specific language governing permissions and limitations
 * under the License.
 */

import { globalConfig } from "@agent-management-platform/types";

/**
 * Fallback gateway version when `globalConfig.gatewayVersion` is not injected
 * (e.g. local dev without a runtime config). Released builds have the real
 * version substituted at deploy time.
 */
export const DEFAULT_GATEWAY_VERSION = "v2026.09.24";

/** The configured gateway version, e.g. "v0.15.0" (carries a leading "v"). */
export function getGatewayVersion(): string {
  return globalConfig.gatewayVersion?.trim() || DEFAULT_GATEWAY_VERSION;
}

/** Gateway version without the leading "v", for contexts that need bare semver. */
export function getGatewayVersionHelm(): string {
  const v = getGatewayVersion();
  return v.startsWith("v") ? v.slice(1) : v;
}

/**
 * The configured Agent Manager release version (e.g. "v0.15.0"). The deployment
 * scripts live in the Agent Manager repo and are tagged with its release, so the
 * script ref is pinned to this version — not the gateway chart version. Empty
 * when not injected (dev), which falls back to `main`.
 */
export function getAmpVersion(): string {
  return globalConfig.ampVersion?.trim() || "";
}

/**
 * Agent Manager release version without the leading "v", for contexts that need
 * bare semver — e.g. the `CHART_VERSION` passed to the gateway-extension OCI chart
 * (OCI tags are plain semver like "0.17.0-rc4", not "v0.17.0-rc4"). Empty when the
 * version is not injected (dev).
 */
export function getAmpVersionHelm(): string {
  const v = getAmpVersion();
  return v.startsWith("v") ? v.slice(1) : v;
}

/**
 * Git ref used to fetch deployment scripts from raw.githubusercontent.com.
 * Release tags are `amp/vX.Y.Z`, so a versioned build pins to that tag. An unset
 * or dev placeholder version (e.g. `0.0.0-dev`) has no matching tag, so fall back
 * to `main`. The leading "v" is normalized so both "v0.15.0" and "0.15.0" work.
 */
export function getScriptRef(): string {
  const version = getAmpVersion();
  if (!version || version.includes("dev")) return "main";
  const bare = version.startsWith("v") ? version.slice(1) : version;
  return `amp/v${bare}`;
}

/**
 * Base URL the generated commands fetch deployment scripts from, without a
 * trailing slash. Defaults to the public raw.githubusercontent.com path for the
 * current release ref; `globalConfig.scriptBaseUrl` overrides it for a
 * deployment where that host is not reachable or serves nothing.
 */
export function getScriptBaseUrl(): string {
  const configured = globalConfig.scriptBaseUrl?.trim();
  if (configured) return configured.replace(/\/+$/, "");
  return `https://raw.githubusercontent.com/wso2/agent-manager/${getScriptRef()}/deployments/scripts`;
}

/** Full URL for a deployment script, pinned to the current release ref. */
export function getRawScriptUrl(scriptName: string): string {
  return `${getScriptBaseUrl()}/${scriptName}`;
}

const DEFAULT_GATEWAY_CONTROL_PLANE_URL = "http://localhost:9243";

export function getGatewayControlPlaneUrl(): string {
  const url = globalConfig.gatewayControlPlaneUrl?.trim() || globalConfig.apiBaseUrl?.trim();
  return url || DEFAULT_GATEWAY_CONTROL_PLANE_URL;
}

const DEFAULT_AGENT_MANAGER_URL = "http://api.amp.localhost:8080";

/** Slash stripped because the scripts append /api/v1. */
export function getAgentManagerUrl(): string {
  const url = globalConfig.apiBaseUrl?.trim().replace(/\/+$/, "");
  return url || DEFAULT_AGENT_MANAGER_URL;
}

export function getGatewayEnvFile(): string {
  return `wso2apip-ai-gateway-${getGatewayVersionHelm()}/configs/keys.env`;
}

export function getConfigureGatewayDisplayCommand(registrationToken: string | null): string {
  const controlPlaneHost = new URL(getGatewayControlPlaneUrl());
  const tokenValue = registrationToken || "<your-gateway-token>";
  return `cat > "${getGatewayEnvFile()}" << 'ENVFILE'
GATEWAY_CONTROLPLANE_HOST=${controlPlaneHost}
GATEWAY_REGISTRATION_TOKEN=${tokenValue}
ENVFILE`;
}

export function getK8sGatewayHelmCommand(
  registrationToken: string | null,
  verb: "install" | "upgrade" = "install",
): string {
  const controlPlaneHost = new URL(getGatewayControlPlaneUrl()).hostname;
  const tokenValue = registrationToken || "your-gateway-token";
  return `helm ${verb} gateway oci://ghcr.io/wso2/api-platform/helm-charts/gateway --version ${getGatewayVersionHelm()} \\
  --set gateway.controller.controlPlane.host="${controlPlaneHost}" \\
  --set gateway.controller.controlPlane.port=443 \\
  --set gateway.controller.controlPlane.token.value="${tokenValue}" \\
  --set gateway.config.analytics.enabled=true`;
}
