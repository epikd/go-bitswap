package psiUtil

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/ipfs/bbloom"
	bsmsg "github.com/ipfs/go-bitswap/message"
	bitswap_message_pb "github.com/ipfs/go-bitswap/message/pb"
	cid "github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multihash"
)

type ClientUtil interface {
	ManipulateIncoming(peer.ID, bsmsg.BitSwapMessage)
	ManipulateOutgoing(peer.ID, bsmsg.BitSwapMessage) bsmsg.BitSwapMessage
	ClearHaves()
}

type ServerUtil interface {
	TransformDontHave(cid.Cid) (cid.Cid, error)
	TransformHaves(codec uint64, haves []cid.Cid) []cid.Cid
	ClearHaves()
}

type DefaultUtil struct{}

func (*DefaultUtil) ManipulateIncoming(peer.ID, bsmsg.BitSwapMessage) {}
func (*DefaultUtil) ManipulateOutgoing(peer.ID, bsmsg.BitSwapMessage) bsmsg.BitSwapMessage {
	return nil
}
func (*DefaultUtil) ClearHaves() {}

func (*DefaultUtil) TransformDontHave(c cid.Cid) (cid.Cid, error) {
	return c, nil
}
func (*DefaultUtil) TransformHaves(codec uint64, haves []cid.Cid) []cid.Cid {
	return haves
}

const DummyCodecBF = uint64(0xbf)

type ClientFilterUtil struct {

	// Pending Want
	pending map[peer.ID]*cid.Set

	// haves of other peers (U)
	// for concurrencies -- rhavelk
	rHaveF map[peer.ID]*bbloom.Bloom

	// Locks
	rHavelk   sync.RWMutex
	pendinglk sync.Mutex

	// RemoteHave cleanup variables
	// periodic cleanup reduces memory overhead at the cost of additional traffic
	rHTimer *time.Timer
	pTimer  *time.Timer

	rHClearDelay time.Duration
	rHClear      map[peer.ID]time.Time
	pClearDelay  time.Duration
	pClear       map[peer.ID]time.Time
}

func NewClientFilterUtil() *ClientFilterUtil {

	cfu := &ClientFilterUtil{
		pending:      make(map[peer.ID]*cid.Set),
		rHaveF:       make(map[peer.ID]*bbloom.Bloom),
		rHClearDelay: defaultClearDelay,
		rHClear:      make(map[peer.ID]time.Time),
		pClearDelay:  defaultClearDelay,
		pClear:       make(map[peer.ID]time.Time),
	}

	return cfu
}

// Adds a received have to storage.
// p is the peer.ID of the sender of the CID
// have is the received CID. It should be the Bloom filter
// The function stores almost everything and does not perform sanity checks.
// (store u_i)
func (cfu *ClientFilterUtil) addRemoteHave(p peer.ID, have cid.Cid) error {
	if have.Prefix().Codec == DummyCodecBF {
		// Could be a Bloom filter try to extract the filter from the CID
		decmh, err := multihash.Decode(have.Hash())
		if err != nil {
			return fmt.Errorf("Failed MH decoding. %w", err)
		}

		bf, err := bbloom.JSONUnmarshal(decmh.Digest)
		if err != nil {
			return fmt.Errorf("%v claims to be a BF but does not contain a BF", have)
		}
		cfu.rHavelk.Lock()
		cfu.rHaveF[p] = bf
		cfu.rHClear[p] = time.Now().Add(cfu.rHClearDelay)
		cfu.rHavelk.Unlock()
	} else {
		log.Infof("Received a normal CID. %v", have.Prefix().Codec)
	}
	return nil
}

// ManipulateOutgoing manipulates an outgoing message.
// If necessary add DummyCID
// Else send result of Bloom Filter lookup
// Remove Want-Haves
// necessary -- storing Bloom Filter of p
func (cfu *ClientFilterUtil) ManipulateOutgoing(p peer.ID, msg bsmsg.BitSwapMessage) bsmsg.BitSwapMessage {
	var have []cid.Cid
	var dhave []cid.Cid
	var bf *bbloom.Bloom
	wh := false
	// Request if necessary
	cfu.rHavelk.RLock()
	_, rinfo := cfu.rHaveF[p]
	if rinfo {
		bf = cfu.rHaveF[p]
	} else {
		cfu.rHavelk.RUnlock()
	}
	wants := msg.Wantlist()
	for _, want := range wants {
		// WANT-HAVE
		if want.WantType == bitswap_message_pb.Message_Wantlist_Have {
			if rinfo {
				has := bf.Has(want.Cid.Hash())
				if has {
					have = append(have, want.Cid)
				} else {
					dhave = append(dhave, want.Cid)
				}
			} else {
				cfu.pendinglk.Lock()
				pending, ok := cfu.pending[p]
				if !ok {
					set := cid.NewSet()
					cfu.pending[p] = set
					pending = set
				}
				pending.Add(want.Cid)
				cfu.pClear[p] = time.Now().Add(cfu.pClearDelay)
				cfu.pendinglk.Unlock()
			}
			msg.Remove(want.Cid)
			wh = true
		}
	}
	if rinfo {
		cfu.rHavelk.RUnlock()
	} else if wh {
		mh, _ := multihash.Encode([]byte(nil), 0xbf)
		dummycid := cid.NewCidV1(DummyCodecBF, mh)
		msg.AddEntry(dummycid, 1, bitswap_message_pb.Message_Wantlist_Have, false) // DummyCID symbolizing request all
	}

	if len(have) > 0 || len(dhave) > 0 {
		answer := bsmsg.New(msg.Full())
		for _, key := range have {
			answer.AddBlockPresence(key, bitswap_message_pb.Message_Have)
		}
		for _, key := range dhave {
			answer.AddBlockPresence(key, bitswap_message_pb.Message_DontHave)
		}
		return answer
	}
	return nil
}

// ManipulateIncoming manipulates an incoming message.
// Haves are temporarly stored for Bloom Filter lookup.
func (cfu *ClientFilterUtil) ManipulateIncoming(p peer.ID, msg bsmsg.BitSwapMessage) {
	for _, bp := range msg.BlockPresences() {
		if bp.Type == bitswap_message_pb.Message_Have {
			msg.RemoveBP(bp.Cid)
			err := cfu.addRemoteHave(p, bp.Cid)
			if err != nil {
				log.Infof("Did not store: %v. %w", bp.Cid, err)
			}
		}
	}

	cfu.pendinglk.Lock()
	pending, ok := cfu.pending[p]
	if !ok {
		cfu.pendinglk.Unlock()
		return
	}
	cfu.rHavelk.Lock()
	bf, okF := cfu.rHaveF[p]
	if !okF {
		log.Infof("Still no valid filter received from %v.", p.Pretty())
		cfu.rHavelk.Unlock()
		cfu.pendinglk.Unlock()
		return
	}
	for _, key := range pending.Keys() {
		has := bf.Has(key.Hash())
		if has {
			msg.AddBlockPresence(key, bitswap_message_pb.Message_Have)
		} else {
			msg.AddBlockPresence(key, bitswap_message_pb.Message_DontHave)
		}
	}
	cfu.rHavelk.Unlock()
	delete(cfu.pending, p)
	cfu.pendinglk.Unlock()
}

// Deal with periodic taks. Needs to be started externally.
func (cfu *ClientFilterUtil) Run(ctx context.Context) {
	cfu.rHTimer = time.NewTimer(cfu.rHClearDelay)
	cfu.pTimer = time.NewTimer(cfu.pClearDelay)
	for {
		select {
		case t := <-cfu.rHTimer.C:
			cfu.rHCleanup(t)
		case t := <-cfu.pTimer.C:
			cfu.pCleanup(t)
		case <-ctx.Done():
			return
		}
	}

}

// Free up memory of old entries, forces client to request stored data again.
// Serves also for refreshing purposes. New blocks might be announced but deletions are not
func (cfu *ClientFilterUtil) rHCleanup(t time.Time) {
	cfu.rHavelk.Lock()
	var delpid []peer.ID
	for pid, rht := range cfu.rHClear {
		if t.After(rht) {
			delpid = append(delpid, pid)
		}
	}
	for _, pid := range delpid {
		delete(cfu.rHClear, pid)
		delete(cfu.rHaveF, pid)
	}
	cfu.rHTimer.Reset(cfu.rHClearDelay)
	cfu.rHavelk.Unlock()
}

// Free up memory, forces client to request stored data again.
// Manual delete of all stored Haves
func (cfu *ClientFilterUtil) ClearHaves() {
	cfu.rHavelk.Lock()
	cfu.rHClear = make(map[peer.ID]time.Time)
	cfu.rHaveF = make(map[peer.ID]*bbloom.Bloom)
	cfu.rHavelk.Unlock()
}

// Free up memory and force client to request stored data again.
// Serves also for refreshing purposes. New blocks might be announced but deletions are not
func (cfu *ClientFilterUtil) pCleanup(t time.Time) {
	cfu.pendinglk.Lock()
	var delpid []peer.ID
	for pid, pt := range cfu.pClear {
		if t.After(pt) {
			delpid = append(delpid, pid)
		}
	}
	for _, pid := range delpid {
		delete(cfu.pClear, pid)
		delete(cfu.pending, pid)
	}
	cfu.pTimer.Reset(cfu.pClearDelay)
	cfu.pendinglk.Unlock()
}

// ServerFilterUtil allows the manipultation and management of CID.
// It stores CIDs in a set and Bloom filter.
type ServerFilterUtil struct {

	// Set to check whether a new Bloom filter needs to be created.
	// Key: plain have, Value encrypted have. (U for others)
	// for concurrencies -- havelk
	haves *cid.Set

	// CID of a Bloom filter containing the CIDs
	// The Bloom filter is created in 50 element steps with a fixed false-positive rate
	// for concurrencies -- havelk
	haveF cid.Cid

	havelk sync.RWMutex
}

// Creates and returns a new ServerBloomUtil.
func NewServerFilterUtil() *ServerFilterUtil {
	return &ServerFilterUtil{
		haves: cid.NewSet(),
	}
}

// Returns the Bloom filter with all haves.
func (sfu *ServerFilterUtil) TransformHaves(codec uint64, haves []cid.Cid) []cid.Cid {
	if len(haves) < 1 {
		return haves
	}
	var retHaves []cid.Cid

	fnew := false
	sfu.havelk.Lock()
	for _, have := range haves {
		ok := sfu.haves.Visit(have)
		if ok {
			fnew = true
		}
	}
	if len(haves) != sfu.haves.Len() {
		newSet := cid.NewSet()
		for _, have := range haves {
			newSet.Add(have)
		}
		sfu.haves = newSet
	}
	if fnew {
		// recreate the whole filter
		margin := 50 - sfu.haves.Len()%50 // slightly obfuscate real amount
		m := margin + sfu.haves.Len()
		bf, err := bbloom.New(float64(m), FPR)
		if err != nil {
			sfu.havelk.Unlock()
			return retHaves
		}
		for _, key := range haves {
			bf.Add(key.Hash())
		}
		bbf := bf.JSONMarshal()
		mh, _ := multihash.Encode(bbf, DummyCodecBF)
		bfcid := cid.NewCidV1(DummyCodecBF, mh)
		sfu.haveF = bfcid
	}
	if codec == DummyCodecBF {
		retHaves = append(retHaves, sfu.haveF)
	}

	sfu.havelk.Unlock()
	return retHaves
}

// For interface
func (sfu *ServerFilterUtil) TransformDontHave(plain cid.Cid) (cid.Cid, error) {
	return plain, nil
}

// Clear stored Haves.
func (sfu *ServerFilterUtil) ClearHaves() {
	sfu.havelk.Lock()
	sfu.haves = cid.NewSet()
	sfu.havelk.Unlock()
}
