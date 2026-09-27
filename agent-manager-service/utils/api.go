// Copyright (c) 2026, WSO2 LLC. (https://www.wso2.com).
//
// WSO2 LLC. licenses this file to you under the Apache License,
// Version 2.0 (the "License"); you may not use this file except
// in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing,
// software distributed under the License is distributed on an
// "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
// KIND, either express or implied.  See the License for the
// specific language governing permissions and limitations
// under the License.

package utils

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
)

// CreateAPIYamlZip creates a ZIP file containing API YAML files
// Compatible with api-platform's implementation
func CreateAPIYamlZip(apiYamlMap map[string]string) ([]byte, error) {
	return createYamlZip("api-", apiYamlMap)
}

// CreateLLMProviderYamlZip creates a ZIP file containing LLM provider YAML files
func CreateLLMProviderYamlZip(providerYamlMap map[string]string) ([]byte, error) {
	return createYamlZip("llm-provider-", providerYamlMap)
}

// CreateLLMProxyYamlZip creates a ZIP file containing LLM proxy YAML files
func CreateLLMProxyYamlZip(proxyYamlMap map[string]string) ([]byte, error) {
	return createYamlZip("llm-proxy-", proxyYamlMap)
}

// CreateMCPProxyYamlZip creates a ZIP file containing MCP proxy YAML files.
func CreateMCPProxyYamlZip(proxyYamlMap map[string]string) ([]byte, error) {
	return createYamlZip("mcp-proxy-", proxyYamlMap)
}

// CreateAgentYamlZip creates a ZIP file containing A2A Agent YAML files.
func CreateAgentYamlZip(agentYamlMap map[string]string) ([]byte, error) {
	return createYamlZip("agent-", agentYamlMap)
}

// createYamlZip writes each YAML as <prefix><id>.yaml at the zip root.
func createYamlZip(fileNamePrefix string, yamlMap map[string]string) ([]byte, error) {
	var buf bytes.Buffer
	zipWriter := zip.NewWriter(&buf)

	for id, yamlContent := range yamlMap {
		fileWriter, err := zipWriter.Create(fmt.Sprintf("%s%s.yaml", fileNamePrefix, id))
		if err != nil {
			return nil, closeAfterZipError(zipWriter, fmt.Errorf("failed to create file in zip: %w", err))
		}
		if _, err := fileWriter.Write([]byte(yamlContent)); err != nil {
			return nil, closeAfterZipError(zipWriter, fmt.Errorf("failed to write file content: %w", err))
		}
	}

	if err := zipWriter.Close(); err != nil {
		return nil, fmt.Errorf("failed to close zip writer: %w", err)
	}
	return buf.Bytes(), nil
}

func closeAfterZipError(zipWriter *zip.Writer, err error) error {
	if closeErr := zipWriter.Close(); closeErr != nil {
		return errors.Join(err, fmt.Errorf("close error: %w", closeErr))
	}
	return err
}
