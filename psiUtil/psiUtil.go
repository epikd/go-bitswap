package psiUtil

import (
	"context"
	"encoding/binary"
	"fmt"
	"sync"
	"time"

	"github.com/cloudflare/circl/group"
	"github.com/epikd/psiMagic"
	"github.com/ipfs/bbloom"
	bsmsg "github.com/ipfs/go-bitswap/message"
	bitswap_message_pb "github.com/ipfs/go-bitswap/message/pb"
	cid "github.com/ipfs/go-cid"
	logging "github.com/ipfs/go-log"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multicodec"
	"github.com/multiformats/go-multihash"
	multih "github.com/multiformats/go-multihash"
)

var log = logging.Logger("bitswap_network_psi")

const (
	// DST for ec cryptography - server and client need to use the same cyclicGroup and dst
	DSTP256      = "BISW-V01-CS01-with-P256_XMD:SHA-256_SSWU_RO_"
	DSTP384      = "BISW-V01-CS01-with-P384_XMD:SHA-384_SSWU_RO_"
	DSTP521      = "BISW-V01-CS01-with-P521_XMD:SHA-512_SSWU_RO_"
	DSTRistretto = "BISW-V01-CS01-with-ristretto255_XMD:SHA-512_R255MAP_RO_"
	// multi-codec identifier to identify a CID which should be handled with PSI
	PsiCidCodec = uint64(0xec)
	// multi-codec identifier to identify the DummyCID to request U of the server -- no filter
	DummyCidCodecA = uint64(0xca)
	// multi-codec identifier to identify the DummyCID to request U of the server -- Filter
	DummyCidCodecF = uint64(0xcf)
	// expected "max" false positive rate of the used filter
	FPR = float64(0.0001)
)

func selectConst(cyclicGroup group.Group) (string, uint64) {
	var dst string
	var hcode uint64
	switch cyclicGroup {
	case group.P256:
		dst = DSTP256
		hcode = uint64(multih.SHA2_256) // crypto.SHA256
	case group.P384:
		dst = DSTP384
		hcode = uint64(multih.SHA3_384) // crypto.SHA384
	case group.P521:
		dst = DSTP521
		hcode = uint64(multih.SHA2_512) // crypto.sha512
	case group.Ristretto255:
		dst = DSTRistretto
		hcode = uint64(multih.SHA2_512) // crypto.sha512
	default:
		return "", 0
	}

	return dst, hcode
}

// ClientPsiUtility allows the manipulation of incoming and outgoing BitswapMessages to realize PSI.
type ClientPsiUtil struct {

	// Elliptic Curve math
	pm psiMagic.PsiMage

	// Identifier for Want-Have request
	id uint16

	// whether to use filter for finding PSI matches
	// the filter potentially reduces traffic at the cost of a false-positive rate
	filter bool

	// Code of the hashing algorithm used by elliptic curve math - HashToElement
	// used as the multihash-code for the PSI CIDs
	hCode uint64

	// map for delete - CID-ID
	// Key: plain CID, Value: ID
	// for concurrencies -- wantlk
	cidId map[cid.Cid]uint16

	// map for "translating" PSI matches
	// Key: ID, Value: plain CID
	// for concurrencies -- wantlk
	idCid map[uint16]cid.Cid

	// map to speed up consecutive requests
	// Key: plain CID, Value encrypted CID with ID. (V)
	// for concurrencies -- wantlk
	wantEnc map[cid.Cid]cid.Cid

	// encrypted haves of other peers (U)
	// for concurrencies -- rhavelk
	rHave  map[peer.ID]*cid.Set
	rHaveF map[peer.ID]*bbloom.Bloom

	// Locks
	idlk    sync.Mutex
	wantlk  sync.RWMutex
	rHavelk sync.RWMutex

	// RemoteHave cleanup variables
	// periodic cleanup reduces memory overhead at the cost of additional traffic
	rHTimer      *time.Timer
	rHClearDelay time.Duration
	rHClear      map[peer.ID]time.Time
}

// Creates and returns a new ClientPsiUtility.
// cyclicGroup determines the used cryptographic group - supports ristretto255, P256, P384, and P521
// filter determines if a filter should be used for requests - see ClientPsiUtil -> filter
func NewClientPsiUtil(cyclicGroup group.Group, filter bool) (*ClientPsiUtil, error) {

	dst, hcode := selectConst(cyclicGroup)
	mage, err := psiMagic.CreateWithNewKey(cyclicGroup, dst)
	if err != nil {
		return &ClientPsiUtil{}, err
	}

	cpu := &ClientPsiUtil{
		pm:           mage,
		hCode:        hcode,
		id:           1,
		cidId:        make(map[cid.Cid]uint16),
		idCid:        make(map[uint16]cid.Cid),
		wantEnc:      make(map[cid.Cid]cid.Cid),
		rHave:        make(map[peer.ID]*cid.Set),
		rHaveF:       make(map[peer.ID]*bbloom.Bloom),
		filter:       filter,
		rHClearDelay: 60 * time.Second,
		rHClear:      make(map[peer.ID]time.Time),
	}

	return cpu, nil
}

// Get the next available Want-Have identifier.
// Throws an error if all available wants are taken.
// The id is not peer specific. Zero indicates the ids are all used.
// This restricts the maximum number of pending "PSI" Want-Haves to (2^16)-2.
func (cpu *ClientPsiUtil) nextID() uint16 {
	cpu.idlk.Lock()
	defer cpu.idlk.Unlock()
	oid := cpu.id
	nid := cpu.id
	_, ok := cpu.idCid[nid]
	for ok {
		nid++
		if nid == 0 {
			nid = 1
		}
		_, ok = cpu.idCid[nid]
		if nid == oid {
			return 0
		}
	}
	cpu.id = nid

	return nid
}

// Transforms (if necessary) the CID into an encrypted CID with an added ID.
// Want is the CID to be transformed
// Returns the encrypted CID with id.
// In case of an error the plain CID is returned with an error.
// The id is necessary to map the returned re-encrypted value to the original CID.
// Only the multihash of the CID is encrypted.
// (take c_i, get v_i, store i in v)
func (cpu *ClientPsiUtil) transformWant(want cid.Cid) (cid.Cid, error) {
	cpu.wantlk.RLock()
	enccid, ok := cpu.wantEnc[want] // see if cid is pending
	if !ok {
		// New CID -- encrypt and store translation
		cpu.wantlk.RUnlock()
		cpu.wantlk.Lock()
		// Check if we still need to encrypt, since aqcuiring the lock.
		if enccid, ok = cpu.wantEnc[want]; ok {
			cpu.wantlk.Unlock()
			return enccid, nil
		}
		// get ID
		id := cpu.nextID()
		if id == 0 {
			return want, fmt.Errorf("Too many outstanding PSI wants")
		}
		// create encrypted point from multihash
		encp, err := cpu.pm.Encrypt(want.Hash())
		if err != nil {
			return want, fmt.Errorf("Fail (%v): %w", want, err)
		}
		// combine encrypted point and id to a multihash
		var encmhb []byte
		bid := make([]byte, 2)
		binary.BigEndian.PutUint16(bid, id)
		encmhb = append(encmhb, bid...)
		encmhb = append(encmhb, encp...)
		encmhb, _ = multihash.Encode(encmhb, cpu.hCode)
		_, encmh, err := multihash.MHFromBytes(encmhb)
		if err != nil {
			return want, fmt.Errorf("Fail (%v): %w", want, err)
		}
		// Create CID from multihash
		enccid = cid.NewCidV1(PsiCidCodec, encmh)

		// store translation
		cpu.wantEnc[want] = enccid
		cpu.idCid[id] = want
		cpu.cidId[want] = id

		cpu.wantlk.Unlock()
	} else {
		cpu.wantlk.RUnlock()
	}

	return enccid, nil
}

// Transform a received DontHave into a Have/DontHave based on the PSI result.
// p is the peer.ID of the peer that supposedly re-encrypted the CID
// dontHave is expected to be the re-encrypted CID.
// Returns the decrypted CID and Have/DontHave PSI result.
// If something went wrong the CID is returned as is and determined as DontHave along with an error.
// (Take w_i, get x_i, and "i" check if x_i element of U)
func (cpu *ClientPsiUtil) transformRemoteDontHave(p peer.ID, donthave cid.Cid) (cid.Cid, bitswap_message_pb.Message_BlockPresenceType, error) {
	mt := bitswap_message_pb.Message_DontHave
	// determine point position
	pstart := donthave.ByteLen() - int(cpu.pm.Group().Params().CompressedElementLength)
	if pstart < 0 {
		return donthave, mt, fmt.Errorf("CID too short.")
	}
	id := binary.BigEndian.Uint16(donthave.Bytes()[(pstart - 2):pstart]) // extract ID
	bdecdig, err := cpu.pm.Decrypt(donthave.Bytes()[pstart:])            // decrypt point
	if err != nil {
		return donthave, mt, fmt.Errorf("Fail %v: %w", donthave, err)
	}
	decmh, _ := multihash.Encode(bdecdig, cpu.hCode)
	deccid := cid.NewCidV1(PsiCidCodec, decmh)

	cpu.wantlk.RLock()
	origcid, ok := cpu.idCid[id]
	cpu.wantlk.RUnlock()
	if !ok {
		// This can happen if e.g. a peer is slow to respond and the block was received from another peer.
		return donthave, mt, fmt.Errorf("Real value no longer present.")
	}

	cpu.rHavelk.RLock()
	defer cpu.rHavelk.RUnlock()
	// Lookup all
	prhave, ok := cpu.rHave[p]
	if ok {
		if prhave.Has(deccid) {
			mt = bitswap_message_pb.Message_Have
		}
	} else {
		// Lookup filter
		rbf, ok := cpu.rHaveF[p]
		if ok {
			if rbf.Has(deccid.Hash()) {
				mt = bitswap_message_pb.Message_Have
			}
		}
	}

	return origcid, mt, nil
}

// Clean up completed wants, frees the id for new PSI Want-Haves.
// Can be called when receiving a block or sending a cancel
// Nobody else should be allowed to delete only add.
// Expect the unaltered CID
func (cpu *ClientPsiUtil) removeWant(plain cid.Cid) {
	cpu.wantlk.Lock()
	id := cpu.cidId[plain]
	delete(cpu.wantEnc, plain)
	delete(cpu.cidId, plain)
	delete(cpu.idCid, id)
	cpu.wantlk.Unlock()
}

// Adds a received have to storage.
// p is the peer.ID of the sender of the CID
// have is the received CID. It should be the encrypted CID or the bloomfilter
// For PSI to work, the cyclicGroup used to encrypt the CID should be the same as the one used by the Client
// The function stores almost everything and does not perform sanity checks.
// (store u_i)
func (cpu *ClientPsiUtil) addRemoteHave(p peer.ID, have cid.Cid) error {

	codec := have.Prefix().Codec
	if codec == PsiCidCodec {
		cpu.rHavelk.Lock()
		prHave, ok := cpu.rHave[p]
		if !ok {
			prHave = cid.NewSet()
			prHave.Add(have)
			cpu.rHave[p] = prHave
		}
		cpu.rHClear[p] = time.Now().Add(cpu.rHClearDelay)
		prHave.Add(have)
		cpu.rHavelk.Unlock()
	} else if codec == DummyCidCodecF {
		// Could be a Bloom filter try to extract the filter from the CID
		decmh, err := multihash.Decode(have.Hash())
		if err != nil {
			return fmt.Errorf("Failed MH decoding. %w", err)
		}
		bf, err := bbloom.JSONUnmarshal(decmh.Digest)
		if err != nil {
			return fmt.Errorf("%v seems like a BF but does not contain a BF", have)
		}
		cpu.rHavelk.Lock()
		cpu.rHaveF[p] = bf
		cpu.rHavelk.Unlock()
	}
	return nil
}

// Checks if node has information about p's stored CIDs.
func (cpu *ClientPsiUtil) requestHave(p peer.ID) bool {
	req := true
	cpu.rHavelk.RLock()
	_, okA := cpu.rHave[p]
	_, okF := cpu.rHaveF[p]
	cpu.rHavelk.RUnlock()
	if okA || okF {
		req = false
	}
	return req
}

// ManipulateOutgoing manipulates an outgoing message.
// Marked CIDs are CIDs with specific multi-codec identifier.
// Replaces marked CID Want-Have with encrypted values (keep Want-Block)
// Removes marked CID Want-Have Cancel (keep WANT-BLOCK) from the message and from internal memory
// Add Want-Have of DummyCID if necessary
// TODO check if bitswap really sends CANCEL_WANT_HAVE or if all get a CANCEL_WANT_BLOCK -- would leak information
func (cpu *ClientPsiUtil) ManipulateOutgoing(p peer.ID, msg bsmsg.BitSwapMessage) {
	rw := cid.NewSet() // candidates for removeWant
	psiwh := false     // PSI request?
	wants := msg.Wantlist()
	for _, want := range wants {
		// WANT-HAVE
		codec := want.Cid.Prefix().Codec
		if want.WantType == bitswap_message_pb.Message_Wantlist_Have && codec == PsiCidCodec {
			if want.Cancel {
				// PSI WANT-HAVE do not need to be canceled, should not even be stored in the remote Ledger
				// A match would be a strange coincidence
				// Sending a cancel means, the CID is no longer of interest -> mark for deletion
				rw.Add(want.Cid)
				msg.Remove(want.Cid)
			} else {
				enccid, err := cpu.transformWant(want.Cid)
				if err != nil {
					enccid = cid.NewCidV1(uint64(multicodec.Raw), enccid.Hash())
					log.Infof("Making normal request for %v. Failed to transfrom: %w", want.Cid, err)
				}
				msg.Remove(want.Cid)
				msg.AddEntry(enccid, want.Priority, want.WantType, true)
				psiwh = true
			}
		} else if want.WantType == bitswap_message_pb.Message_Wantlist_Block && codec == PsiCidCodec && want.Cancel {
			// Sending a cancel means, the CID is no longer of interest mark for deletion
			// Want-Block contains the "real" CID which would be stored in the ledger. Therefore it can be kept.
			// If all cancels are CANCEL_WANT_BLOCK remove this cancel. Otherwise it would leak information to other peers.
			rw.Add(want.Cid)
			//msg.Remove(want.Cid)
		}
	}
	// Free Ids based on cancels
	for _, key := range rw.Keys() {
		cpu.removeWant(key)
	}

	// Request U if necessary
	if psiwh && cpu.requestHave(p) {
		codec := uint64(DummyCidCodecA)
		if cpu.filter {
			codec = DummyCidCodecF
		}
		mh, _ := multihash.Encode(make([]byte, 0), cpu.hCode)
		dummycid := cid.NewCidV1(codec, mh)
		msg.AddEntry(dummycid, 1, bitswap_message_pb.Message_Wantlist_Have, false) // DummyCID symbolizing request all
	}
}

// ManipulateIncoming manipulates an incoming message.
// Marked CIDs are CIDs with special multi-codec identifier.
// Haves of marked CIDs are stored for PSI results calculation.
// DontHave of marked CIDs are transformed into HAVE/DontHave based on the PSI result.
func (cpu *ClientPsiUtil) ManipulateIncoming(p peer.ID, msg bsmsg.BitSwapMessage) {
	var dh []cid.Cid
	for _, bp := range msg.BlockPresences() {
		codec := bp.Cid.Prefix().Codec
		// Only marked CIDs
		if bp.Type == bitswap_message_pb.Message_Have && (codec == PsiCidCodec || codec == DummyCidCodecF) {
			msg.RemoveBP(bp.Cid)
			err := cpu.addRemoteHave(p, bp.Cid)
			if err != nil {
				log.Infof("Did not store: %v. %w", bp.Cid, err)
			}
		} else if bp.Type == bitswap_message_pb.Message_DontHave && codec == PsiCidCodec {
			dh = append(dh, bp.Cid) // Wait till all Have from the message are stored.
		}
	}
	for _, key := range dh {
		deccid, bpt, err := cpu.transformRemoteDontHave(p, key)
		if err != nil {
			log.Infof("Failed to transform: %w", key, err)
		}
		msg.RemoveBP(key)
		msg.AddBlockPresence(deccid, bpt)
	}
}

// Deal with periodic taks. Needs to be started externally.
func (pu *ClientPsiUtil) Run(ctx context.Context) {
	pu.rHTimer = time.NewTimer(pu.rHClearDelay)
	for {
		select {
		case t := <-pu.rHTimer.C:
			pu.rHCleanup(t)
		case <-ctx.Done():
			return
		}
	}

}

// Free up memory and force client to request stored data again.
// Serves also for refreshing purposes. New blocks might be announced but deletions are not
func (pu *ClientPsiUtil) rHCleanup(t time.Time) {
	pu.rHavelk.Lock()
	var delpid []peer.ID
	for pid, rht := range pu.rHClear {
		if t.After(rht) {
			delpid = append(delpid, pid)
		}
	}
	for _, pid := range delpid {
		delete(pu.rHClear, pid)
		delete(pu.rHave, pid)
		delete(pu.rHaveF, pid)
	}
	pu.rHTimer.Reset(pu.rHClearDelay)
	pu.rHavelk.Unlock()
}

// ServerPsiUtil allows the manipultation and management of CID.
// It encrypts CID and stores CIDs in a map or Bloom Filter.
type ServerPsiUtil struct {

	// Elliptic Curve math
	pm psiMagic.PsiMage

	// Code of the hashing algorithm used by elliptic curve math - HashToElement
	// used as the multihash-code for the PSI CIDs
	hCode uint64

	// Map to skip consecutive encryption
	// Key: plain have, Value encrypted have. (U for others)
	// for concurrencies -- havelk
	haves map[cid.Cid]cid.Cid

	// A Bloom filter containing the encrypted CIDs
	// The Bloom filter is created in 50 element steps with a fixed false-positive rate
	// for concurrencies -- havelk
	haveF *bbloom.Bloom

	havelk sync.RWMutex
}

// Creates and returns a new ServerPsiUtility.
// cyclicGroup determines the used cryptographic group - supports ristretto255, P256, P384, and P521
func NewServerPsiUtil(cyclicGroup group.Group) (*ServerPsiUtil, error) {
	var err error
	dst, hcode := selectConst(cyclicGroup)
	mage, err := psiMagic.CreateWithNewKey(cyclicGroup, dst)
	if err != nil {
		return &ServerPsiUtil{}, err
	}

	emptyBloom, err := bbloom.New(float64(10), FPR)
	if err != nil {
		return &ServerPsiUtil{}, err
	}

	return &ServerPsiUtil{
		pm:    mage,
		hCode: hcode,
		haves: make(map[cid.Cid]cid.Cid),
		haveF: emptyBloom,
	}, nil
}

// Transforms and stores (if necessary) multiple CIDs.
// The transformation is into an encrypted CID.
// Only the multihash is transformed.
// Codec is the multi-codec identfier to determine the return value -- DummyCidCodecA -- all stored CIDs, DummyCidCodecF -- filter containing all stored CIDs
// haves are the CIDs to be stored/encrypted
// Returns the encrypted CIDs or a filter depending on codec.
// In the case that the encryption of a CID fails the CID is ignored.
// (take s_i, get u_i)
func (spu *ServerPsiUtil) TransformHaves(codec uint64, haves []cid.Cid) []cid.Cid {
	if len(haves) < 1 {
		return haves
	}
	var encHaves []cid.Cid

	bfnew := false
	spu.havelk.Lock()
	for _, have := range haves {
		_, ok := spu.haves[have] // see if encryption already exists
		if !ok {
			// New CID -- encrypt and store translation
			// Encrypt Multihash
			encmhb, err := spu.pm.Encrypt(have.Hash())
			if err != nil {
				continue
			}
			// Encode point as multihash
			encmhb, _ = multihash.Encode(encmhb, spu.hCode)
			_, nencmh, err := multihash.MHFromBytes(encmhb)
			if err != nil {
				continue
			}
			// Create CID from multihash
			enccid := cid.NewCidV1(PsiCidCodec, nencmh)
			spu.haves[have] = enccid
			bfnew = true
		}
	}
	spu.havelk.Unlock()
	if bfnew {
		spu.havelk.Lock()
		puffer := 50 - len(spu.haves)%50 // slightly obfuscate real amount
		m := puffer + len(spu.haves)
		bf, err := bbloom.New(float64(m), FPR)
		if err != nil {
			return encHaves
		}
		for _, key := range spu.haves {
			bf.Add(key.Hash())
		}
		spu.haveF = bf
		spu.havelk.Unlock()
	}
	spu.havelk.RLock()
	if codec == DummyCidCodecA {
		for _, key := range spu.haves {
			encHaves = append(encHaves, key)
		}
	} else if codec == DummyCidCodecF {
		bbf := spu.haveF.JSONMarshal()
		mh, _ := multihash.Encode(bbf, 0xbf)
		enccid := cid.NewCidV1(DummyCidCodecF, mh)
		encHaves = append(encHaves, enccid)
	}
	spu.havelk.RUnlock()
	return encHaves
}

// Transforms an encrypted CID into a re-encrypted CID.
// enc is a supposedly encrypted CID.
// Returns the re-encrypted CID
// If something went wrong the CID is returned with an error
// The function does not perform intensive sanity checks
func (spu *ServerPsiUtil) TransformDontHave(enc cid.Cid) (cid.Cid, error) {
	// find and only reencrypt point
	pstart := enc.ByteLen() - int(spu.pm.Group().Params().CompressedElementLength)
	if pstart < 0 {
		return enc, fmt.Errorf("The CID (%v) is too short.", enc)
	}
	brencp, err := spu.pm.ReEncrypt(enc.Bytes()[pstart:])
	if err != nil {
		return enc, fmt.Errorf("Fail %v: %w", enc, err)
	}
	// keep rest of CID as is
	brenccid := append(enc.Bytes()[:pstart], brencp...)
	_, renccid, err := cid.CidFromBytes(brenccid)
	if err != nil {
		return enc, fmt.Errorf("Fail %v: %w", enc, err)
	}

	return renccid, nil
}
