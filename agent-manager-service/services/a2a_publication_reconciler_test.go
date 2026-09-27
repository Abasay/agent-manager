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

package services

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/wso2/agent-manager/agent-manager-service/clients/clientmocks"
	"github.com/wso2/agent-manager/agent-manager-service/models"
	"github.com/wso2/agent-manager/agent-manager-service/repositories"
	"github.com/wso2/agent-manager/agent-manager-service/repositories/repomocks"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func pendingPublication() models.A2APublication {
	return models.A2APublication{
		ID:              uuid.New(),
		OUID:            "org-1",
		ProjectName:     "checkout",
		AgentName:       "trip-planner",
		EnvironmentName: "dev",
		EnvironmentUUID: uuid.New(),
		ArtifactUUID:    uuid.New(),
		Status:          models.A2APublicationStatusPending,
		UpdatedAt:       time.Date(2026, 9, 24, 10, 0, 0, 123456000, time.UTC),
	}
}

// a2aReconcilerHarness holds the reconciler and the mocks behind it, so each
// test can assert on what the publication attempt did rather than only on what
// it returned.
type a2aReconcilerHarness struct {
	svc            *a2aPublicationReconcilerService
	gatewayRepo    *repomocks.GatewayRepositoryMock
	hub            *recordingEventHub
	pubRepo        *repomocks.A2APublicationRepositoryMock
	deploymentRepo *repomocks.DeploymentRepositoryMock
	ocClient       *clientmocks.OpenChoreoClientMock
}

// newA2AReconcilerHarness wires a reconciler whose every dependency succeeds and
// whose binding reports serviceURL. Tests narrow it to the condition they are
// about.
func newA2AReconcilerHarness(serviceURL string) *a2aReconcilerHarness {
	hub := &recordingEventHub{}
	pubRepo := &repomocks.A2APublicationRepositoryMock{
		MarkPublishedFunc: func(ctx context.Context, read models.A2APublication, upstreamURL string) error { return nil },
		MarkAttemptFailedFunc: func(ctx context.Context, read models.A2APublication, lastErr string, nextAttemptAt time.Time) error {
			return nil
		},
		MarkWaitingFunc: func(ctx context.Context, read models.A2APublication, lastErr string, nextAttemptAt time.Time) error {
			return nil
		},
		MarkFailedFunc: func(ctx context.Context, read models.A2APublication, lastErr string) error { return nil },
	}
	deploymentRepo := &repomocks.DeploymentRepositoryMock{
		CreateWithLimitEnforcementFunc: func(deployment *models.Deployment, maxDeployments int) error { return nil },
	}
	ocClient := &clientmocks.OpenChoreoClientMock{
		GetReleaseBindingServiceURLFunc: func(ctx context.Context, ouID, componentName, environment string) (string, error) {
			return serviceURL, nil
		},
	}
	gatewayRepo := &repomocks.GatewayRepositoryMock{
		ListWithFiltersFunc: func(opts repositories.GatewayFilterOptions) ([]*models.Gateway, error) {
			return []*models.Gateway{{
				UUID:                     uuid.New(),
				Name:                     "ingress-dev",
				Vhost:                    "agents.example.com",
				GatewayFunctionalityType: models.GatewayRoleIngress,
			}}, nil
		},
	}
	agentConfigRepo := &repomocks.AgentConfigRepositoryMock{
		GetFunc: func(ctx context.Context, ouID, projectName, agentName, environmentName string) (*models.AgentConfig, error) {
			return &models.AgentConfig{EnableApiKeySecurity: true, CORSEnabled: true}, nil
		},
	}

	return &a2aReconcilerHarness{
		hub:            hub,
		gatewayRepo:    gatewayRepo,
		pubRepo:        pubRepo,
		deploymentRepo: deploymentRepo,
		ocClient:       ocClient,
		svc: &a2aPublicationReconcilerService{
			pubRepo:         pubRepo,
			deploymentRepo:  deploymentRepo,
			gatewayRepo:     gatewayRepo,
			agentConfigRepo: agentConfigRepo,
			ocClient:        ocClient,
			events:          NewGatewayEventsService(hub),
			logger:          testLogger(),
		},
	}
}

// The whole reason this reconciler exists: status is populated only after the
// binding reconciles, so an early attempt must wait rather than emit an Agent
// that routes nowhere.
func TestReconcilerWaitsWhenServiceURLIsNotReadyYet(t *testing.T) {
	h := newA2AReconcilerHarness("")

	h.svc.publishOne(context.Background(), pendingPublication())

	assert.Empty(t, h.deploymentRepo.CreateWithLimitEnforcementCalls(),
		"nothing is published until the binding reports an upstream")
	assert.Empty(t, h.hub.published, "and no gateway is told about it")

	waits := h.pubRepo.MarkWaitingCalls()
	require.Len(t, waits, 1)
	assert.True(t, waits[0].NextAttemptAt.After(time.Now()), "the retry is scheduled forward")
	assert.Empty(t, h.pubRepo.MarkAttemptFailedCalls(), "waiting is not charged an attempt")
	assert.Empty(t, h.pubRepo.MarkFailedCalls())
}

// A first source build can take far longer than the attempt budget; waiting on
// it must never fail the row.
func TestReconcilerNeverGivesUpWaitingForTheFirstBinding(t *testing.T) {
	h := newA2AReconcilerHarness("")

	pub := pendingPublication()
	pub.AttemptCount = a2aPublicationAttemptBudget - 1
	h.svc.publishOne(context.Background(), pub)

	assert.Len(t, h.pubRepo.MarkWaitingCalls(), 1)
	assert.Empty(t, h.pubRepo.MarkFailedCalls())
}

// A retry must be due by the next tick, not land just after it and skip one.
func TestReconcilerRetryLandsOnTheNextTick(t *testing.T) {
	h := newA2AReconcilerHarness("http://trip-planner.dp-default:9099")
	h.svc.gatewayRepo = &repomocks.GatewayRepositoryMock{
		ListWithFiltersFunc: func(opts repositories.GatewayFilterOptions) ([]*models.Gateway, error) {
			return []*models.Gateway{}, nil
		},
	}

	before := time.Now()
	h.svc.publishOne(context.Background(), pendingPublication())

	retries := h.pubRepo.MarkAttemptFailedCalls()
	require.Len(t, retries, 1)
	assert.True(t, retries[0].NextAttemptAt.Before(before.Add(a2aReconcilerTickInterval)),
		"due before the next tick fires")
}

// Past the budget a real failure (here: no ingress gateway) is called failed
// rather than retried forever.
func TestReconcilerGivesUpPastTheAttemptBudget(t *testing.T) {
	h := newA2AReconcilerHarness("http://trip-planner.dp-default:9099")
	h.svc.gatewayRepo = &repomocks.GatewayRepositoryMock{
		ListWithFiltersFunc: func(opts repositories.GatewayFilterOptions) ([]*models.Gateway, error) {
			return []*models.Gateway{}, nil
		},
	}

	exhausted := pendingPublication()
	exhausted.AttemptCount = a2aPublicationAttemptBudget - 1
	h.svc.publishOne(context.Background(), exhausted)

	require.Len(t, h.pubRepo.MarkFailedCalls(), 1)
	assert.Empty(t, h.pubRepo.MarkAttemptFailedCalls(), "the retry cycle has ended")
	assert.Empty(t, h.deploymentRepo.CreateWithLimitEnforcementCalls())
}

// The ingress-role filter belongs in the query, scoped to the org.
func TestReconcilerQueriesOnlyIngressGatewaysOfTheOrg(t *testing.T) {
	h := newA2AReconcilerHarness("http://trip-planner.dp-default:9099")

	pub := pendingPublication()
	h.svc.publishOne(context.Background(), pub)

	calls := h.gatewayRepo.ListWithFiltersCalls()
	require.Len(t, calls, 1)
	assert.Equal(t, pub.OUID, calls[0].Filters.OrganizationID)
	assert.Equal(t, models.IngressGatewayRoles, calls[0].Filters.FunctionalityTypeIn)
	require.NotNil(t, calls[0].Filters.EnvironmentID)
	assert.Equal(t, pub.EnvironmentUUID.String(), *calls[0].Filters.EnvironmentID)
}

// The happy path: a ready binding produces a deployments row carrying the Agent
// YAML and an agent.deployed broadcast keyed on the artifact UUID.
func TestReconcilerPublishesOnceServiceURLIsAvailable(t *testing.T) {
	const upstream = "http://trip-planner.dp-default:9099"
	h := newA2AReconcilerHarness(upstream)

	pub := pendingPublication()
	h.svc.publishOne(context.Background(), pub)

	created := h.deploymentRepo.CreateWithLimitEnforcementCalls()
	require.Len(t, created, 1)
	assert.Equal(t, pub.ArtifactUUID, created[0].Deployment.ArtifactUUID)

	var published A2AAgentDeploymentYAML
	require.NoError(t, yaml.Unmarshal(created[0].Deployment.Content, &published))
	assert.Equal(t, kindA2AAgent, published.Kind)
	assert.Equal(t, upstream, published.Spec.Upstream.URL)

	require.Len(t, h.hub.published, 1)
	evt := h.hub.published[0]
	assert.Equal(t, "agent.deployed", string(evt.EventType))
	assert.Equal(t, pub.ArtifactUUID.String(), evt.EntityID,
		"the gateway fetches /agents/{agentId} by the artifact UUID")

	var envelope struct {
		Payload struct {
			DeploymentID string `json:"deploymentId"`
		} `json:"payload"`
	}
	require.NoError(t, json.Unmarshal([]byte(evt.EventData), &envelope))
	assert.Equal(t, created[0].Deployment.DeploymentID.String(), envelope.Payload.DeploymentID,
		"the event points at the row the gateway will fetch")

	marked := h.pubRepo.MarkPublishedCalls()
	require.Len(t, marked, 1)
	assert.Equal(t, pub, marked[0].Read,
		"the row is marked as it was read, so a re-enqueue meanwhile is not swallowed")
	assert.Equal(t, upstream, marked[0].UpstreamURL, "the published upstream is recorded for drift checks")
	assert.Empty(t, h.pubRepo.MarkAttemptFailedCalls())
}

// The reconciler is the only place an A2A agent's gateway resource is built, so
// it is where A2A-Version is guaranteed and the card's CORS is attached.
func TestReconcilerPublishesA2AVersionAndInheritedCardCORS(t *testing.T) {
	h := newA2AReconcilerHarness("http://trip-planner.dp-default:9099")
	h.svc.agentConfigRepo = &repomocks.AgentConfigRepositoryMock{
		GetFunc: func(ctx context.Context, ouID, projectName, agentName, environmentName string) (*models.AgentConfig, error) {
			return &models.AgentConfig{
				CORSEnabled:      true,
				CORSAllowOrigins: []string{"https://client.example"},
				CORSAllowMethods: []string{"GET", "POST", "OPTIONS"},
				CORSAllowHeaders: []string{"Content-Type"},
			}, nil
		},
	}

	h.svc.publishOne(context.Background(), pendingPublication())

	created := h.deploymentRepo.CreateWithLimitEnforcementCalls()
	require.Len(t, created, 1)
	var published A2AAgentDeploymentYAML
	require.NoError(t, yaml.Unmarshal(created[0].Deployment.Content, &published))

	opCORS := published.Spec.A2A.OperationConfigs.Policies[0]
	assert.Equal(t, "cors", opCORS["name"])
	assert.Contains(t, opCORS["params"].(map[string]interface{})["allowedHeaders"], "A2A-Version")

	require.NotNil(t, published.Spec.A2A.AgentCard, "an inherited card follows enabled agent CORS")
	cardCORS := published.Spec.A2A.AgentCard.Public.Policies[0]["params"].(map[string]interface{})
	assert.Equal(t, []interface{}{"https://client.example"}, cardCORS["allowedOrigins"])
	assert.Equal(t, []interface{}{"GET", "OPTIONS"}, cardCORS["allowedMethods"])
}

func publishedPublication(upstreamURL string) models.A2APublication {
	pub := pendingPublication()
	pub.Status = models.A2APublicationStatusPublished
	pub.PublishedUpstreamURL = upstreamURL
	return pub
}

func (h *a2aReconcilerHarness) withPublished(rows ...models.A2APublication) {
	h.pubRepo.FindPublishedFunc = func(ctx context.Context, afterID uuid.UUID, limit int) ([]models.A2APublication, error) {
		return rows, nil
	}
	h.pubRepo.RequeueFunc = func(ctx context.Context, read models.A2APublication) error { return nil }
}

// A new release with a changed port moves the Service; the gateway must follow.
func TestDriftCheckRequeuesWhenTheUpstreamMoved(t *testing.T) {
	h := newA2AReconcilerHarness("http://trip-planner.dp-default:8080")
	stale := publishedPublication("http://trip-planner.dp-default:9099")
	h.withPublished(stale)

	h.svc.checkUpstreamDrift(context.Background())

	requeued := h.pubRepo.RequeueCalls()
	require.Len(t, requeued, 1)
	assert.Equal(t, stale, requeued[0].Read)
}

func TestDriftCheckLeavesAMatchingUpstreamAlone(t *testing.T) {
	const upstream = "http://trip-planner.dp-default:9099"
	h := newA2AReconcilerHarness(upstream)
	h.withPublished(publishedPublication(upstream))

	h.svc.checkUpstreamDrift(context.Background())

	assert.Empty(t, h.pubRepo.RequeueCalls())
}

// An unreadable or empty binding URL is not evidence of drift; requeueing
// would only park a working agent in the waiting state.
func TestDriftCheckIgnoresAnUnknownCurrentUpstream(t *testing.T) {
	h := newA2AReconcilerHarness("")
	h.withPublished(publishedPublication("http://trip-planner.dp-default:9099"))
	h.svc.checkUpstreamDrift(context.Background())
	assert.Empty(t, h.pubRepo.RequeueCalls())

	h.ocClient.GetReleaseBindingServiceURLFunc = func(ctx context.Context, ouID, componentName, environment string) (string, error) {
		return "", assert.AnError
	}
	h.svc.checkUpstreamDrift(context.Background())
	assert.Empty(t, h.pubRepo.RequeueCalls())
}

// The scan pages through published rows across ticks and wraps at the end, so
// every row is checked without any tick doing unbounded work.
func TestDriftCheckPagesThroughPublishedRowsAcrossTicks(t *testing.T) {
	h := newA2AReconcilerHarness("http://trip-planner.dp-default:9099")
	page := make([]models.A2APublication, a2aDriftCheckBatch)
	for i := range page {
		page[i] = publishedPublication("http://trip-planner.dp-default:9099")
	}
	var cursors []uuid.UUID
	h.pubRepo.FindPublishedFunc = func(ctx context.Context, afterID uuid.UUID, limit int) ([]models.A2APublication, error) {
		cursors = append(cursors, afterID)
		assert.Equal(t, a2aDriftCheckBatch, limit)
		if afterID == uuid.Nil {
			return page, nil
		}
		return []models.A2APublication{}, nil
	}

	h.svc.checkUpstreamDrift(context.Background())
	h.svc.checkUpstreamDrift(context.Background())
	h.svc.checkUpstreamDrift(context.Background())

	require.Len(t, cursors, 3)
	assert.Equal(t, uuid.Nil, cursors[0])
	assert.Equal(t, page[len(page)-1].ID, cursors[1], "the next tick resumes after the last row checked")
	assert.Equal(t, uuid.Nil, cursors[2], "a short page wraps the scan back to the start")
}
