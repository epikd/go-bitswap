package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"time"

	"github.com/cloudflare/circl/group"
	"github.com/epikd/psiMagic"
	bitswap "github.com/ipfs/go-bitswap"
	bsmsg "github.com/ipfs/go-bitswap/message"
	bitswap_message_pb "github.com/ipfs/go-bitswap/message/pb"
	bsnet "github.com/ipfs/go-bitswap/network"
	"github.com/ipfs/go-bitswap/psiUtil"
	"github.com/ipfs/go-cid"
	"github.com/ipfs/go-datastore"
	blockstore "github.com/ipfs/go-ipfs-blockstore"
	blocksutil "github.com/ipfs/go-ipfs-blocksutil"
	logging "github.com/ipfs/go-log/v2"
	libp2p "github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	libp2pcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
	"github.com/multiformats/go-multibase"
	"github.com/multiformats/go-multihash"
)

var clog = logging.Logger("client")
var cbsk = []byte{76, 38, 10, 153, 182, 9, 75, 252, 63, 114, 61, 120, 73, 218, 243, 11, 199, 184, 194, 203, 163, 184, 210, 254, 112, 174, 166, 228, 139, 240, 178, 135, 189, 81, 133, 230, 136, 31, 126, 48, 19, 17, 98, 56, 118, 248, 57, 143, 220, 106, 87, 26, 90, 99, 113, 10, 166, 10, 171, 253, 77, 252, 169, 23}

func main() {
	ip := "127.0.0.1"
	sip := "127.0.0.1"
	cport := 3335
	mport := 3336

	tcp := false
	psi := false
	filter := false

	ccount := 100
	acount := 1
	adelay := 20 * time.Millisecond
	cdelay := 1 * time.Second
	// Server information
	var sma multiaddr.Multiaddr
	if tcp {
		ma := fmt.Sprintf("/ip4/%v/tcp/3333", sip)
		sma, _ = multiaddr.NewMultiaddr(ma)
	} else {
		ma := fmt.Sprintf("/ip4/%v/udp/3334/quic", sip)
		sma, _ = multiaddr.NewMultiaddr(ma)
	}
	sid, err := peer.Decode("12D3KooWDLHSn3Hbkq6JBZduAqCkDNAQXjrqqizxud78urr3EifB")
	if err != nil {
		fmt.Println(err)
		return
	}
	sai := peer.AddrInfo{Addrs: []multiaddr.Multiaddr{sma}, ID: sid}

	// Logging
	fp := filepath.Join(".", "client.log")
	logf, err := os.Create(fp)
	if err != nil {
		fmt.Println(err)
	}
	defer logf.Close()
	logcfg := logging.GetConfig()
	logcfg.File = filepath.Clean(fp)
	logcfg.Format = logging.JSONOutput
	logcfg.Level = logging.LevelInfo
	logcfg.Stderr = false
	logcfg.Stdout = false
	logging.SetupLogging(logcfg)

	cin := make(chan bool)
	cclient := make(chan bool)
	cattack := make(chan bool)
	cerr := make(chan error)

	fmt.Printf("PSI=%v,Filter=%v\n", psi, filter)
	var resname string
	for i := 1; i < 10; i++ {
		resname = fmt.Sprintf("result-%v-%v-%v-%v", psi, filter, acount, i)
		resname = filepath.Join(".", resname)
		_, err := os.Stat(resname)
		if err != nil && errors.Is(err, os.ErrNotExist) {
			break
		} else if err != nil {
			fmt.Println(err)
			return
		}
	}

	go runRequest(cin, cclient, cerr, ccount, ip, cport, tcp, filter, psi, sai, resname)
	go runAttack(cin, cattack, cerr, acount, adelay, ip, mport, tcp, sai)

	for i := 0; i < 2; {
		select {
		case r := <-cin:
			if r {
				fmt.Println("Signal ready")
				i++
			} else {
				fmt.Println("Something went wrong.")
				return
			}
		case err := <-cerr:
			fmt.Println(err)
			return
		}
	}

	cattack <- true
	select {
	case r := <-cin:
		if r {
			fmt.Println("Signal finish")
		} else {
			fmt.Println("Something went wrong.")
			return
		}
	case err := <-cerr:
		fmt.Println(err)
		return
	}
	time.Sleep(cdelay)
	cclient <- true
	select {
	case r := <-cin:
		if r {
			fmt.Println("Signal finish")
		} else {
			fmt.Println("Something went wrong.")
			return
		}
	case err := <-cerr:
		fmt.Println(err)
		return
	}
	fmt.Println("finished")
}

func runRequest(c1 chan bool, c2 chan bool, cerr chan error, count int, ip string, port int, tcp bool, filter bool, psi bool, sai peer.AddrInfo, resname string) {

	fmt.Println("running Bitswap-Client")
	ctx := context.Background()

	// use always same identity
	sk, err := libp2pcrypto.UnmarshalEd25519PrivateKey(cbsk)
	if err != nil {
		cerr <- err
		return
	}

	// Read Block CIDs
	var cidstr []string
	fis, err := ioutil.ReadDir("./blk")
	for i, fi := range fis {
		if i >= count {
			break
		}
		cidstr = append(cidstr, fi.Name())
	}
	var keys []cid.Cid
	for _, key := range cidstr {
		_, barr, err := multibase.Decode(key)
		if err != nil {
			cerr <- err
			return
		}
		_, c, err := cid.CidFromBytes(barr)
		if err != nil {
			cerr <- err
			return
		}
		keys = append(keys, c)
	}

	var ma string
	if tcp {
		ma = fmt.Sprintf("/ip4/%s/tcp/%d", ip, port)
	} else {
		ma = fmt.Sprintf("/ip4/%s/udp/%d/quic", ip, port)
	}
	listen, err := multiaddr.NewMultiaddr(ma)
	if err != nil {
		cerr <- err
		return
	}
	h, err := libp2p.New(libp2p.ListenAddrs(listen), libp2p.Identity(sk))
	if err != nil {
		cerr <- err
		return
	}
	kad, err := dht.New(ctx, h)
	if err != nil {
		cerr <- err
		return
	}
	for _, a := range h.Addrs() {
		fmt.Printf("Client - listening on addr: %s\n", a.String())
	}

	bstore := blockstore.NewBlockstore(datastore.NewMapDatastore())

	var bsopt []bitswap.Option
	var bsnopt []bsnet.NetOpt
	bsopt = append(bsopt, bitswap.WithPSI(psi))
	bsopt = append(bsopt, bitswap.WithFilter(filter))
	bsnopt = append(bsnopt, bsnet.PSI(psi))
	bsnopt = append(bsnopt, bsnet.Filter(filter))
	ex := bitswap.New(ctx, bsnet.NewFromIpfsHost(h, kad, bsnopt...), bstore, bsopt...)

	fmt.Println("Client - connecting to provider...")
	err = h.Connect(ctx, sai)
	if err != nil {
		cerr <- fmt.Errorf("could not connect to provider: %w", err)
		return
	}
	fmt.Println("Client - Connected to provider")

	res, err := os.Create(resname)
	if err != nil {
		cerr <- err
		return
	}
	defer res.Close()

	fmt.Println("Reached.")
	// Download 1 Example Block for initialisation
	bg := blocksutil.NewBlockGenerator()
	icid := cid.NewCidV1(psiUtil.PsiCidCodec, bg.Next().Multihash())
	t0 := time.Now()
	blk, err := ex.PsiGetBlock(ctx, icid)
	if err != nil {
		cerr <- err
		return
	}
	t1 := time.Since(t0)
	first := &BitswapStat{
		SingleDownloadSpeed: &SingleDownloadSpeed{
			Count:            0,
			Cid:              blk.Cid().String(),
			DownloadDuration: Duration{T: t1},
		},
	}
	fmt.Println(Marshal(first))
	res.Write([]byte(first.tblHdr()))
	res.Write([]byte(first.tblLine()))

	c1 <- true
	select {
	case start := <-c2:
		if !start {
			return
		}
	}

	// Download specified blocks
	fmt.Println("Client - Downloading specific blocks")
	begin := time.Now()
	for blkcount, key := range keys {
		fmt.Printf("downloading block %s\n", key.String())
		dlBegin := time.Now()
		blk, err := ex.PsiGetBlock(ctx, key)
		if err != nil {
			cerr <- fmt.Errorf("could not download block %s: %w", key.String(), err)
			return
		}
		err = bstore.Put(ctx, blk) // store block
		if err != nil {
			cerr <- fmt.Errorf("could not store block %s: %w", key.String(), err)
			return
		}
		dlDuration := time.Since(dlBegin)
		s := &BitswapStat{
			SingleDownloadSpeed: &SingleDownloadSpeed{
				Count:            blkcount + 1,
				Cid:              blk.Cid().String(),
				TimeExpired:      Duration{T: time.Since(begin)},
				DownloadDuration: Duration{T: dlDuration},
			},
		}
		fmt.Println(Marshal(s))
		res.Write([]byte(s.tblLine()))
		stored, err := bstore.Has(ctx, blk.Cid())
		if err != nil {
			cerr <- fmt.Errorf("error checking if block was stored %s: %w", key.String(), err)
			return
		}
		if !stored {
			cerr <- fmt.Errorf("block was not stored %s: %w", key.String(), err)
			return
		}
	}
	dur := time.Since(begin)
	s := &BitswapStat{
		MultipleDownloadSpeed: &MultipleDownloadSpeed{
			BlockCount:    len(keys),
			TotalDuration: Duration{T: dur},
		},
	}
	fmt.Println(Marshal(s))

	// Clean up blockstore in case of new request run
	for _, key := range keys {
		err := bstore.DeleteBlock(ctx, key)
		if err != nil {
			cerr <- fmt.Errorf("could not delete block %s: %w", key.String(), err)
			return
		}
	}
	time.Sleep(1 * time.Second) // wait for bitswap to sent outstanding Cancels

	c1 <- true
	return
}

func runAttack(c1 chan bool, c2 chan bool, cerr chan error, count int, delay time.Duration, ip string, port int, tcp bool, sai peer.AddrInfo) {

	fmt.Println("running malicious Entity")
	ctx := context.Background()

	var ma string
	if tcp {
		ma = fmt.Sprintf("/ip4/%s/tcp/%d", ip, port)
	} else {
		ma = fmt.Sprintf("/ip4/%s/udp/%d/quic", ip, port)
	}
	fmt.Println(ma)
	listen, err := multiaddr.NewMultiaddr(ma)
	if err != nil {
		cerr <- err
		return
	}
	h, err := libp2p.New(libp2p.ListenAddrs(listen))
	if err != nil {
		cerr <- err
		return
	}
	for _, a := range h.Addrs() {
		fmt.Printf("Attacker - listening on addr: %s\n", a.String())
	}

	fmt.Println("Attacker - connecting to provider...")
	err = h.Connect(ctx, sai)
	if err != nil {
		cerr <- fmt.Errorf("Attacker could not connect to provider: %w", err)
		return
	}
	fmt.Println("Attacker - connected to provider")

	// Receive answers
	// var begin time.Time
	// h.SetStreamHandler(protocol.ID("ipfs/bitswap/psi"), func(s libp2pnet.Stream) {
	// 	reader := msgio.NewVarintReaderSize(s, libp2pnet.MessageSizeMax)
	// 	received, err := bsmsg.FromMsgReader(reader)
	// 	if err != nil {
	// 		if err != io.EOF {
	// 			_ = s.Reset()
	// 		}
	// 		return
	// 	}
	// 	fmt.Printf("%v, %v, %v, %v \n", received.Size(), time.Since(begin), received.Wantlist(), len(received.BlockPresences()))
	// 	begin = time.Now()
	// 	s.Close()
	// })

	c1 <- true
	select {
	case start := <-c2:
		if !start {
			return
		}
	}
	fmt.Println("Attack start!")
	bg := blocksutil.NewBlockGenerator()
	for i := 0; i < count; i++ {
		msg, err := createMsg(bg)
		if err != nil {
			cerr <- err
			return
		}
		con, err := h.NewStream(ctx, sai.ID, "ipfs/bitswap/psi")
		if err != nil {
			cerr <- err
			return
		}

		fmt.Printf("Attacker - Message %d, Size %d\n", i+1, msg.ToProtoV1().Size())
		err = msg.ToNetV1(con)
		if err != nil {
			cerr <- err
			return
		}
		time.Sleep(delay)
	}
	fmt.Println("Attacker - end send.")
	c1 <- true
	return
}

func createMsg(bg blocksutil.BlockGenerator) (bsmsg.BitSwapMessage, error) {

	psim, err := psiMagic.CreateWithNewKey(group.Ristretto255, "BISW-V01-CS01-with-ristretto255_XMD:SHA-512_R255MAP_RO_")
	if err != nil {
		return nil, err
	}
	gset := cid.NewSet()
	emh := bg.Next().Multihash()
	p, err := psim.Encrypt(emh)
	if err != nil {
		return nil, err
	}
	keys := cid.NewSet()
	counter := 1
	for i := 0; i < 89240; i++ {
		if i%127 == 0 {
			counter = 1
			emh = bg.Next().Multihash()
			p, err = psim.Encrypt(emh)
			if err != nil {
				return nil, err
			}
		}
		mh, _ := multihash.Encode(p, uint64(counter))
		counter++
		c := cid.NewCidV1(psiUtil.PsiCidCodec, mh)
		keys.Add(c)
		t := gset.Visit(c)
		if !t {
			fmt.Println("double")
		}
	}
	msg := bsmsg.New(false)
	for _, e := range keys.Keys() {
		msg.AddEntry(e, 1, bitswap_message_pb.Message_Wantlist_Have, true)
	}
	//fmt.Println(msg.ToProtoV1().Size())

	return msg, nil
}

type Duration struct {
	T time.Duration
}

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.T.String())
}

func (d *Duration) UnmarshalJSON(b []byte) error {
	var v interface{}
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	switch value := v.(type) {
	case float64:
		d.T = time.Duration(value)
		return nil
	case string:
		var err error
		d.T, err = time.ParseDuration(value)
		if err != nil {
			return err
		}
		return nil
	default:
		return errors.New("invalid duration")
	}
}

func Marshal(v interface{}) string {
	q, _ := json.Marshal(v)
	return string(q)
}

type BitswapStat struct {
	*SingleDownloadSpeed
	*MultipleDownloadSpeed
}

type SingleDownloadSpeed struct {
	Count            int      `json:"count"`
	Cid              string   `json:"cid"`
	TimeExpired      Duration `json:"time"`
	DownloadDuration Duration `json:"download_duration"`
}

func (sds *SingleDownloadSpeed) tblHdr() string {
	str := fmt.Sprintf("count, time, dur, cid")
	str = fmt.Sprintln(str)
	return str
}

func (sds *SingleDownloadSpeed) tblLine() string {
	str := fmt.Sprintf("%v,%v,%v,%v", sds.Count, sds.TimeExpired.T.Seconds(), sds.DownloadDuration.T.Seconds(), sds.Cid)
	str = fmt.Sprintln(str)
	return str
}

type MultipleDownloadSpeed struct {
	BlockCount    int      `json:"block_count"` // number of blocks downloaded
	TotalDuration Duration `json:"total_duration"`
}
