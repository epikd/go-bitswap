package main

import (
	"bufio"
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"path"
	"path/filepath"

	blocksutil "github.com/ipfs/go-ipfs-blocksutil"
	logging "github.com/ipfs/go-log/v2"

	bitswap "github.com/ipfs/go-bitswap"
	bsnet "github.com/ipfs/go-bitswap/network"
	block "github.com/ipfs/go-block-format"
	"github.com/ipfs/go-cid"
	"github.com/ipfs/go-datastore"
	blockstore "github.com/ipfs/go-ipfs-blockstore"
	libp2p "github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	libp2pcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/multiformats/go-multiaddr"
)

var slog = logging.Logger("server")

var sbsk = []byte{184, 36, 152, 185, 67, 205, 197, 214, 75, 64, 160, 99, 103, 44, 113, 171, 63, 198, 183, 160, 101, 141, 211, 25, 5, 230, 47, 85, 174, 217, 182, 48, 52, 61, 36, 227, 72, 154, 98, 89, 89, 39, 196, 235, 90, 85, 179, 73, 148, 86, 11, 39, 22, 105, 135, 50, 100, 148, 220, 234, 66, 67, 44, 114}

func main() {
	ip := "127.0.0.1"
	tcpport := 3333
	quicport := 3334

	filter := false
	psi := false

	// config logging
	fp := filepath.Join(".", "server.log")
	logf, err := os.Create(fp)
	if err != nil {
		fmt.Println(err)
	}
	defer logf.Close()
	logcfg := logging.GetConfig()
	logcfg.File = filepath.Clean(fp)
	logcfg.Format = logging.JSONOutput
	logcfg.Level = logging.LevelWarn
	logcfg.Stderr = false
	logcfg.Stdout = false
	logging.SetupLogging(logcfg)

	fmt.Printf("PSI=%v,Filter=%v\n", psi, filter)
	fmt.Println(runprovide(ip, tcpport, quicport, filter, psi))
}

func runprovide(ip string, tcpport int, quicport int, filter bool, psi bool) error {

	setsize := 1000
	rcount := 100

	fmt.Println("running Bitswap-Server")
	ctx := context.Background()

	// use always same identity
	sk, err := libp2pcrypto.UnmarshalEd25519PrivateKey(sbsk)
	if err != nil {
		fmt.Println(err)
		return err
	}

	ma := fmt.Sprintf("/ip4/%s/tcp/%d", ip, tcpport)
	listen, err := multiaddr.NewMultiaddr(ma)
	if err != nil {
		return err
	}

	ma2 := fmt.Sprintf("/ip4/%s/udp/%d/quic", ip, quicport)
	listen2, err := multiaddr.NewMultiaddr(ma2)
	if err != nil {
		return err
	}

	h, err := libp2p.New(libp2p.ListenAddrs(listen, listen2), libp2p.Identity(sk))
	if err != nil {
		return err
	}
	kad, err := dht.New(ctx, h)
	if err != nil {
		return err
	}
	for _, a := range h.Addrs() {
		fmt.Printf("listening on addr: %s\n", a.String())
	}

	bstore := blockstore.NewBlockstore(datastore.NewMapDatastore())

	var bsopt []bitswap.Option
	var bsnopt []bsnet.NetOpt
	bsopt = append(bsopt, bitswap.WithPSI(psi))
	bsopt = append(bsopt, bitswap.WithFilter(filter))
	bsnopt = append(bsnopt, bsnet.PSI(psi))
	bsnopt = append(bsnopt, bsnet.Filter(filter))
	ex := bitswap.New(ctx, bsnet.NewFromIpfsHost(h, kad, bsnopt...), bstore, bsopt...)

	// create blocks of specific size and add them to the block store
	var keys []cid.Cid
	blks := readblks(rcount)
	for _, blk := range blks {
		err := bstore.Put(ctx, blk)
		if err != nil {
			return err
		}
		err = ex.NotifyNewBlocks(ctx, blk)
		if err != nil {
			return err
		}
		mh := blk.Multihash()
		//fmt.Printf("publishing block %s\n", mh.String())
		keys = append(keys, cid.NewCidV1(0xec, mh))
	}

	// create mass of example blocks
	bg := blocksutil.NewBlockGenerator()
	nblks := bg.Blocks(setsize - rcount)
	for _, blk := range nblks {
		err := bstore.Put(ctx, blk)
		if err != nil {
			return err
		}
		err = ex.NotifyNewBlocks(ctx, blk)
		if err != nil {
			return err
		}
	}

	// wait for user input before terminating
	for {
		scanner := bufio.NewScanner(os.Stdin)
		scanner.Scan()
		text := scanner.Text()

		if text == "peerledger" {
			for _, pid := range h.Network().Peers() {
				l := ex.WantlistForPeer(pid)
				fmt.Println(len(l))
			}
		} else if text == "end" {
			break
		} else {
			fmt.Println("Do not know:", text)
		}
	}
	return nil
}

func readblks(n int) []*block.BasicBlock {
	dn := "./blk"
	fis, err := ioutil.ReadDir(dn)
	if err != nil {
		fmt.Println(err)
		return nil
	}
	var fns []string
	for _, fi := range fis {
		fns = append(fns, fi.Name())
	}

	var blks []*block.BasicBlock
	for i, fn := range fns {
		if i >= n {
			break
		}
		c := path.Join(dn, fn)
		data, err := os.ReadFile(c)
		if err != nil {
			fmt.Println(err)
			return nil
		}
		blk := block.NewBlock(data)
		blks = append(blks, blk)
	}
	return blks
}
