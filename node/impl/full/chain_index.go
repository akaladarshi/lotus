package full

import (
	"context"
	"errors"

	"go.uber.org/fx"

	"github.com/filecoin-project/go-state-types/abi"

	"github.com/filecoin-project/lotus/api"
	"github.com/filecoin-project/lotus/chain/types"
	"github.com/filecoin-project/lotus/chainindex"
)

type ChainIndexerAPI interface {
	ChainValidateIndex(ctx context.Context, epoch abi.ChainEpoch, backfill bool) (*types.IndexValidation, error)
}

var (
	_ ChainIndexerAPI = *new(api.FullNode)
)

type ChainIndexAPI struct {
	fx.In
	ChainIndexerAPI
}

type ChainIndexHandler struct {
	indexer chainindex.Indexer
}

func (ch *ChainIndexHandler) ChainValidateIndex(ctx context.Context, epoch abi.ChainEpoch, backfill bool) (*types.IndexValidation, error) {
	if ch.indexer == nil {
		return nil, errors.New("indexer is not initialized")
	}
	return ch.indexer.ChainValidateIndex(ctx, epoch, backfill)
}

var _ ChainIndexerAPI = (*ChainIndexHandler)(nil)

func NewChainIndexHandler(
	indexer chainindex.Indexer,
) *ChainIndexHandler {
	return &ChainIndexHandler{
		indexer: indexer,
	}
}
