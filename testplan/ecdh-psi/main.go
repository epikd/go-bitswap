package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"time"

	logging "github.com/ipfs/go-log"

	bitswap "github.com/ipfs/go-bitswap"
	bsnet "github.com/ipfs/go-bitswap/network"
	block "github.com/ipfs/go-block-format"
	"github.com/ipfs/go-cid"
	"github.com/ipfs/go-datastore"
	blockstore "github.com/ipfs/go-ipfs-blockstore"
	exchange "github.com/ipfs/go-ipfs-exchange-interface"
	libp2p "github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
	"github.com/multiformats/go-multihash"
	"github.com/testground/sdk-go/network"
	"github.com/testground/sdk-go/run"
	"github.com/testground/sdk-go/runtime"
	"github.com/testground/sdk-go/sync"
)

var (
	testcases = map[string]interface{}{
		"speed-test": run.InitializedTestCaseFn(runSpeedTest),
	}
	networkState  = sync.State("network-configured")
	readyState    = sync.State("ready-to-publish")
	readyDLState  = sync.State("ready-to-download")
	doneState     = sync.State("done")
	providerTopic = sync.NewTopic("provider", &peer.AddrInfo{})
	blockTopic    = sync.NewTopic("blocks", &multihash.Multihash{})
)

func main() {
	run.InvokeMap(testcases)
}

func runSpeedTest(runenv *runtime.RunEnv, initCtx *run.InitContext) error {

	runenv.RecordMessage("running speed-test")
	ctx := context.Background()
	//linkShape := network.LinkShape{}
	linkShape := network.LinkShape{
		Latency:       100 * time.Millisecond,
		Jitter:        5 * time.Millisecond,
		Bandwidth:     3e6,
		Loss:          0.02,
		Corrupt:       0.01,
		CorruptCorr:   0.1,
		Reorder:       0.01,
		ReorderCorr:   0.1,
		Duplicate:     0.02,
		DuplicateCorr: 0.1,
	}
	initCtx.NetClient.MustConfigureNetwork(ctx, &network.Config{
		Network:        "default",
		Enable:         true,
		Default:        linkShape,
		CallbackState:  networkState,
		CallbackTarget: runenv.TestGroupInstanceCount,
		RoutingPolicy:  network.AllowAll,
	})
	listen, err := multiaddr.NewMultiaddr(fmt.Sprintf("/ip4/%s/tcp/%d", initCtx.NetClient.MustGetDataNetworkIP().String(), 3333+initCtx.GlobalSeq))
	if err != nil {
		return err
	}
	h, err := libp2p.New(libp2p.ListenAddrs(listen))
	if err != nil {
		return err
	}
	kad, err := dht.New(ctx, h)
	if err != nil {
		return err
	}
	for _, a := range h.Addrs() {
		runenv.RecordMessage("listening on addr: %s", a.String())
	}

	bstore := blockstore.NewBlockstore(datastore.NewMapDatastore())
	ex := bitswap.New(ctx, bsnet.NewFromIpfsHost(h, kad), bstore)
	logging.SetAllLoggers(logging.LevelInfo)

	err = logging.SetLogLevel("*", "info")
	if err != nil {
		fmt.Print("Logging error:" + err.Error())
	}

	switch c := initCtx.GlobalSeq; {
	case c < 2:
		runenv.RecordMessage("running provider")
		err = runProvide(ctx, runenv, h, bstore, ex)
	case c > 1:
		runenv.RecordMessage("running requestor")
		err = runRequest(ctx, runenv, h, bstore, ex)
	default:
		runenv.RecordMessage("not part of a group")
		err = errors.New("unknown test group id")
	}
	return err
}

func runProvide(ctx context.Context, runenv *runtime.RunEnv, h host.Host, bstore blockstore.Blockstore, ex exchange.Interface) error {

	tgc := sync.MustBoundClient(ctx, runenv)
	ai := peer.AddrInfo{
		ID:    h.ID(),
		Addrs: h.Addrs(),
	}
	tgc.MustPublish(ctx, providerTopic, &ai)
	tgc.MustSignalAndWait(ctx, readyState, runenv.TestInstanceCount)

	size := runenv.SizeParam("size")
	count := runenv.IntParam("count")
	for i := 0; i < count; i++ {
		runenv.RecordMessage("generating %d-sized random block", size)
		buf := make([]byte, size)
		_, _ = rand.Read(buf)
		blk := block.NewBlock(buf)
		err := bstore.Put(ctx, blk)
		if err != nil {
			return err
		}
		err = ex.NotifyNewBlocks(ctx, blk)
		if err != nil {
			return err
		}
		mh := blk.Multihash()
		runenv.RecordMessage("publishing block %s", mh.String())
		tgc.MustPublish(ctx, blockTopic, &mh)
	}
	tgc.MustSignalAndWait(ctx, readyDLState, runenv.TestInstanceCount)
	runenv.RecordMessage("ReadyDL")
	tgc.MustSignalAndWait(ctx, doneState, runenv.TestInstanceCount)

	runenv.RecordMessage("Done")
	return nil
}

func runRequest(ctx context.Context, runenv *runtime.RunEnv, h host.Host, bstore blockstore.Blockstore, ex exchange.Interface) error {

	tgc := sync.MustBoundClient(ctx, runenv)
	providers := make(chan *peer.AddrInfo)
	blkmhs := make(chan *multihash.Multihash)
	providerSub, err := tgc.Subscribe(ctx, providerTopic, providers)
	if err != nil {
		return err
	}
	ai := <-providers

	runenv.RecordMessage("connecting  to provider provider: %s", fmt.Sprint(*ai))
	providerSub.Done()

	err = h.Connect(ctx, *ai)
	if err != nil {
		return fmt.Errorf("could not connect to provider: %w", err)
	}

	runenv.RecordMessage("connected to provider")

	blockmhSub, err := tgc.Subscribe(ctx, blockTopic, blkmhs)
	if err != nil {
		return fmt.Errorf("could not subscribe to block sub: %w", err)
	}
	defer blockmhSub.Done()

	// tell the provider that we're ready for it to publish blocks
	tgc.MustSignalAndWait(ctx, readyState, runenv.TestInstanceCount)
	runenv.RecordMessage("Ready")

	cids := cid.NewSet()
	count := runenv.IntParam("count")
	for i := 0; i < count; i++ {
		mh := <-blkmhs
		cids.Add(cid.NewCidV0(*mh))
	}
	runenv.RecordMessage("Received: CIDs -- %v \n", cids.Keys())

	// wait until the provider is ready for us to start downloading
	tgc.MustSignalAndWait(ctx, readyDLState, runenv.TestInstanceCount)
	runenv.RecordMessage("ReadyDL")
	begin := time.Now()
	for _, key := range cids.Keys() {
		runenv.RecordMessage("downloading block %s", key.String())
		dlBegin := time.Now()
		blk, err := ex.(*bitswap.Bitswap).PsiGetBlock(ctx, key)
		if err != nil {
			return fmt.Errorf("could not download block %s: %w", key.String(), err)
		}
		err = bstore.Put(ctx, blk) // store block
		if err != nil {
			return fmt.Errorf("could not store block %s: %w", key.String(), err)
		}
		dlDuration := time.Since(dlBegin)
		s := &BitswapStat{
			SingleDownloadSpeed: &SingleDownloadSpeed{
				Cid:              blk.Cid().String(),
				DownloadDuration: dlDuration,
			},
		}
		runenv.RecordMessage(Marshal(s))

		stored, err := bstore.Has(ctx, blk.Cid())
		if err != nil {
			return fmt.Errorf("error checking if blck was stored %s: %w", key.String(), err)
		}
		if !stored {
			return fmt.Errorf("block was not stored %s: %w", key.String(), err)
		}
	}
	duration := time.Since(begin)
	s := &BitswapStat{
		MultipleDownloadSpeed: &MultipleDownloadSpeed{
			BlockCount:    count,
			TotalDuration: duration,
		},
	}
	runenv.RecordMessage(Marshal(s))
	// Request first block again
	if cids.Len() > 0 {
		cid := cids.Keys()[0]
		err = bstore.DeleteBlock(ctx, cid)
		if err != nil {
			runenv.RecordFailure(err)
		}
		runenv.RecordMessage("Redownloading block %s", cid.String())
		dlBegin := time.Now()
		blk, err := ex.GetBlock(ctx, cid)
		if err != nil {
			return fmt.Errorf("could not download block %s: %w", cid.String(), err)
		}
		err = bstore.Put(ctx, blk) // store block
		if err != nil {
			runenv.RecordFailure(err)
		}
		dlDuration := time.Since(dlBegin)
		s := &BitswapStat{
			SingleDownloadSpeed: &SingleDownloadSpeed{
				Cid:              blk.Cid().String(),
				DownloadDuration: dlDuration,
			},
		}
		runenv.RecordMessage(Marshal(s))
	}

	tgc.MustSignalEntry(ctx, doneState)
	return nil
}
