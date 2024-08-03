package state

import (
	"context"
	"crypto/rand"
	// "encoding/binary"
	// "encoding/hex"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	abci "github.com/cometbft/cometbft/abci/types"
	cfg "github.com/cometbft/cometbft/config"
	"github.com/cometbft/cometbft/crypto/ed25519"
	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/libs/pubsub/query"
	cmproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/cometbft/cometbft/proxy"
	cmtypes "github.com/cometbft/cometbft/types"

	// conf "github.com/AnomalyFi/nodekit-relay/config"
	// relay "github.com/AnomalyFi/nodekit-relay/rpc"

	// "github.com/rollkit/rollkit/config"
	"github.com/rollkit/rollkit/mempool"
	"github.com/rollkit/rollkit/test/mocks"
	"github.com/rollkit/rollkit/types"
)

func prepareProposalResponse(_ context.Context, req *abci.RequestPrepareProposal) (*abci.ResponsePrepareProposal, error) {
	return &abci.ResponsePrepareProposal{
		Txs: req.Txs,
	}, nil
}

func doTestCreateBlock(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	logger := log.TestingLogger()

	app := &mocks.Application{}
	app.On("CheckTx", mock.Anything, mock.Anything).Return(&abci.ResponseCheckTx{}, nil)
	app.On("PrepareProposal", mock.Anything, mock.Anything).Return(prepareProposalResponse)
	app.On("ProcessProposal", mock.Anything, mock.Anything).Return(&abci.ResponseProcessProposal{Status: abci.ResponseProcessProposal_ACCEPT}, nil)
	fmt.Println("App On CheckTx")
	client, err := proxy.NewLocalClientCreator(app).NewABCIClient()
	fmt.Println("Created New Local Client")
	require.NoError(err)
	require.NotNil(client)

	fmt.Println("Made NID")
	mpool := mempool.NewCListMempool(cfg.DefaultMempoolConfig(), proxy.NewAppConnMempool(client, proxy.NopMetrics()), 0)
	fmt.Println("Made a NewTxMempool")
	executor := NewBlockExecutor([]byte("test address"), "test", mpool, proxy.NewAppConnConsensus(client, proxy.NopMetrics()), nil, 100, logger, NopMetrics(), types.GetRandomBytes(32))
	fmt.Println("Made a New Block Executor")

	state := types.State{}

	state.ConsensusParams.Block = &cmproto.BlockParams{}
	state.ConsensusParams.Block.MaxBytes = 100
	state.ConsensusParams.Block.MaxGas = 100000

	// empty block
	block, err := executor.CreateBlock(1, &types.Commit{}, abci.ExtendedCommitInfo{}, []byte{}, state)
	require.NoError(err)
	require.NotNil(block)
	assert.Empty(block.Data.Txs)
	assert.Equal(uint64(1), block.Height())

	// one small Tx
	err = mpool.CheckTx([]byte{1, 2, 3, 4}, func(r *abci.ResponseCheckTx) {}, mempool.TxInfo{})
	require.NoError(err)
	block, err = executor.CreateBlock(2, &types.Commit{}, abci.ExtendedCommitInfo{}, []byte{}, state)
	require.NoError(err)
	require.NotNil(block)
	assert.Equal(uint64(2), block.Height())
	assert.Len(block.Data.Txs, 1)

	// now there are 3 Txs, and only two can fit into single block
	err = mpool.CheckTx([]byte{4, 5, 6, 7}, func(r *abci.ResponseCheckTx) {}, mempool.TxInfo{})
	require.NoError(err)
	err = mpool.CheckTx(make([]byte, 100), func(r *abci.ResponseCheckTx) {}, mempool.TxInfo{})
	require.NoError(err)
	block, err = executor.CreateBlock(3, &types.Commit{}, abci.ExtendedCommitInfo{}, []byte{}, state)
	require.NoError(err)
	require.NotNil(block)
	assert.Len(block.Data.Txs, 2)

	// limit max bytes
	mpool.Flush()
	executor.maxBytes = 10
	err = mpool.CheckTx(make([]byte, 10), func(r *abci.ResponseCheckTx) {}, mempool.TxInfo{})
	require.NoError(err)
	block, err = executor.CreateBlock(4, &types.Commit{}, abci.ExtendedCommitInfo{}, []byte{}, state)
	require.NoError(err)
	require.NotNil(block)
	assert.Empty(block.Data.Txs)
}

// func TestCreateBlockWithFraudProofsDisabled(t *testing.T) {
// 	doTestCreateBlock(t)
// }

func doTestApplyBlock(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	logger := log.TestingLogger()

	var mockAppHash []byte
	_, err := rand.Read(mockAppHash[:])
	require.NoError(err)

	app := &mocks.Application{}
	app.On("CheckTx", mock.Anything, mock.Anything).Return(&abci.ResponseCheckTx{}, nil)
	app.On("Commit", mock.Anything, mock.Anything).Return(&abci.ResponseCommit{}, nil)
	app.On("PrepareProposal", mock.Anything, mock.Anything).Return(prepareProposalResponse)
	app.On("ProcessProposal", mock.Anything, mock.Anything).Return(&abci.ResponseProcessProposal{Status: abci.ResponseProcessProposal_ACCEPT}, nil)
	app.On("FinalizeBlock", mock.Anything, mock.Anything).Return(
		func(_ context.Context, req *abci.RequestFinalizeBlock) (*abci.ResponseFinalizeBlock, error) {
			txResults := make([]*abci.ExecTxResult, len(req.Txs))
			for idx := range req.Txs {
				txResults[idx] = &abci.ExecTxResult{
					Code: abci.CodeTypeOK,
				}
			}

			return &abci.ResponseFinalizeBlock{
				TxResults: txResults,
				AppHash:   mockAppHash,
			}, nil
		},
	)

	client, err := proxy.NewLocalClientCreator(app).NewABCIClient()
	require.NoError(err)
	require.NotNil(client)

	mpool := mempool.NewCListMempool(cfg.DefaultMempoolConfig(), proxy.NewAppConnMempool(client, proxy.NopMetrics()), 0)
	eventBus := cmtypes.NewEventBus()
	require.NoError(eventBus.Start())

	txQuery, err := query.New("tm.event='Tx'")
	require.NoError(err)
	txSub, err := eventBus.Subscribe(context.Background(), "test", txQuery, 1000)
	require.NoError(err)
	require.NotNil(txSub)

	headerQuery, err := query.New("tm.event='NewBlockHeader'")
	require.NoError(err)
	headerSub, err := eventBus.Subscribe(context.Background(), "test", headerQuery, 100)
	require.NoError(err)
	require.NotNil(headerSub)

	vKey := ed25519.GenPrivKey()
	validators := []*cmtypes.Validator{
		{
			Address:          vKey.PubKey().Address(),
			PubKey:           vKey.PubKey(),
			VotingPower:      int64(100),
			ProposerPriority: int64(1),
		},
	}
	state := types.State{}
	state.InitialHeight = 1
	state.LastBlockHeight = 0
	state.ConsensusParams.Block = &cmproto.BlockParams{}
	state.ConsensusParams.Block.MaxBytes = 100
	state.ConsensusParams.Block.MaxGas = 100000
	chainID := "test"
	executor := NewBlockExecutor(vKey.PubKey().Address().Bytes(), chainID, mpool, proxy.NewAppConnConsensus(client, proxy.NopMetrics()), eventBus, 100, logger, NopMetrics(), types.GetRandomBytes(32))

	err = mpool.CheckTx([]byte{1, 2, 3, 4}, func(r *abci.ResponseCheckTx) {}, mempool.TxInfo{})
	require.NoError(err)
	block, err := executor.CreateBlock(1, &types.Commit{Signatures: []types.Signature{types.Signature([]byte{1, 1, 1})}}, abci.ExtendedCommitInfo{}, []byte{}, state)
	require.NoError(err)
	require.NotNil(block)
	assert.Equal(uint64(1), block.Height())
	assert.Len(block.Data.Txs, 1)
	dataHash, err := block.Data.Hash()
	assert.NoError(err)
	block.SignedHeader.DataHash = dataHash

	// Update the signature on the block to current from last
	voteBytes := block.SignedHeader.Header.MakeCometBFTVote()
	sig, _ := vKey.Sign(voteBytes)
	block.SignedHeader.Commit = types.Commit{
		Signatures: []types.Signature{sig},
	}
	block.SignedHeader.Validators = cmtypes.NewValidatorSet(validators)

	newState, resp, err := executor.ApplyBlock(context.Background(), state, block)
	require.NoError(err)
	require.NotNil(newState)
	require.NotNil(resp)
	assert.Equal(uint64(1), newState.LastBlockHeight)
	appHash, _, err := executor.Commit(context.Background(), newState, block, resp)
	require.NoError(err)
	assert.Equal(mockAppHash, appHash)

	require.NoError(mpool.CheckTx([]byte{0, 1, 2, 3, 4}, func(r *abci.ResponseCheckTx) {}, mempool.TxInfo{}))
	require.NoError(mpool.CheckTx([]byte{5, 6, 7, 8, 9}, func(r *abci.ResponseCheckTx) {}, mempool.TxInfo{}))
	require.NoError(mpool.CheckTx([]byte{1, 2, 3, 4, 5}, func(r *abci.ResponseCheckTx) {}, mempool.TxInfo{}))
	require.NoError(mpool.CheckTx(make([]byte, 90), func(r *abci.ResponseCheckTx) {}, mempool.TxInfo{}))
	block, err = executor.CreateBlock(2, &types.Commit{Signatures: []types.Signature{types.Signature([]byte{1, 1, 1})}}, abci.ExtendedCommitInfo{}, []byte{}, newState)
	require.NoError(err)
	require.NotNil(block)
	assert.Equal(uint64(2), block.Height())
	assert.Len(block.Data.Txs, 3)
	dataHash, err = block.Data.Hash()
	assert.NoError(err)
	block.SignedHeader.DataHash = dataHash

	voteBytes = block.SignedHeader.Header.MakeCometBFTVote()
	sig, _ = vKey.Sign(voteBytes)
	block.SignedHeader.Commit = types.Commit{
		Signatures: []types.Signature{sig},
	}
	block.SignedHeader.Validators = cmtypes.NewValidatorSet(validators)

	newState, resp, err = executor.ApplyBlock(context.Background(), newState, block)
	require.NoError(err)
	require.NotNil(newState)
	require.NotNil(resp)
	assert.Equal(uint64(2), newState.LastBlockHeight)
	_, _, err = executor.Commit(context.Background(), newState, block, resp)
	require.NoError(err)

	// wait for at least 4 Tx events, for up to 3 second.
	// 3 seconds is a fail-scenario only
	timer := time.NewTimer(3 * time.Second)
	txs := make(map[int64]int)
	cnt := 0
	for cnt != 4 {
		select {
		case evt := <-txSub.Out():
			cnt++
			data, ok := evt.Data().(cmtypes.EventDataTx)
			assert.True(ok)
			assert.NotEmpty(data.Tx)
			txs[data.Height]++
		case <-timer.C:
			t.FailNow()
		}
	}
	assert.Zero(len(txSub.Out())) // expected exactly 4 Txs - channel should be empty
	assert.EqualValues(1, txs[1])
	assert.EqualValues(3, txs[2])

	require.EqualValues(2, len(headerSub.Out()))
	for h := 1; h <= 2; h++ {
		evt := <-headerSub.Out()
		_, ok := evt.Data().(cmtypes.EventDataNewBlockHeader)
		assert.True(ok)
	}
}

// func TestApplyBlockWithFraudProofsDisabled(t *testing.T) {
// 	doTestApplyBlock(t)
// }

func TestUpdateStateConsensusParams(t *testing.T) {
	logger := log.TestingLogger()
	app := &mocks.Application{}
	client, err := proxy.NewLocalClientCreator(app).NewABCIClient()
	require.NoError(t, err)
	require.NotNil(t, client)

	chainID := "test"

	mpool := mempool.NewCListMempool(cfg.DefaultMempoolConfig(), proxy.NewAppConnMempool(client, proxy.NopMetrics()), 0)
	eventBus := cmtypes.NewEventBus()
	require.NoError(t, eventBus.Start())
	executor := NewBlockExecutor([]byte("test address"), chainID, mpool, proxy.NewAppConnConsensus(client, proxy.NopMetrics()), eventBus, 100, logger, NopMetrics(), types.GetRandomBytes(32))

	state := types.State{
		ConsensusParams: cmproto.ConsensusParams{
			Block: &cmproto.BlockParams{
				MaxBytes: 100,
				MaxGas:   100000,
			},
			Validator: &cmproto.ValidatorParams{
				PubKeyTypes: []string{cmtypes.ABCIPubKeyTypeEd25519},
			},
			Version: &cmproto.VersionParams{
				App: 1,
			},
			Abci: &cmproto.ABCIParams{},
		},
	}

	block := types.GetRandomBlock(1234, 2)

	txResults := make([]*abci.ExecTxResult, len(block.Data.Txs))
	for idx := range block.Data.Txs {
		txResults[idx] = &abci.ExecTxResult{
			Code: abci.CodeTypeOK,
		}
	}

	resp := &abci.ResponseFinalizeBlock{
		ConsensusParamUpdates: &cmproto.ConsensusParams{
			Block: &cmproto.BlockParams{
				MaxBytes: 200,
				MaxGas:   200000,
			},
			Validator: &cmproto.ValidatorParams{
				PubKeyTypes: []string{cmtypes.ABCIPubKeyTypeEd25519},
			},
			Version: &cmproto.VersionParams{
				App: 2,
			},
		},
		TxResults: txResults,
	}

	updatedState, err := executor.updateState(state, block, resp)
	require.NoError(t, err)

	assert.Equal(t, uint64(1235), updatedState.LastHeightConsensusParamsChanged)
	assert.Equal(t, int64(200), updatedState.ConsensusParams.Block.MaxBytes)
	assert.Equal(t, int64(200000), updatedState.ConsensusParams.Block.MaxGas)
	assert.Equal(t, uint64(2), updatedState.ConsensusParams.Version.App)
}

// TODO: Test new logic in Create Block 
// func TestNodeKit(t *testing.T) {
// 	// assert := assert.New(t)
// 	require := require.New(t)
	
// 	logger := log.TestingLogger()
	
// 	app := &mocks.Application{}
// 	app.On("CheckTx", mock.Anything, mock.Anything).Return(&abci.ResponseCheckTx{}, nil)
// 	app.On("PrepareProposal", mock.Anything, mock.Anything).Return(prepareProposalResponse)
// 	app.On("ProcessProposal", mock.Anything, mock.Anything).Return(&abci.ResponseProcessProposal{Status: abci.ResponseProcessProposal_ACCEPT}, nil)
// 	fmt.Println("App On CheckTx")
// 	client, err := proxy.NewLocalClientCreator(app).NewABCIClient()
// 	fmt.Println("Created New Local Client")
// 	require.NoError(err)
// 	require.NotNil(client)
	
// 	fmt.Println("Made NID")
// 	// mpool := mempool.NewCListMempool(cfg.DefaultMempoolConfig(), proxy.NewAppConnMempool(client, proxy.NopMetrics()), 0)
// 	fmt.Println("Made a NewTxMempool")
// 	// executor := NewBlockExecutor([]byte("test address"), "test", mpool, proxy.NewAppConnConsensus(client, proxy.NopMetrics()), nil, 100, logger, NopMetrics(), types.GetRandomBytes(32))
// 	fmt.Println("Made a New Block Executor")
	
// 	state := types.State{}
	
// 	state.ConsensusParams.Block = &cmproto.BlockParams{}
// 	state.ConsensusParams.Block.MaxBytes = 100
// 	state.ConsensusParams.Block.MaxGas = 100000

// 	//SEQ test
// 	chainID := "hCTcJQm6811V9Suj6XomjXEcszEPLpG3nD4dRWUWUQHgZRWbJ"
// 	uri := "http://54.175.18.95:9650/ext/bc/hCTcJQm6811V9Suj6XomjXEcszEPLpG3nD4dRWUWUQHgZRWbJ"
// 	rollupChainID := uint64(45200)
// 	rollupNamespace := make([]byte, 8)
// 	binary.LittleEndian.PutUint64(rollupNamespace, rollupChainID)

// 	// seq := NewClient(uri, chainID)

// 	// create mpool of tx(s)
// 	mpoolTxs := []types.Tx{
// 		[]byte{1, 2, 3, 4},
// 		[]byte{5, 6, 7, 8},
// 		[]byte{9, 10, 11, 12},
// 		[]byte{13, 14, 15, 16},
// 		[]byte{17, 18, 19, 20},
// 		[]byte{21, 22, 23, 24},
// 	}
// 	// submit tx(s) from mpool
// 	for _,tx := range mpoolTxs {
// 		seq.client.SubmitTx(context.Background(), chainID, 1337, rollupNamespace, tx)
// 	}
// 	hexNamespace := hex.EncodeToString(rollupNamespace)
// 	// IMPORTANT: first submit txs, then get height from submitted block on SEQ and input it below.
// 	blockHeight := uint64(271) 
// 	// IMPORTANT: make sure values in this file with SEQ are the same and match the values in executer.go to test the code. 
// 	// gets transactions from submitted SEQ block height
// 	blockTxs, err := seq.client.GetBlockTransactionsByNamespace(context.Background(), blockHeight, hexNamespace)
// 	require.NoError(err)
// 	fmt.Printf("blockTxs test: %v\n", blockTxs)
// 	// Convert the transactions to the rollkit type(does this in executor.go but here it is printed to make sure it correctly displays tx format needed)
// 	rollkitTxs := fromSEQTransactions(blockTxs.Txs)
// 	fmt.Printf("rollkitTxs: %v\n", rollkitTxs)
// 	// Check that the transactions in the block returned by CreateBlockmatch the transactions from the sequencer
// 	block, err := executor.CreateBlock(1, &types.Commit{}, abci.ExtendedCommitInfo{}, []byte{}, state)
// 	require.NoError(err)
// 	require.NotNil(block)
// 	// assert.Equal(t, rollkitTxs, block.Data.Txs)
// 	assert.Equal(t, uint64(1), block.Height())

// 	// debugging
// 	fmt.Printf("block: %v\n", block)
// 	fmt.Printf("block height: %v\n", block.Height())
// 	fmt.Printf("block TXS: %v\n", block.Data.Txs)
	
// 	debugging relayer funcs
// 	relay_uri := "http://127.0.0.1:12510"

// 	file := conf.SeqJsonRPCConfig {
// 		URI: uri,
// 		NetworkID: 1337,
// 		ChainID: chainID,
// 	}

// 	cli, err := relay.NewJSONRPCClient(relay_uri, file)
// 	require.NoError(err)
// 	fmt.Printf("da test cli : %v\n", cli)
// 	daBlock, err := cli.GetSeqBlock(context.Background(), blockHeight)
// 	require.NoError(err)
// 	fmt.Printf(" da test seq block: %v\n", daBlock)
// 	stable, err := cli.GetStableSeqHeight(context.Background())
// 	require.NoError(err)
// 	fmt.Printf("da test stable seq block: %v\n", stable)
// 	ns, _, err := cli.GetNamespacedSeqBlock(context.Background(), []byte("opstack deployment seq chain id "), blockHeight)
// 	require.NoError(err)
// 	fmt.Printf("da test ns : %v\n", ns)	
// }