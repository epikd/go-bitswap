package psiUtil

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/cloudflare/circl/group"
	"github.com/ipfs/bbloom"
	bsmsg "github.com/ipfs/go-bitswap/message"
	bitswap_message_pb "github.com/ipfs/go-bitswap/message/pb"
	"github.com/ipfs/go-cid"
	blocksutil "github.com/ipfs/go-ipfs-blocksutil"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multihash"
)

func TestM_New(t *testing.T) {
	_, err := NewClientPsiUtil(group.Ristretto255, false)
	if err != nil {
		t.Fatal(err)
	}

	_, err = NewClientPsiUtil(group.P256, false)
	if err != nil {
		t.Fatal(err)
	}

	_, err = NewServerPsiUtil(group.P384)
	if err != nil {
		t.Fatal(err)
	}

	_, err = NewServerPsiUtil(group.P521)
	if err != nil {
		t.Fatal(err)
	}

	var dummygroup group.Group
	_, err = NewClientPsiUtil(dummygroup, true)
	if err == nil {
		t.Fatal(err)
	}

	_, err = NewServerPsiUtil(dummygroup)
	if err == nil {
		t.Fatal(err)
	}
}

func TestMClient_nextID(t *testing.T) {
	cpu, _ := NewClientPsiUtil(group.Ristretto255, true)
	cpu.id = math.MaxUint16 - 5
	for i := uint32(1); i < math.MaxUint16+1; i++ {
		id := cpu.nextID()
		cpu.idCid[id] = cid.Cid{}
	}

	id := cpu.nextID()
	if id != 0 {
		t.Fatal("Should be 0.")
	}

}

func TestMClient_transformWant(t *testing.T) {
	bg := blocksutil.NewBlockGenerator()
	blks := bg.Blocks(1000)
	var keys []cid.Cid
	for _, blk := range blks {
		key := cid.NewCidV1(PsiCidCodec, blk.Cid().Hash())
		keys = append(keys, key)
	}
	cpu, _ := NewClientPsiUtil(group.Ristretto255, true)

	wg := sync.WaitGroup{}
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(t *testing.T) {
			defer wg.Done()
			for _, key := range keys {
				_, err := cpu.transformWant(key)
				if err != nil {
					t.Error("Transform failed.")
				}
			}
		}(t)
	}
	wg.Wait()
	if cpu.id != uint16(len(keys)) {
		t.Fatalf("Id error. %v\n", cpu.id)
	}
	if cpu.idCid[1] != keys[0] {
		t.Fatal("Wrong id-cid translation.\n")
	}
}

func TestMClient_removeWant(t *testing.T) {
	// This test ignores the possible translation race
	bg := blocksutil.NewBlockGenerator()
	blks := bg.Blocks(10)
	cpu, _ := NewClientPsiUtil(group.Ristretto255, true)
	var keys []cid.Cid
	for _, blk := range blks {
		key := cid.NewCidV1(PsiCidCodec, blk.Cid().Hash())
		enckey, err := cpu.transformWant(key)
		if err != nil {
			t.Fatalf("Failed to encrypt CID %v: %v\n", key, err)
		}
		keys = append(keys, enckey)
	}
	serverID := peer.ID("DummyPeerID")

	nblks := bg.Blocks(10)

	wg := sync.WaitGroup{}
	for i := 0; i < 3; i++ {
		wg.Add(1)
		if i%3 == 0 {
			go func(t *testing.T) {
				defer wg.Done()
				for _, key := range keys {
					_, _, err := cpu.transformRemoteDontHave(serverID, key)
					if err != nil {
						t.Errorf("Transform of %v failed: %v", key, err)
					}
				}
			}(t)
		} else if i%3 == 1 {
			go func(t *testing.T) {
				defer wg.Done()
				for _, key := range keys {
					cpu.removeWant(key)
				}
			}(t)
		} else {
			go func(t *testing.T) {
				defer wg.Done()
				for _, blk := range nblks {
					_, err := cpu.transformWant(cid.NewCidV1(PsiCidCodec, blk.Cid().Hash()))
					if err != nil {
						t.Errorf("Failed to encrypt CID %v: %v\n", blk.Cid(), err)
					}
				}
			}(t)
		}
	}
	wg.Wait()
}

func TestMClient_addRemoteHave(t *testing.T) {
	serverID := peer.ID("DummyPeerID")
	cpu, _ := NewClientPsiUtil(group.Ristretto255, true)
	bg := blocksutil.NewBlockGenerator()
	blks := bg.Blocks(10)
	var keys []cid.Cid
	for _, blk := range blks {
		key := cid.NewCidV1(PsiCidCodec, blk.Cid().Hash())
		keys = append(keys, key)
	}

	bf, err := bbloom.New(float64(10), FPR)
	if err != nil {
		t.Fatal("BF creation failed.")
	}
	for _, key := range keys {
		bf.Add(key.Bytes())
	}
	bbf := bf.JSONMarshal()
	mh, _ := multihash.Encode(bbf, 0xbf)
	bfCid := cid.NewCidV1(DummyCidCodecF, mh)
	keys = append(keys, bfCid)
	mh, _ = multihash.Encode(make([]byte, 0), 0xbf)
	wrongBfCid := cid.NewCidV1(DummyCidCodecF, mh)
	keys = append(keys, wrongBfCid)

	wg := sync.WaitGroup{}
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(t *testing.T) {
			defer wg.Done()
			errcount := 0
			for _, key := range keys {
				err = cpu.addRemoteHave(serverID, key)
				if err != nil {
					errcount++
				}
			}
			if errcount != 1 {
				t.Error(err)
			}
		}(t)
	}
	wg.Wait()
	if cpu.rHave[serverID].Len() != len(keys)-2 {
		t.Fatal("Wrong number of elements stored.")
	}
}

func TestMClient_Run(t *testing.T) {
	cpu, _ := NewClientPsiUtil(group.Ristretto255, true)
	cpu.rHClearDelay = 5 * time.Second
	bg := blocksutil.NewBlockGenerator()
	blks := bg.Blocks(10)
	var keys []cid.Cid
	for _, blk := range blks {
		key := cid.NewCidV1(PsiCidCodec, blk.Cid().Hash())
		keys = append(keys, key)
	}

	ctx := context.Background()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go cpu.Run(ctx)
	serverID := peer.ID("DummyPeerID")
	serverID2 := peer.ID("DummyPeerID2")
	for _, key := range keys {
		err := cpu.addRemoteHave(serverID, key)
		if err != nil {
			t.Fatal(err)
		}
		err = cpu.addRemoteHave(serverID2, key)
		if err != nil {
			t.Fatal(err)
		}
	}
	prHave, ok := cpu.rHave[serverID]
	if !ok {
		t.Fatal("Storage failed.")
	} else {
		if prHave.Len() != len(keys) {
			t.Fatal("Storage failed.")
		}
	}
	time.Sleep(2 * time.Second)
	for _, key := range keys {
		err := cpu.addRemoteHave(serverID2, key)
		if err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(4 * time.Second)
	_, ok = cpu.rHave[serverID]
	if ok {
		t.Fatal("Delete failed.")
	}
	_, ok = cpu.rHave[serverID2]
	if !ok {
		t.Fatal("Deleted too much.")
	}
	time.Sleep(5 * time.Second)
	_, ok = cpu.rHave[serverID2]
	if ok {
		t.Fatal("Delete fail.")
	}
}

func createOutBsMsg(cancel bool) bsmsg.BitSwapMessage {
	bg := blocksutil.NewBlockGenerator()
	blks := bg.Blocks(10)
	var keys []cid.Cid
	for _, blk := range blks {
		key := cid.NewCidV1(PsiCidCodec, blk.Cid().Hash())
		keys = append(keys, key)
	}

	msg := bsmsg.New(true)
	for _, key := range keys {
		if cancel {
			msg.Cancel(key)
		} else {
			msg.AddEntry(key, 1, bitswap_message_pb.Message_Wantlist_Have, true)
		}
	}

	return msg
}

func TestMClient_ManipulateOutgoing(t *testing.T) {
	serverID := peer.ID("DummyPeerID")
	cpu, _ := NewClientPsiUtil(group.Ristretto255, true)
	msg := createOutBsMsg(false)
	origWl := msg.Wantlist()
	cpu.ManipulateOutgoing(serverID, msg)

	if len(msg.Wantlist()) != len(origWl)+1 {
		t.Fatalf("Wants got lost. %v/%v", len(msg.Wantlist()), len(origWl))
	}
	if len(cpu.idCid) != len(origWl) && len(cpu.wantEnc) != len(origWl) && len(cpu.cidId) != len(origWl) {
		t.Fatal("Did not store enough wants.")
	}

	msg = createOutBsMsg(true)
	cpu.ManipulateOutgoing(serverID, msg)
	if len(cpu.idCid) != 0 && len(cpu.wantEnc) != 0 && len(cpu.cidId) != 0 {
		t.Fatal("Did not remove enough wants.")
	}

}

func createInBsMsg(wants []cid.Cid, haves []cid.Cid) bsmsg.BitSwapMessage {
	msg := bsmsg.New(true)
	for _, key := range wants {
		msg.AddDontHave(key)
	}
	for _, key := range haves {
		msg.AddHave(key)
	}

	return msg
}

func TestMClient_ManipulateIncoming(t *testing.T) {
	filter := true
	cpu, _ := NewClientPsiUtil(group.Ristretto255, filter)
	bg := blocksutil.NewBlockGenerator()
	blks := bg.Blocks(10)
	var wants []cid.Cid
	for _, blk := range blks {
		key := cid.NewCidV1(PsiCidCodec, blk.Cid().Hash())
		wants = append(wants, key)
	}
	var encwants []cid.Cid
	for _, key := range wants {
		encwant, err := cpu.transformWant(key)
		if err != nil {
			t.Fatal(err)
		}
		encwants = append(encwants, encwant)
	}

	spu, _ := NewServerPsiUtil(group.Ristretto255)

	var reencwants []cid.Cid
	for _, want := range encwants {
		dh, err := spu.TransformDontHave(want)
		if err != nil {
			t.Fatal(err)
		}
		reencwants = append(reencwants, dh)
	}
	codec := DummyCidCodecA
	if filter {
		codec = DummyCidCodecF
	}
	haves := spu.TransformHaves(codec, wants)
	serverID := peer.ID("DummyPeerID")
	msg := createInBsMsg(reencwants, haves) // Have + DontHave
	cpu.ManipulateIncoming(serverID, msg)
	if len(msg.Haves()) != 10 {
		t.Fatalf("Wrong number of haves. %v/10 \n", len(msg.Haves()))
	}
	if len(msg.DontHaves()) > 0 {
		t.Fatalf("Too many donthaves. %v/0 \n", len(msg.DontHaves()))
	}
	msg = createInBsMsg(reencwants, nil) // Only DontHave
	cpu.ManipulateIncoming(serverID, msg)
	if len(msg.Haves()) != 10 {
		t.Fatalf("Wrong number of haves. %v/10 \n", len(msg.Haves()))
	}
	if len(msg.DontHaves()) > 0 {
		t.Fatalf("Too many donthave. %v/0 \n", len(msg.DontHaves()))
	}
}

func TestMServer_transformHaves(t *testing.T) {
	bg := blocksutil.NewBlockGenerator()
	blks := bg.Blocks(1000)
	var keys []cid.Cid
	for _, blk := range blks {
		key := cid.NewCidV1(PsiCidCodec, blk.Cid().Hash())
		keys = append(keys, key)
	}
	spu, _ := NewServerPsiUtil(group.Ristretto255)

	wg := sync.WaitGroup{}
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(t *testing.T) {
			defer wg.Done()
			cids := spu.TransformHaves(DummyCidCodecF, keys)
			if len(cids) != 1 {
				t.Errorf("Should only return 1 Element")
			}
		}(t)
	}
	wg.Wait()
	if len(spu.haves) != len(keys) {
		t.Fatal("Not enough elements stored.\n")
	}
}

func TestM_transform(t *testing.T) {
	bg := blocksutil.NewBlockGenerator()
	blks := bg.Blocks(10)
	var wants []cid.Cid
	for _, blk := range blks {
		want := cid.NewCidV1(PsiCidCodec, blk.Cid().Hash())
		wants = append(wants, want)
	}
	cpu, _ := NewClientPsiUtil(group.Ristretto255, true)
	serverID := peer.ID("DummyPeerID")
	spu, _ := NewServerPsiUtil(group.Ristretto255)
	var encwants []cid.Cid
	for _, want := range wants {
		encwant, err := cpu.transformWant(want)
		if err != nil {
			t.Fatalf("Failed to encrypt CID %v: %v\n", want, err)
		}
		encwant, err = spu.TransformDontHave(encwant) // normally done by server
		if err != nil {
			t.Fatalf("Failed to encrypt CID %v: %v\n", want, err)
		}
		encwants = append(encwants, encwant)
	}

	enccid := spu.TransformHaves(DummyCidCodecA, wants[:1])
	if len(enccid) != 1 {
		t.Fatalf("Transform Have fail. %v", len(enccid))
	}
	err := cpu.addRemoteHave(serverID, enccid[0])
	if err != nil {
		t.Fatal(err)
	}

	wg := sync.WaitGroup{}
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(t *testing.T) {
			defer wg.Done()
			haves := 0
			donthaves := 0
			for _, enckey := range encwants {
				res, bpt, err := cpu.transformRemoteDontHave(serverID, enckey)
				if err != nil {
					t.Errorf("Transform of %v failed: %v", enckey, err)
				}
				if bpt == bitswap_message_pb.Message_Have {
					haves++
					if !bytes.Equal(res.Hash(), blks[0].Cid().Hash()) {
						t.Errorf("Translation fail.")
					}
				} else {
					donthaves++
				}
			}
			if donthaves != 9 || haves != 1 {
				t.Errorf("Matching fail. %v, %v", donthaves, haves)
			}
		}(t)
	}
	wg.Wait()
}

func Example_psiUtility() {
	bg := blocksutil.NewBlockGenerator()
	blks := bg.Blocks(20)
	var keys []cid.Cid
	for _, blk := range blks {
		key := cid.NewCidV1(PsiCidCodec, blk.Cid().Hash()) // mark as PSI request
		keys = append(keys, key)
	}

	m := 10
	n := 20
	C := keys[:m]
	S := keys[:n]

	V := cid.NewSet()
	W := cid.NewSet()
	X := cid.NewSet()

	// Client and Server need to use the same group
	cpu, err := NewClientPsiUtil(group.Ristretto255, false)
	if err != nil {
		panic(err)
	}

	spu, err := NewServerPsiUtil(group.Ristretto255)
	if err != nil {
		panic(err)
	}

	for _, wantHave := range C {
		encWantHave, err := cpu.transformWant(wantHave)
		if err != nil {
			panic(err)
		}
		V.Add(encWantHave) // Client sends encWantHave (V) to Server
	}

	serverID := peer.ID("DummyPeerID")
	sHaves := spu.TransformHaves(DummyCidCodecA, S)
	for _, shave := range sHaves {
		// Client receives shave (U)
		err = cpu.addRemoteHave(serverID, shave)
		if err != nil {
			panic(err)
		}
	}

	for _, wanthave := range V.Keys() {
		// Server does not know the encrypted WantHave, it becomes a DontHave -- reencrypted
		donthave, err := spu.TransformDontHave(wanthave)
		if err != nil {
			panic(err)
		}
		W.Add(donthave) // Server send reencrypted dontHaves to Client
	}

	for _, donthave := range W.Keys() {
		// Client decrypts dontHaves and compares with the received values
		deccid, mt, err := cpu.transformRemoteDontHave(serverID, donthave)
		if err != nil {
			panic(err)
		}
		if mt == bitswap_message_pb.Message_Have {
			X.Add(deccid)
		}
	}

	fmt.Printf("S and C have %v elements in common. \n", X.Len())
	// Output: S and C have 10 elements in common.

}
