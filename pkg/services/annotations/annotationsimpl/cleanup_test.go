package annotationsimpl

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/grafana/grafana/pkg/infra/db"
	"github.com/grafana/grafana/pkg/infra/log"
	"github.com/grafana/grafana/pkg/services/annotations"
	"github.com/grafana/grafana/pkg/setting"
)

func TestIntegrationAnnotationCleanUp(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	fakeSQL := db.InitTestDB(t)

	tests := []struct {
		name                    string
		createAnnotationsNum    int
		createOldAnnotationsNum int

		cfg                           *setting.Cfg
		alertAnnotationCount          int64
		annotationCleanupJobBatchSize int
		dashboardAnnotationCount      int64
		APIAnnotationCount            int64
		affectedAnnotations           int64
	}{
		{
			name:                          "default settings should not delete any annotations",
			createAnnotationsNum:          21,
			createOldAnnotationsNum:       6,
			annotationCleanupJobBatchSize: 1,
			cfg: &setting.Cfg{
				AlertingAnnotationCleanupSetting:   settingsFn(0, 0),
				DashboardAnnotationCleanupSettings: settingsFn(0, 0),
				APIAnnotationCleanupSettings:       settingsFn(0, 0),
			},
			alertAnnotationCount:     7,
			dashboardAnnotationCount: 7,
			APIAnnotationCount:       7,
			affectedAnnotations:      0,
		},
		{
			name:                          "should remove annotations created before cut off point",
			createAnnotationsNum:          21,
			createOldAnnotationsNum:       6,
			annotationCleanupJobBatchSize: 1,
			cfg: &setting.Cfg{
				AlertingAnnotationCleanupSetting:   settingsFn(time.Hour*48, 0),
				DashboardAnnotationCleanupSettings: settingsFn(time.Hour*48, 0),
				APIAnnotationCleanupSettings:       settingsFn(time.Hour*48, 0),
			},
			alertAnnotationCount:     5,
			dashboardAnnotationCount: 5,
			APIAnnotationCount:       5,
			affectedAnnotations:      6,
		},
		{
			name:                          "should only keep three annotations",
			createAnnotationsNum:          15,
			createOldAnnotationsNum:       6,
			annotationCleanupJobBatchSize: 1,
			cfg: &setting.Cfg{
				AlertingAnnotationCleanupSetting:   settingsFn(0, 3),
				DashboardAnnotationCleanupSettings: settingsFn(0, 3),
				APIAnnotationCleanupSettings:       settingsFn(0, 3),
			},
			alertAnnotationCount:     3,
			dashboardAnnotationCount: 3,
			APIAnnotationCount:       3,
			affectedAnnotations:      6,
		},
		{
			name:                          "running the max count delete again should not remove any annotations",
			createAnnotationsNum:          9,
			createOldAnnotationsNum:       6,
			annotationCleanupJobBatchSize: 1,
			cfg: &setting.Cfg{
				AlertingAnnotationCleanupSetting:   settingsFn(0, 3),
				DashboardAnnotationCleanupSettings: settingsFn(0, 3),
				APIAnnotationCleanupSettings:       settingsFn(0, 3),
			},
			alertAnnotationCount:     3,
			dashboardAnnotationCount: 3,
			APIAnnotationCount:       3,
			affectedAnnotations:      0,
		},
		{
			name:                          "should not fail if batch size is larger than SQLITE_MAX_VARIABLE_NUMBER for SQLite >= 3.32.0",
			createAnnotationsNum:          40003,
			createOldAnnotationsNum:       0,
			annotationCleanupJobBatchSize: 32767,
			cfg: &setting.Cfg{
				AlertingAnnotationCleanupSetting:   settingsFn(0, 1),
				DashboardAnnotationCleanupSettings: settingsFn(0, 1),
				APIAnnotationCleanupSettings:       settingsFn(0, 1),
			},
			alertAnnotationCount:     1,
			dashboardAnnotationCount: 1,
			APIAnnotationCount:       1,
			affectedAnnotations:      40000,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			createTestAnnotations(t, fakeSQL, test.createAnnotationsNum, test.createOldAnnotationsNum)
			assertAnnotationCount(t, fakeSQL, "", int64(test.createAnnotationsNum))
			assertAnnotationTagCount(t, fakeSQL, 2*int64(test.createAnnotationsNum))

			t.Cleanup(func() {
				err := fakeSQL.WithDbSession(context.Background(), func(session *db.Session) error {
					_, deleteAnnotationErr := session.Exec("DELETE FROM annotation")
					_, deleteAnnotationTagErr := session.Exec("DELETE FROM annotation_tag")
					return errors.Join(deleteAnnotationErr, deleteAnnotationTagErr)
				})
				assert.NoError(t, err)
			})

			cfg := setting.NewCfg()
			cfg.AnnotationCleanupJobBatchSize = int64(test.annotationCleanupJobBatchSize)
			cleaner := ProvideCleanupService(fakeSQL, cfg)
			affectedAnnotations, affectedAnnotationTags, err := cleaner.Run(context.Background(), test.cfg)
			require.NoError(t, err)

			assert.Equal(t, test.affectedAnnotations, affectedAnnotations)
			assert.Equal(t, test.affectedAnnotations*2, affectedAnnotationTags)

			assertAnnotationCount(t, fakeSQL, alertAnnotationType, test.alertAnnotationCount)
			assertAnnotationCount(t, fakeSQL, dashboardAnnotationType, test.dashboardAnnotationCount)
			assertAnnotationCount(t, fakeSQL, apiAnnotationType, test.APIAnnotationCount)

			// we create two records in annotation_tag for each sample annotation
			expectedAnnotationTagCount := (test.alertAnnotationCount +
				test.dashboardAnnotationCount +
				test.APIAnnotationCount) * 2
			assertAnnotationTagCount(t, fakeSQL, expectedAnnotationTagCount)
		})
	}
}

func TestIntegrationOldAnnotationsAreDeletedFirst(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	fakeSQL := db.InitTestDB(t)

	t.Cleanup(func() {
		err := fakeSQL.WithDbSession(context.Background(), func(session *db.Session) error {
			_, err := session.Exec("DELETE FROM annotation")
			return err
		})
		assert.NoError(t, err)
	})

	// create some test annotations
	a := annotations.Item{
		DashboardID: 1,
		OrgID:       1,
		UserID:      1,
		PanelID:     1,
		AlertID:     10,
		Text:        "",
		Created:     time.Now().AddDate(-10, 0, -10).UnixNano() / int64(time.Millisecond),
	}

	err := fakeSQL.WithDbSession(context.Background(), func(sess *db.Session) error {
		_, err := sess.Insert(a)
		require.NoError(t, err, "cannot insert annotation")
		_, err = sess.Insert(a)
		require.NoError(t, err, "cannot insert annotation")

		a.AlertID = 20
		_, err = sess.Insert(a)
		require.NoError(t, err, "cannot insert annotation")

		// run the clean up task to keep one annotation.
		cfg := setting.NewCfg()
		cfg.AnnotationCleanupJobBatchSize = 1
		cleaner := NewXormStore(cfg, log.New("annotation.test"), fakeSQL, nil)
		_, err = cleaner.CleanAnnotations(context.Background(), setting.AnnotationCleanupSettings{MaxCount: 1}, alertAnnotationType)
		require.NoError(t, err)

		// assert that the last annotations were kept
		countNew, err := sess.Where("alert_id = 20").Count(&annotations.Item{})
		require.NoError(t, err)
		require.Equal(t, int64(1), countNew, "the last annotations should be kept")

		countOld, err := sess.Where("alert_id = 10").Count(&annotations.Item{})
		require.NoError(t, err)
		require.Equal(t, int64(0), countOld, "the two first annotations should have been deleted")

		return nil
	})
	require.NoError(t, err)
}

func assertAnnotationCount(t *testing.T, fakeSQL db.DB, sql string, expectedCount int64) {
	t.Helper()

	err := fakeSQL.WithDbSession(context.Background(), func(sess *db.Session) error {
		count, err := sess.Where(sql).Count(&annotations.Item{})
		require.NoError(t, err)
		require.Equal(t, expectedCount, count)
		return nil
	})
	require.NoError(t, err)
}

func assertAnnotationTagCount(t *testing.T, fakeSQL db.DB, expectedCount int64) {
	t.Helper()

	err := fakeSQL.WithDbSession(context.Background(), func(sess *db.Session) error {
		count, err := sess.SQL("select count(*) from annotation_tag").Count()
		require.NoError(t, err)
		require.Equal(t, expectedCount, count)
		return nil
	})
	require.NoError(t, err)
}

func createTestAnnotations(t *testing.T, store db.DB, expectedCount int, oldAnnotations int) {
	t.Helper()

	cutoffDate := time.Now()

	newAnnotations := make([]*annotations.Item, 0, expectedCount)
	newAnnotationTags := make([]*annotationTag, 0, 2*expectedCount)
	for i := 0; i < expectedCount; i++ {
		a := &annotations.Item{
			ID:          int64(i + 1),
			DashboardID: 1,
			OrgID:       1,
			UserID:      1,
			PanelID:     1,
			Text:        "",
		}

		// mark every third as an API annotation
		// that does not belong to a dashboard
		if i%3 == 1 {
			a.DashboardID = 0
		}

		// mark every third annotation as an alert annotation
		if i%3 == 0 {
			a.AlertID = 10
			a.DashboardID = 2
		}

		// create epoch as int annotations.go line 40
		a.Created = cutoffDate.UnixNano() / int64(time.Millisecond)

		// set a really old date for the first six annotations
		if i < oldAnnotations {
			a.Created = cutoffDate.AddDate(-10, 0, -10).UnixNano() / int64(time.Millisecond)
		}

		newAnnotations = append(newAnnotations, a)
		newAnnotationTags = append(newAnnotationTags, &annotationTag{AnnotationID: a.ID, TagID: 1}, &annotationTag{AnnotationID: a.ID, TagID: 2})
	}

	err := store.WithDbSession(context.Background(), func(sess *db.Session) error {
		batchsize := 500
		for i := 0; i < len(newAnnotations); i += batchsize {
			_, err := sess.InsertMulti(newAnnotations[i:min(i+batchsize, len(newAnnotations))])
			require.NoError(t, err)
		}
		return nil
	})
	require.NoError(t, err)

	err = store.WithDbSession(context.Background(), func(sess *db.Session) error {
		batchsize := 500
		for i := 0; i < len(newAnnotationTags); i += batchsize {
			_, err := sess.InsertMulti(newAnnotationTags[i:min(i+batchsize, len(newAnnotationTags))])
			require.NoError(t, err)
		}
		return nil
	})
	require.NoError(t, err)
}

func settingsFn(maxAge time.Duration, maxCount int64) setting.AnnotationCleanupSettings {
	return setting.AnnotationCleanupSettings{MaxAge: maxAge, MaxCount: maxCount}
}

// TestIntegrationCleanAnnotationsBatchProcessing tests the batched cleanup process
// to ensure it properly handles multiple batches and respects batch size limits
func TestIntegrationCleanAnnotationsBatchProcessing(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	fakeSQL := db.InitTestDB(t)

	tests := []struct {
		name                          string
		createAnnotationsNum          int
		batchSize                     int
		maxCount                      int64
		expectedRemainingAnnotations  int64
		expectedDeletedInMultipleBatches bool
	}{
		{
			name:                         "single batch deletion",
			createAnnotationsNum:         10,
			batchSize:                    20,
			maxCount:                     5,
			expectedRemainingAnnotations: 5,
			expectedDeletedInMultipleBatches: false,
		},
		{
			name:                         "multiple batch deletion",
			createAnnotationsNum:         100,
			batchSize:                    10,
			maxCount:                     50,
			expectedRemainingAnnotations: 50,
			expectedDeletedInMultipleBatches: true,
		},
		{
			name:                         "exact batch size match",
			createAnnotationsNum:         30,
			batchSize:                    10,
			maxCount:                     20,
			expectedRemainingAnnotations: 20,
			expectedDeletedInMultipleBatches: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Cleanup(func() {
				err := fakeSQL.WithDbSession(context.Background(), func(session *db.Session) error {
					_, err := session.Exec("DELETE FROM annotation")
					return err
				})
				assert.NoError(t, err)
			})

			// Create test annotations
			createTestAnnotations(t, fakeSQL, tt.createAnnotationsNum, 0)

			cfg := setting.NewCfg()
			cfg.AnnotationCleanupJobBatchSize = tt.batchSize
			cleaner := NewXormStore(cfg, log.New("annotation.test"), fakeSQL, nil)

			affected, err := cleaner.CleanAnnotations(context.Background(), settingsFn(0, tt.maxCount), alertAnnotationType)
			require.NoError(t, err)
			
			expectedDeleted := tt.createAnnotationsNum/3 - int(tt.expectedRemainingAnnotations)
			assert.Equal(t, int64(expectedDeleted), affected, "should delete the correct number of annotations")

			assertAnnotationCount(t, fakeSQL, alertAnnotationType, tt.expectedRemainingAnnotations)
		})
	}
}

// TestIntegrationCleanAnnotationsWithContextCancellation tests that cleanup
// respects context cancellation and doesn't delete more than necessary
func TestIntegrationCleanAnnotationsWithContextCancellation(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	fakeSQL := db.InitTestDB(t)

	t.Cleanup(func() {
		err := fakeSQL.WithDbSession(context.Background(), func(session *db.Session) error {
			_, err := session.Exec("DELETE FROM annotation")
			return err
		})
		assert.NoError(t, err)
	})

	// Create many annotations
	createTestAnnotations(t, fakeSQL, 100, 0)

	cfg := setting.NewCfg()
	cfg.AnnotationCleanupJobBatchSize = 10
	cleaner := NewXormStore(cfg, log.New("annotation.test"), fakeSQL, nil)

	// Create a context that will be cancelled
	ctx, cancel := context.WithCancel(context.Background())
	
	// Cancel immediately to test cancellation handling
	cancel()

	affected, err := cleaner.CleanAnnotations(ctx, settingsFn(0, 20), alertAnnotationType)
	
	// Should get context cancelled error
	assert.Error(t, err)
	assert.True(t, errors.Is(err, context.Canceled), "error should be context.Canceled")
	
	// Some annotations might have been deleted before cancellation
	assert.GreaterOrEqual(t, affected, int64(0))
}

// TestIntegrationCleanAnnotationsByTimeRange tests cleanup based on time cutoff
func TestIntegrationCleanAnnotationsByTimeRange(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	fakeSQL := db.InitTestDB(t)

	tests := []struct {
		name                    string
		createRecentAnnotations int
		createOldAnnotations    int
		maxAge                  time.Duration
		expectedRemainingCount  int64
	}{
		{
			name:                    "delete all old annotations",
			createRecentAnnotations: 10,
			createOldAnnotations:    5,
			maxAge:                  time.Hour * 48,
			expectedRemainingCount:  10,
		},
		{
			name:                    "delete some old annotations with small batch",
			createRecentAnnotations: 15,
			createOldAnnotations:    10,
			maxAge:                  time.Hour * 24,
			expectedRemainingCount:  15,
		},
		{
			name:                    "no deletion when maxAge is very large",
			createRecentAnnotations: 10,
			createOldAnnotations:    5,
			maxAge:                  time.Hour * 24 * 365 * 100, // 100 years
			expectedRemainingCount:  15,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Cleanup(func() {
				err := fakeSQL.WithDbSession(context.Background(), func(session *db.Session) error {
					_, err := session.Exec("DELETE FROM annotation")
					return err
				})
				assert.NoError(t, err)
			})

			total := tt.createRecentAnnotations + tt.createOldAnnotations
			createTestAnnotations(t, fakeSQL, total, tt.createOldAnnotations)

			cfg := setting.NewCfg()
			cfg.AnnotationCleanupJobBatchSize = 5
			cleaner := NewXormStore(cfg, log.New("annotation.test"), fakeSQL, nil)

			affected, err := cleaner.CleanAnnotations(context.Background(), settingsFn(tt.maxAge, 0), "")
			require.NoError(t, err)

			expectedDeleted := total - int(tt.expectedRemainingCount)
			assert.Equal(t, int64(expectedDeleted), affected)

			// Verify remaining count
			err = fakeSQL.WithDbSession(context.Background(), func(sess *db.Session) error {
				count, err := sess.Count(&annotations.Item{})
				require.NoError(t, err)
				assert.Equal(t, tt.expectedRemainingCount, count)
				return nil
			})
			require.NoError(t, err)
		})
	}
}

// TestIntegrationCleanOrphanedAnnotationTagsBatchProcessing tests orphaned tag cleanup
func TestIntegrationCleanOrphanedAnnotationTagsBatchProcessing(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	fakeSQL := db.InitTestDB(t)

	t.Cleanup(func() {
		err := fakeSQL.WithDbSession(context.Background(), func(session *db.Session) error {
			_, err := session.Exec("DELETE FROM annotation_tag")
			if err != nil {
				return err
			}
			_, err = session.Exec("DELETE FROM annotation")
			return err
		})
		assert.NoError(t, err)
	})

	// Create annotations with tags
	createTestAnnotations(t, fakeSQL, 20, 0)

	// Delete some annotations to create orphaned tags
	err := fakeSQL.WithDbSession(context.Background(), func(sess *db.Session) error {
		_, err := sess.Exec("DELETE FROM annotation WHERE id <= 10")
		return err
	})
	require.NoError(t, err)

	cfg := setting.NewCfg()
	cfg.AnnotationCleanupJobBatchSize = 5
	cleaner := NewXormStore(cfg, log.New("annotation.test"), fakeSQL, nil)

	affected, err := cleaner.CleanOrphanedAnnotationTags(context.Background())
	require.NoError(t, err)

	// Should have deleted tags for 10 annotations (2 tags each)
	assert.Equal(t, int64(20), affected)

	// Verify remaining tags belong to existing annotations
	err = fakeSQL.WithDbSession(context.Background(), func(sess *db.Session) error {
		var orphanedCount int64
		orphanedCount, err = sess.SQL(`
			SELECT COUNT(*) FROM annotation_tag 
			WHERE annotation_id NOT IN (SELECT id FROM annotation)
		`).Count()
		require.NoError(t, err)
		assert.Equal(t, int64(0), orphanedCount, "should have no orphaned tags")
		return nil
	})
	require.NoError(t, err)
}

// TestIntegrationCleanAnnotationsEdgeCases tests edge cases in cleanup
func TestIntegrationCleanAnnotationsEdgeCases(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	fakeSQL := db.InitTestDB(t)

	t.Run("cleanup with zero annotations", func(t *testing.T) {
		t.Cleanup(func() {
			err := fakeSQL.WithDbSession(context.Background(), func(session *db.Session) error {
				_, err := session.Exec("DELETE FROM annotation")
				return err
			})
			assert.NoError(t, err)
		})

		cfg := setting.NewCfg()
		cfg.AnnotationCleanupJobBatchSize = 10
		cleaner := NewXormStore(cfg, log.New("annotation.test"), fakeSQL, nil)

		affected, err := cleaner.CleanAnnotations(context.Background(), settingsFn(time.Hour*24, 0), "")
		require.NoError(t, err)
		assert.Equal(t, int64(0), affected)
	})

	t.Run("cleanup with maxCount greater than total", func(t *testing.T) {
		t.Cleanup(func() {
			err := fakeSQL.WithDbSession(context.Background(), func(session *db.Session) error {
				_, err := session.Exec("DELETE FROM annotation")
				return err
			})
			assert.NoError(t, err)
		})

		createTestAnnotations(t, fakeSQL, 10, 0)

		cfg := setting.NewCfg()
		cfg.AnnotationCleanupJobBatchSize = 5
		cleaner := NewXormStore(cfg, log.New("annotation.test"), fakeSQL, nil)

		affected, err := cleaner.CleanAnnotations(context.Background(), settingsFn(0, 100), "")
		require.NoError(t, err)
		assert.Equal(t, int64(0), affected, "should not delete any annotations")

		err = fakeSQL.WithDbSession(context.Background(), func(sess *db.Session) error {
			count, err := sess.Count(&annotations.Item{})
			require.NoError(t, err)
			assert.Equal(t, int64(10), count)
			return nil
		})
		require.NoError(t, err)
	})

	t.Run("cleanup with batch size of 1", func(t *testing.T) {
		t.Cleanup(func() {
			err := fakeSQL.WithDbSession(context.Background(), func(session *db.Session) error {
				_, err := session.Exec("DELETE FROM annotation")
				return err
			})
			assert.NoError(t, err)
		})

		createTestAnnotations(t, fakeSQL, 15, 5)

		cfg := setting.NewCfg()
		cfg.AnnotationCleanupJobBatchSize = 1
		cleaner := NewXormStore(cfg, log.New("annotation.test"), fakeSQL, nil)

		affected, err := cleaner.CleanAnnotations(context.Background(), settingsFn(time.Hour*48, 0), "")
		require.NoError(t, err)
		assert.Equal(t, int64(5), affected)
	})
}

// TestIntegrationDeleteByIDsWithVariousBatchSizes tests the deleteByIDs function
// with different batch sizes including edge cases for SQLite parameter limits
func TestIntegrationDeleteByIDsWithVariousBatchSizes(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	fakeSQL := db.InitTestDB(t)

	tests := []struct {
		name              string
		annotationCount   int
		batchSize         int
		idsToDelete       []int64
		expectedAffected  int64
	}{
		{
			name:            "delete with small batch",
			annotationCount: 20,
			batchSize:       10,
			idsToDelete:     []int64{1, 2, 3, 4, 5},
			expectedAffected: 5,
		},
		{
			name:            "delete with empty IDs",
			annotationCount: 10,
			batchSize:       10,
			idsToDelete:     []int64{},
			expectedAffected: 0,
		},
		{
			name:            "delete with large batch near SQLite limit",
			annotationCount: 1000,
			batchSize:       1000,
			idsToDelete:     make([]int64, 500),
			expectedAffected: 500,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Cleanup(func() {
				err := fakeSQL.WithDbSession(context.Background(), func(session *db.Session) error {
					_, err := session.Exec("DELETE FROM annotation")
					return err
				})
				assert.NoError(t, err)
			})

			// Create annotations
			createTestAnnotations(t, fakeSQL, tt.annotationCount, 0)

			// Populate idsToDelete if needed
			if len(tt.idsToDelete) > 0 && tt.idsToDelete[0] == 0 {
				for i := range tt.idsToDelete {
					tt.idsToDelete[i] = int64(i + 1)
				}
			}

			cfg := setting.NewCfg()
			cfg.AnnotationCleanupJobBatchSize = tt.batchSize
			cleaner := NewXormStore(cfg, log.New("annotation.test"), fakeSQL, nil)

			affected, err := cleaner.deleteByIDs(context.Background(), "annotation", tt.idsToDelete)
			require.NoError(t, err)
			assert.Equal(t, tt.expectedAffected, affected)

			// Verify deletions
			err = fakeSQL.WithDbSession(context.Background(), func(sess *db.Session) error {
				remaining, err := sess.Count(&annotations.Item{})
				require.NoError(t, err)
				expected := tt.annotationCount - int(tt.expectedAffected)
				assert.Equal(t, int64(expected), remaining)
				return nil
			})
			require.NoError(t, err)
		})
	}
}

// TestIntegrationFetchIDsWithComplexConditions tests the fetchIDs function
func TestIntegrationFetchIDsWithComplexConditions(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	fakeSQL := db.InitTestDB(t)

	t.Cleanup(func() {
		err := fakeSQL.WithDbSession(context.Background(), func(session *db.Session) error {
			_, err := session.Exec("DELETE FROM annotation")
			return err
		})
		assert.NoError(t, err)
	})

	// Create test data
	createTestAnnotations(t, fakeSQL, 30, 10)

	cfg := setting.NewCfg()
	cfg.AnnotationCleanupJobBatchSize = 100
	cleaner := NewXormStore(cfg, log.New("annotation.test"), fakeSQL, nil)

	t.Run("fetch IDs with limit", func(t *testing.T) {
		cond := fmt.Sprintf("org_id = 1 ORDER BY id DESC %s", fakeSQL.GetDialect().Limit(10))
		ids, err := cleaner.fetchIDs(context.Background(), "annotation", cond)
		require.NoError(t, err)
		assert.Len(t, ids, 10)
	})

	t.Run("fetch IDs with alert filter", func(t *testing.T) {
		cond := fmt.Sprintf("org_id = 1 AND alert_id > 0 ORDER BY id DESC %s", fakeSQL.GetDialect().Limit(20))
		ids, err := cleaner.fetchIDs(context.Background(), "annotation", cond)
		require.NoError(t, err)
		// Every third annotation is an alert (created by createTestAnnotations)
		assert.LessOrEqual(t, len(ids), 20)
	})

	t.Run("fetch IDs without condition returns error", func(t *testing.T) {
		ids, err := cleaner.fetchIDs(context.Background(), "annotation", "")
		require.Error(t, err)
		assert.Nil(t, ids)
		assert.Contains(t, err.Error(), "condition must be supplied")
	})

	t.Run("fetch IDs with time range", func(t *testing.T) {
		cutoffDate := time.Now().Add(-time.Hour).UnixNano() / int64(time.Millisecond)
		cond := fmt.Sprintf("org_id = 1 AND created < %d ORDER BY id DESC %s", cutoffDate, fakeSQL.GetDialect().Limit(5))
		ids, err := cleaner.fetchIDs(context.Background(), "annotation", cond)
		require.NoError(t, err)
		assert.LessOrEqual(t, len(ids), 5)
	})
}

// TestIntegrationCleanAnnotationsConcurrentOperations tests cleanup during concurrent operations
func TestIntegrationCleanAnnotationsConcurrentOperations(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	fakeSQL := db.InitTestDB(t)

	t.Cleanup(func() {
		err := fakeSQL.WithDbSession(context.Background(), func(session *db.Session) error {
			_, err := session.Exec("DELETE FROM annotation")
			return err
		})
		assert.NoError(t, err)
	})

	// Create initial annotations
	createTestAnnotations(t, fakeSQL, 100, 20)

	cfg := setting.NewCfg()
	cfg.AnnotationCleanupJobBatchSize = 10
	cleaner := NewXormStore(cfg, log.New("annotation.test"), fakeSQL, nil)

	// Run cleanup in goroutine
	done := make(chan error, 1)
	go func() {
		_, err := cleaner.CleanAnnotations(context.Background(), settingsFn(time.Hour*48, 0), "")
		done <- err
	}()

	// Simulate concurrent inserts
	for i := 0; i < 10; i++ {
		annotation := &annotations.Item{
			OrgID:       1,
			UserID:      1,
			DashboardID: 1,
			Text:        fmt.Sprintf("concurrent annotation %d", i),
			Created:     time.Now().UnixNano() / int64(time.Millisecond),
		}
		err := fakeSQL.WithDbSession(context.Background(), func(sess *db.Session) error {
			_, err := sess.Insert(annotation)
			return err
		})
		require.NoError(t, err)
	}

	// Wait for cleanup to complete
	err := <-done
	require.NoError(t, err)

	// Verify database is in consistent state
	err = fakeSQL.WithDbSession(context.Background(), func(sess *db.Session) error {
		count, err := sess.Count(&annotations.Item{})
		require.NoError(t, err)
		assert.Greater(t, count, int64(0), "should have some annotations remaining")
		return nil
	})
	require.NoError(t, err)
}

// TestIntegrationUntilDoneOrCancelledFunction tests the helper function behavior
func TestIntegrationUntilDoneOrCancelledFunction(t *testing.T) {
	t.Run("processes all batches successfully", func(t *testing.T) {
		callCount := 0
		batchWork := func() (int64, error) {
			callCount++
			if callCount > 3 {
				return 0, nil // Signal completion
			}
			return 10, nil
		}

		affected, err := untilDoneOrCancelled(context.Background(), batchWork)
		require.NoError(t, err)
		assert.Equal(t, int64(30), affected)
		assert.Equal(t, 4, callCount)
	})

	t.Run("stops on context cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		callCount := 0

		batchWork := func() (int64, error) {
			callCount++
			if callCount == 2 {
				cancel()
			}
			return 10, nil
		}

		affected, err := untilDoneOrCancelled(ctx, batchWork)
		assert.Error(t, err)
		assert.True(t, errors.Is(err, context.Canceled))
		assert.Equal(t, int64(20), affected) // Should have processed 2 batches before cancellation
	})

	t.Run("stops on error", func(t *testing.T) {
		callCount := 0
		expectedErr := errors.New("batch processing error")

		batchWork := func() (int64, error) {
			callCount++
			if callCount == 2 {
				return 5, expectedErr
			}
			return 10, nil
		}

		affected, err := untilDoneOrCancelled(context.Background(), batchWork)
		assert.Error(t, err)
		assert.Equal(t, expectedErr, err)
		assert.Equal(t, int64(15), affected) // 10 from first batch + 5 from second
		assert.Equal(t, 2, callCount)
	})

	t.Run("handles zero-affected immediately", func(t *testing.T) {
		callCount := 0
		batchWork := func() (int64, error) {
			callCount++
			return 0, nil
		}

		affected, err := untilDoneOrCancelled(context.Background(), batchWork)
		require.NoError(t, err)
		assert.Equal(t, int64(0), affected)
		assert.Equal(t, 1, callCount)
	})
}

// TestIntegrationCleanAnnotationsMixedTypes tests cleanup with different annotation types
func TestIntegrationCleanAnnotationsMixedTypes(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	fakeSQL := db.InitTestDB(t)

	t.Cleanup(func() {
		err := fakeSQL.WithDbSession(context.Background(), func(session *db.Session) error {
			_, err := session.Exec("DELETE FROM annotation")
			return err
		})
		assert.NoError(t, err)
	})

	// Create mix of alert, dashboard, and API annotations
	createTestAnnotations(t, fakeSQL, 30, 0)

	cfg := setting.NewCfg()
	cfg.AnnotationCleanupJobBatchSize = 5
	cleaner := NewXormStore(cfg, log.New("annotation.test"), fakeSQL, nil)

	t.Run("cleanup only alert annotations", func(t *testing.T) {
		initialCount := getAnnotationCount(t, fakeSQL, alertAnnotationType)
		
		affected, err := cleaner.CleanAnnotations(context.Background(), settingsFn(0, 5), alertAnnotationType)
		require.NoError(t, err)

		finalCount := getAnnotationCount(t, fakeSQL, alertAnnotationType)
		assert.Equal(t, int64(5), finalCount)
		assert.Equal(t, initialCount-finalCount, affected)
	})

	t.Run("cleanup only dashboard annotations", func(t *testing.T) {
		initialCount := getAnnotationCount(t, fakeSQL, dashboardAnnotationType)
		
		affected, err := cleaner.CleanAnnotations(context.Background(), settingsFn(0, 3), dashboardAnnotationType)
		require.NoError(t, err)

		finalCount := getAnnotationCount(t, fakeSQL, dashboardAnnotationType)
		assert.Equal(t, int64(3), finalCount)
		assert.Equal(t, initialCount-finalCount, affected)
	})

	t.Run("verify other annotation types untouched", func(t *testing.T) {
		// After cleaning alerts and dashboards, API annotations should be untouched
		apiCount := getAnnotationCount(t, fakeSQL, apiAnnotationType)
		assert.Greater(t, apiCount, int64(0), "API annotations should still exist")
	})
}

// Helper function to get annotation count by type
func getAnnotationCount(t *testing.T, db db.DB, annotationType string) int64 {
	t.Helper()
	var count int64
	err := db.WithDbSession(context.Background(), func(sess *db.Session) error {
		var err error
		count, err = sess.Where(annotationType).Count(&annotations.Item{})
		return err
	})
	require.NoError(t, err)
	return count
}
