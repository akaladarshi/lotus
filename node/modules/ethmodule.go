package modules

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/hashicorp/golang-lru/arc/v2"
	"github.com/ipfs/go-cid"
	"go.uber.org/fx"

	"github.com/filecoin-project/go-state-types/abi"
	"github.com/filecoin-project/lotus/chain/ethhashlookup"
	"github.com/filecoin-project/lotus/chain/events"
	"github.com/filecoin-project/lotus/chain/messagepool"
	"github.com/filecoin-project/lotus/chain/stmgr"
	"github.com/filecoin-project/lotus/chain/store"
	"github.com/filecoin-project/lotus/chain/types/ethtypes"
	"github.com/filecoin-project/lotus/node/config"
	"github.com/filecoin-project/lotus/node/impl/full"
	"github.com/filecoin-project/lotus/node/modules/helpers"
	"github.com/filecoin-project/lotus/node/repo"
	"golang.org/x/xerrors"
)

func EthModuleAPI(cfg config.FevmConfig, enableAutomaticBackFill bool, maxAutomaticBackFillBlocks uint64) func(helpers.MetricsCtx, repo.LockedRepo, fx.Lifecycle, *store.ChainStore, *stmgr.StateManager, EventHelperAPI, *messagepool.MessagePool, full.StateAPI, full.ChainAPI, full.MpoolAPI, full.SyncAPI, *full.EthEventHandler) (*full.EthModule, error) {
	return func(mctx helpers.MetricsCtx, r repo.LockedRepo, lc fx.Lifecycle, cs *store.ChainStore, sm *stmgr.StateManager, evapi EventHelperAPI, mp *messagepool.MessagePool, stateapi full.StateAPI, chainapi full.ChainAPI, mpoolapi full.MpoolAPI, syncapi full.SyncAPI, ethEventHandler *full.EthEventHandler) (*full.EthModule, error) {
		ctx := helpers.LifecycleCtx(mctx, lc)

		sqlitePath, err := r.SqlitePath()
		if err != nil {
			return nil, err
		}

		dbPath := filepath.Join(sqlitePath, ethhashlookup.DefaultDbFilename)

		// Check if the db exists, if not, we'll back-fill some entries
		_, err = os.Stat(dbPath)
		dbAlreadyExists := err == nil

		transactionHashLookup, err := ethhashlookup.NewTransactionHashLookup(ctx, dbPath)
		if err != nil {
			return nil, err
		}

		lc.Append(fx.Hook{
			OnStop: func(ctx context.Context) error {
				return transactionHashLookup.Close()
			},
		})

		ethTxHashManager := full.EthTxHashManager{
			StateAPI:              stateapi,
			ChainAPI:              chainapi,
			TransactionHashLookup: transactionHashLookup,
		}

		if !dbAlreadyExists {
			err = ethTxHashManager.PopulateExistingMappings(mctx, 0)
			if err != nil {
				return nil, err
			}
		}

		// prefill the whole skiplist cache maintained internally by the GetTipsetByHeight
		go func() {
			start := time.Now()
			log.Infoln("Start prefilling GetTipsetByHeight cache")
			_, err := cs.GetTipsetByHeight(mctx, abi.ChainEpoch(0), cs.GetHeaviestTipSet(), false)
			if err != nil {
				log.Warnf("error when prefilling GetTipsetByHeight cache: %w", err)
			}
			log.Infof("Prefilling GetTipsetByHeight done in %s", time.Since(start))
		}()

		lc.Append(fx.Hook{
			OnStart: func(context.Context) error {
				ev, err := events.NewEvents(ctx, &evapi)
				if err != nil {
					return err
				}

				// Tipset listener
				head := ev.Observe(&ethTxHashManager)

				if enableAutomaticBackFill { // If the db exists and back-fill is enabled, we'll back-fill missing entries
					go func() { // since there is only one DB connection, so we can do this in a goroutine without worrying about concurrent write issues
						err = ethTxHashManager.FillIndexGap(head, mctx, abi.ChainEpoch(maxAutomaticBackFillBlocks))
						if err != nil {
							log.Warnf("error when back-filling transaction index gap: %w", err)
						}
					}()
				}

				ch, err := mp.Updates(ctx)
				if err != nil {
					return err
				}
				go full.WaitForMpoolUpdates(ctx, ch, &ethTxHashManager)
				go full.EthTxHashGC(ctx, cfg.EthTxHashMappingLifetimeDays, &ethTxHashManager)

				return nil
			},
		})

		var blkCache *arc.ARCCache[cid.Cid, *ethtypes.EthBlock]
		var blkTxCache *arc.ARCCache[cid.Cid, *ethtypes.EthBlock]
		if cfg.EthBlkCacheSize > 0 {
			blkCache, err = arc.NewARC[cid.Cid, *ethtypes.EthBlock](cfg.EthBlkCacheSize)
			if err != nil {
				return nil, xerrors.Errorf("failed to create block cache: %w", err)
			}

			blkTxCache, err = arc.NewARC[cid.Cid, *ethtypes.EthBlock](cfg.EthBlkCacheSize)
			if err != nil {
				return nil, xerrors.Errorf("failed to create block transaction cache: %w", err)
			}
		}

		return &full.EthModule{
			Chain:        cs,
			Mpool:        mp,
			StateManager: sm,

			ChainAPI:        chainapi,
			MpoolAPI:        mpoolapi,
			StateAPI:        stateapi,
			SyncAPI:         syncapi,
			EthEventHandler: ethEventHandler,

			EthTxHashManager:         &ethTxHashManager,
			EthTraceFilterMaxResults: cfg.EthTraceFilterMaxResults,

			EthBlkCache:   blkCache,
			EthBlkTxCache: blkTxCache,
		}, nil
	}
}
