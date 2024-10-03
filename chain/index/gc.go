package index

import (
	"context"
	"time"

	logging "github.com/ipfs/go-log/v2"

	"github.com/filecoin-project/lotus/chain/actors/builtin"
)

var (
	log             = logging.Logger("chainindex")
	cleanupInterval = time.Duration(4) * time.Hour
)

const graceEpochs = 10

func (si *SqliteIndexer) gcLoop() {
	defer si.wg.Done()

	// Initial cleanup before entering the loop
	si.gc(si.ctx)

	cleanupTicker := time.NewTicker(cleanupInterval)
	defer cleanupTicker.Stop()

	for si.ctx.Err() == nil {
		if si.isClosed() {
			return
		}

		select {
		case <-cleanupTicker.C:
			si.gc(si.ctx)
		case <-si.ctx.Done():
			return
		}
	}
}

func (si *SqliteIndexer) gc(ctx context.Context) {
	si.writerLk.Lock()
	defer si.writerLk.Unlock()

	if si.gcRetentionEpochs <= 0 {
		log.Info("gc retention epochs is not set, skipping gc")
		return
	}
	log.Info("starting index gc")

	head := si.cs.GetHeaviestTipSet()

	removalEpoch := int64(head.Height()) - si.gcRetentionEpochs - graceEpochs
	if removalEpoch <= 0 {
		log.Info("no tipsets to gc")
		return
	}

	log.Infof("gc'ing all (reverted and non-reverted) tipsets before epoch %d", removalEpoch)

	res, err := si.stmts.removeTipsetsBeforeHeightStmt.ExecContext(ctx, removalEpoch)
	if err != nil {
		log.Errorw("failed to remove reverted tipsets before height", "height", removalEpoch, "error", err)
		return
	}

	rows, err := res.RowsAffected()
	if err != nil {
		log.Errorw("failed to get rows affected", "error", err)
		return
	}

	log.Infof("gc'd %d tipsets before epoch %d", rows, removalEpoch)

	// -------------------------------------------------------------------------------------------------
	// Also GC eth hashes

	// Calculate the retention duration based on the number of epochs to retain.
	// retentionDuration represents the total duration (in seconds) for which data should be retained before considering it for garbage collection.
	// graceDuration represents the additional duration (in seconds) to retain data after the retention duration.
	// Since time.Duration expects a nanosecond value, we multiply the total seconds by time.Second to convert it to seconds.
	retentionDuration := time.Duration(si.gcRetentionEpochs*builtin.EpochDurationSeconds) * time.Second
	graceDuration := time.Duration(graceEpochs*builtin.EpochDurationSeconds) * time.Second

	// Calculate the total duration to retain data.
	totalRetentionDuration := retentionDuration + graceDuration
	currHeadTime := time.Unix(int64(head.MinTimestamp()), 0)
	// gcTime is the time that is (gcRetentionEpochs + graceEpochs) in seconds before currHeadTime
	gcTime := currHeadTime.Add(-totalRetentionDuration)
	if gcTime.Before(time.Unix(0, 0)) || gcTime.IsZero() {
		log.Info("gcTime is invalid, skipping gc")
		return
	}

	log.Infof("gc'ing eth hashes before time %s", gcTime.UTC().String())

	res, err = si.stmts.removeEthHashesBeforeTimeStmt.ExecContext(ctx, gcTime.Unix())
	if err != nil {
		log.Errorf("failed to gc eth hashes before time %s: %w", gcTime.String(), err)
		return
	}

	rows, err = res.RowsAffected()
	if err != nil {
		log.Errorf("failed to get rows affected: %w", err)
		return
	}

	log.Infof("gc'd %d eth hashes before time %s", rows, gcTime.String())
}