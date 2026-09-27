//go:build integration

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

package repositories

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wso2/agent-manager/agent-manager-service/db"
	"github.com/wso2/agent-manager/agent-manager-service/models"
)

func newTestPublication(agentName string) *models.A2APublication {
	return &models.A2APublication{
		OUID:            "ou-" + uuid.New().String()[:8],
		ProjectName:     "checkout",
		AgentName:       agentName,
		EnvironmentName: "Development",
		EnvironmentUUID: uuid.New(),
		ArtifactUUID:    uuid.New(),
	}
}

func cleanupPublication(t *testing.T, repo A2APublicationRepository, pub *models.A2APublication) {
	t.Helper()
	t.Cleanup(func() {
		_ = repo.DeleteForAgent(context.Background(), pub.OUID, pub.ProjectName, pub.AgentName)
	})
}

// A redeploy must re-publish, and a pair that exhausted its budget must get
// another chance — so a second enqueue resets the existing row rather than
// adding a second one for the same pair.
func TestA2APublicationEnqueueResetsTheExistingRow(t *testing.T) {
	repo := NewA2APublicationRepository(db.GetDB())
	ctx := context.Background()

	pub := newTestPublication("trip-planner-" + uuid.New().String()[:8])
	cleanupPublication(t, repo, pub)
	require.NoError(t, repo.Enqueue(ctx, pub))
	require.NoError(t, repo.MarkFailed(ctx, *pub, "gave up"))

	requeued := newTestPublication(pub.AgentName)
	requeued.OUID = pub.OUID
	require.NoError(t, repo.Enqueue(ctx, requeued))

	due, err := repo.FindDue(ctx, time.Now(), 100)
	require.NoError(t, err)

	var found []models.A2APublication
	for _, row := range due {
		if row.AgentName == pub.AgentName {
			found = append(found, row)
		}
	}
	require.Len(t, found, 1, "one row per agent-environment pair, not one per deploy")
	assert.Equal(t, models.A2APublicationStatusPending, found[0].Status)
	assert.Equal(t, 0, found[0].AttemptCount, "the attempt budget is fresh")
	assert.Empty(t, found[0].LastError)
	assert.Equal(t, requeued.ArtifactUUID, found[0].ArtifactUUID, "the newest deploy's artifact wins")
}

// A row whose retry is scheduled for later must not be handed out until then;
// otherwise the backoff has no effect and the reconciler spins.
func TestA2APublicationFindDueRespectsBackoff(t *testing.T) {
	repo := NewA2APublicationRepository(db.GetDB())
	ctx := context.Background()

	pub := newTestPublication("backoff-" + uuid.New().String()[:8])
	cleanupPublication(t, repo, pub)
	require.NoError(t, repo.Enqueue(ctx, pub))
	require.NoError(t, repo.MarkAttemptFailed(ctx, *pub, "binding not ready", time.Now().Add(time.Hour)))

	due, err := repo.FindDue(ctx, time.Now(), 100)
	require.NoError(t, err)
	for _, row := range due {
		assert.NotEqual(t, pub.AgentName, row.AgentName, "a backed-off row is not due yet")
	}

	later, err := repo.FindDue(ctx, time.Now().Add(2*time.Hour), 100)
	require.NoError(t, err)
	var attempts int
	for _, row := range later {
		if row.AgentName == pub.AgentName {
			attempts = row.AttemptCount
		}
	}
	assert.Equal(t, 1, attempts, "the failed attempt was counted")
}

// A published row is done: leaving it due would republish the same agent on
// every tick forever.
func TestA2APublicationMarkPublishedRemovesItFromTheQueue(t *testing.T) {
	repo := NewA2APublicationRepository(db.GetDB())
	ctx := context.Background()

	pub := newTestPublication("published-" + uuid.New().String()[:8])
	cleanupPublication(t, repo, pub)
	require.NoError(t, repo.Enqueue(ctx, pub))
	require.NoError(t, repo.MarkPublished(ctx, *pub, "http://agent:9099"))

	due, err := repo.FindDue(ctx, time.Now(), 100)
	require.NoError(t, err)
	for _, row := range due {
		assert.NotEqual(t, pub.AgentName, row.AgentName, "a published row is no longer due")
	}
}

// dueRowFor returns the due row for agentName, as the reconciler would have read it.
func dueRowFor(t *testing.T, repo A2APublicationRepository, agentName string) models.A2APublication {
	t.Helper()
	due, err := repo.FindDue(context.Background(), time.Now(), 100)
	require.NoError(t, err)
	for _, row := range due {
		if row.AgentName == agentName {
			return row
		}
	}
	require.FailNow(t, "no due row", "agent %s", agentName)
	return models.A2APublication{}
}

func statusOf(t *testing.T, pub *models.A2APublication) models.A2APublication {
	t.Helper()
	var row models.A2APublication
	require.NoError(t, db.GetDB().Where("id = ?", pub.ID).First(&row).Error)
	return row
}

// A settings save that re-enqueues while the reconciler is publishing the
// config it read earlier must not be swallowed: the reconciler published the
// old config, so the row has to stay pending for the next tick to pick up the
// new one.
func TestA2APublicationMarkPublishedDoesNotSwallowANewerEnqueue(t *testing.T) {
	repo := NewA2APublicationRepository(db.GetDB())
	ctx := context.Background()

	pub := newTestPublication("race-published-" + uuid.New().String()[:8])
	cleanupPublication(t, repo, pub)
	require.NoError(t, repo.Enqueue(ctx, pub))
	read := dueRowFor(t, repo, pub.AgentName)

	requeue := *pub
	require.NoError(t, repo.Enqueue(ctx, &requeue))
	require.ErrorIs(t, repo.MarkPublished(ctx, read, "http://agent:9099"), ErrA2APublicationSuperseded)

	assert.Equal(t, models.A2APublicationStatusPending, statusOf(t, pub).Status,
		"the newer enqueue is still owed a publish")
}

// A retry recorded against a row that was re-enqueued meanwhile would push a
// fresh publication's first attempt 30s out and charge it an attempt it never
// made.
func TestA2APublicationMarkAttemptFailedDoesNotTouchANewerEnqueue(t *testing.T) {
	repo := NewA2APublicationRepository(db.GetDB())
	ctx := context.Background()

	pub := newTestPublication("race-retry-" + uuid.New().String()[:8])
	cleanupPublication(t, repo, pub)
	require.NoError(t, repo.Enqueue(ctx, pub))
	read := dueRowFor(t, repo, pub.AgentName)

	requeue := *pub
	require.NoError(t, repo.Enqueue(ctx, &requeue))
	require.ErrorIs(t, repo.MarkAttemptFailed(ctx, read, "binding not ready", time.Now().Add(time.Hour)), ErrA2APublicationSuperseded)

	row := statusOf(t, pub)
	assert.Equal(t, 0, row.AttemptCount, "the fresh attempt budget is untouched")
	require.NotNil(t, row.NextAttemptAt)
	assert.False(t, row.NextAttemptAt.After(time.Now()), "and it is still due now")
}

// Giving up on a row that was re-enqueued meanwhile would fail a publication
// that has not had a single attempt.
func TestA2APublicationMarkFailedDoesNotFailANewerEnqueue(t *testing.T) {
	repo := NewA2APublicationRepository(db.GetDB())
	ctx := context.Background()

	pub := newTestPublication("race-failed-" + uuid.New().String()[:8])
	cleanupPublication(t, repo, pub)
	require.NoError(t, repo.Enqueue(ctx, pub))
	read := dueRowFor(t, repo, pub.AgentName)

	requeue := *pub
	require.NoError(t, repo.Enqueue(ctx, &requeue))
	require.ErrorIs(t, repo.MarkFailed(ctx, read, "gave up"), ErrA2APublicationSuperseded)

	assert.Equal(t, models.A2APublicationStatusPending, statusOf(t, pub).Status,
		"the newer enqueue is still owed a publish")
}

// Drift checks compare against what the gateway was last given, and a later
// redeploy's Enqueue must not erase it.
func TestA2APublicationMarkPublishedRecordsTheUpstream(t *testing.T) {
	repo := NewA2APublicationRepository(db.GetDB())
	ctx := context.Background()

	pub := newTestPublication("upstream-" + uuid.New().String()[:8])
	cleanupPublication(t, repo, pub)
	require.NoError(t, repo.Enqueue(ctx, pub))
	require.NoError(t, repo.MarkPublished(ctx, *pub, "http://agent:9099"))
	assert.Equal(t, "http://agent:9099", statusOf(t, pub).PublishedUpstreamURL)

	requeue := *pub
	require.NoError(t, repo.Enqueue(ctx, &requeue))
	assert.Equal(t, "http://agent:9099", statusOf(t, pub).PublishedUpstreamURL)
}

// Waiting on a binding is not charged against the attempt budget.
func TestA2APublicationMarkWaitingDoesNotCountAnAttempt(t *testing.T) {
	repo := NewA2APublicationRepository(db.GetDB())
	ctx := context.Background()

	pub := newTestPublication("waiting-" + uuid.New().String()[:8])
	cleanupPublication(t, repo, pub)
	require.NoError(t, repo.Enqueue(ctx, pub))
	require.NoError(t, repo.MarkWaiting(ctx, *pub, "binding not ready", time.Now().Add(time.Hour)))

	row := statusOf(t, pub)
	assert.Equal(t, 0, row.AttemptCount)
	assert.Equal(t, models.A2APublicationStatusPending, row.Status)
	require.NotNil(t, row.NextAttemptAt)
	assert.True(t, row.NextAttemptAt.After(time.Now()), "the next check is backed off")
}

// A drifted published row goes back on the queue with a fresh budget.
func TestA2APublicationRequeueMakesAPublishedRowDue(t *testing.T) {
	repo := NewA2APublicationRepository(db.GetDB())
	ctx := context.Background()

	pub := newTestPublication("requeue-" + uuid.New().String()[:8])
	cleanupPublication(t, repo, pub)
	require.NoError(t, repo.Enqueue(ctx, pub))
	require.NoError(t, repo.MarkPublished(ctx, *pub, "http://agent:9099"))
	published := statusOf(t, pub)

	require.NoError(t, repo.Requeue(ctx, published))

	row := dueRowFor(t, repo, pub.AgentName)
	assert.Equal(t, models.A2APublicationStatusPending, row.Status)
	assert.Equal(t, 0, row.AttemptCount)
	require.ErrorIs(t, repo.Requeue(ctx, published), ErrA2APublicationSuperseded,
		"a stale read does not requeue twice")
}

// FindPublished pages in id order and returns only published rows.
func TestA2APublicationFindPublishedPagesPublishedRows(t *testing.T) {
	repo := NewA2APublicationRepository(db.GetDB())
	ctx := context.Background()

	published := newTestPublication("paged-pub-" + uuid.New().String()[:8])
	pending := newTestPublication("paged-pending-" + uuid.New().String()[:8])
	cleanupPublication(t, repo, published)
	cleanupPublication(t, repo, pending)
	require.NoError(t, repo.Enqueue(ctx, published))
	require.NoError(t, repo.Enqueue(ctx, pending))
	require.NoError(t, repo.MarkPublished(ctx, *published, "http://agent:9099"))

	var seen []models.A2APublication
	cursor := uuid.Nil
	for {
		page, err := repo.FindPublished(ctx, cursor, 2)
		require.NoError(t, err)
		for i := 1; i < len(page); i++ {
			assert.Less(t, page[i-1].ID.String(), page[i].ID.String(), "id order")
		}
		seen = append(seen, page...)
		if len(page) < 2 {
			break
		}
		cursor = page[len(page)-1].ID
	}

	var names []string
	for _, row := range seen {
		assert.Equal(t, models.A2APublicationStatusPublished, row.Status)
		names = append(names, row.AgentName)
	}
	assert.Contains(t, names, published.AgentName)
	assert.NotContains(t, names, pending.AgentName)
}
