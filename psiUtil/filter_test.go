package psiUtil

import (
	"context"
	"testing"
	"time"

	"github.com/ipfs/bbloom"
	"github.com/ipfs/go-cid"
	blocksutil "github.com/ipfs/go-ipfs-blocksutil"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multihash"
)

func TestM_FilterManipulateOutgoing(t *testing.T) {
	cfu := NewClientFilterUtil()
	serverID := peer.ID("sid")
	msg := createOutBsMsg(false)
	new := cfu.ManipulateOutgoing(serverID, msg)
	if new != nil {
		t.Fatal("Created false message")
	}
	keys, ok := cfu.pending[serverID]
	if !ok || len(msg.Wantlist()) != 1 {
		t.Fatal("Message manipulation failed.")
	}

	bf, err := bbloom.New(float64(keys.Len()), FPR)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range keys.Keys() {
		bf.Add(key.Hash())
	}
	cfu.rHaveF[serverID] = bf

	msg = createOutBsMsg(false)
	new = cfu.ManipulateOutgoing(serverID, msg)
	if !msg.Empty() {
		t.Fatal("Message too full")
	}
	if new == nil {
		t.Fatal("Lookup failed.")
	}
	if len(new.BlockPresences()) != cfu.pending[serverID].Len() {
		t.Fatal("Lost CIDs.")
	}

}

func TestM_FilterManipulateIncoming(t *testing.T) {
	bg := blocksutil.NewBlockGenerator()
	blks := bg.Blocks(100)

	cfu := NewClientFilterUtil()
	serverID := peer.ID("sid")
	cfu.pending[serverID] = cid.NewSet()
	var keys []cid.Cid
	for _, blk := range blks {
		cfu.pending[serverID].Add(blk.Cid())
		keys = append(keys, blk.Cid())
	}
	bf, err := bbloom.New(100, FPR)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range keys {
		bf.Add(key.Hash())
	}
	bbf := bf.JSONMarshal()
	mh, _ := multihash.Encode(bbf, DummyCodecBF)
	bfcid := cid.NewCidV1(DummyCodecBF, mh)
	haves := []cid.Cid{bfcid}
	msg := createInBsMsg([]cid.Cid(nil), haves)
	cfu.ManipulateIncoming(serverID, msg)
	if len(msg.BlockPresences()) != len(keys) || len(cfu.pending) != 0 {
		t.Fatal("Manipulation fail")
	}
	if len(msg.Haves()) != len(keys) {
		t.Fatal("Filter fail")
	}
}

func TestM_FilterRun(t *testing.T) {
	cfu := NewClientFilterUtil()
	cfu.pClearDelay = 2 * time.Second
	cfu.rHClearDelay = 2 * time.Second

	msg := createOutBsMsg(false)

	bf, err := bbloom.New(float64(50), FPR)
	if err != nil {
		t.Fatal(err)
	}
	bbf := bf.JSONMarshal()
	mh, _ := multihash.Encode(bbf, DummyCodecBF)
	bfcid := cid.NewCidV1(DummyCodecBF, mh)
	haves := make([]cid.Cid, 1)
	haves[0] = bfcid
	msg2 := createInBsMsg(make([]cid.Cid, 0), haves)
	serverID := peer.ID("DummyPeerID")

	ctx := context.Background()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go cfu.Run(ctx)

	cfu.ManipulateOutgoing(serverID, msg)
	if len(cfu.pending) == 0 {
		t.Fatal("Not enough pendings.")
	}
	time.Sleep(3 * time.Second)
	if len(cfu.pending) > 0 {
		t.Fatal("Too many pendings.")
	}
	cfu.ManipulateIncoming(serverID, msg2)
	if len(cfu.rHaveF) == 0 {
		t.Fatal("Not enough filter.")
	}
	time.Sleep(3 * time.Second)
	if len(cfu.rHaveF) > 0 {
		t.Fatal("Too many filter.")
	}

}

func TestM_FilterTransfromHaves(t *testing.T) {
	sfu := NewServerFilterUtil()
	bg := blocksutil.NewBlockGenerator()
	blks := bg.Blocks(100)
	var keys []cid.Cid
	for _, blk := range blks {
		keys = append(keys, blk.Cid())
	}
	arr := sfu.TransformHaves(DummyCodecBF, keys)
	if len(arr) != 1 {
		t.Fatal("Filter not stored")
	}
	bfcid := arr[0]
	decbfcid, err := multihash.Decode(bfcid.Hash())
	if err != nil {
		t.Fatal(err)
	}
	bf, err := bbloom.JSONUnmarshal(decbfcid.Digest)
	if err != nil {
		t.Fatal(err)
	}
	has := 0
	for _, key := range keys {
		ok := bf.Has(key.Hash())
		if ok {
			has++
		}
	}
	if has != len(blks) {
		t.Fatalf("Some elements not in the Bloom Filter: %v/100", has)
	}
}
