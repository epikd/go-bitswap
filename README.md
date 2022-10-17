Bitswap
==================

This is a PoC trying to improve the privacy of the Bitswap discovery process using Bloom filter and Private Set Intersection.

## Background

Bitswap is the default data exchange component for IPFS.
It sends requests for specific blocks to the network and answers request from other nodes.

Bitswap retrieves a block in multiple steps.
First there is an initial discovery phase, where Bitswap sends to all currently connected peers a *WANT-HAVE* message, requesting the CID.
The neighbors, receiving the *WANT-HAVE*, answer with a *HAVE* if they store the block.
If the peer does not store the block, it does not answer or sends a *DontHave* depending on the "sendDontHave" field of the received *WANT-HAVE* message.

After a certain delay in which the block was not yet received, Bitswap searches for content providers.
In IPFS content providers are searched in the Kademlia-based DHT.

The problem is finding a peer possessing the block, without revealing the interest to all neighbor.
It is sufficient if only one peer possessing the block is informed about the interest.
Two parts of Bitswap leak the interest to many unnecessary additional peers, the discovery process and the provider search.

Here the discovery process is modified.
There are two methods: using a Bloom filter and using Private Set Intersection (PSI).
The methods mainly protect the requesters privacy and do not protect the actual exchange of the block.
For the Bloom filter the [bbloom](https://github.com/ipfs/bbloom) package is used

### Bloom filter
The simplest method to prevent the leak of interest to another peer is by not requesting a specific CID, but requesting all stored CIDs.
To reduce a potentially long list of CIDs, a Bloom filter is used.
The Bloom filter also adds plausible deniability for the peer to the request.

This method changes the request of a CID to the request of a Bloom filter of all stored blocks.
Instead of sending *WANT-HAVE* messages, the requester asks the Bloom filter.

Example:  
CID requested: QmSXGdesfFsGHNeCL5YnAiY5XLS68gTRrKh8PsMysEJfkp  
As CIDv1 with specific multi-codec identifier : bahwacerahytszibb7o3d4lnlqykyq72y244kvhdkq6dbsay525jhalyd636q  
Bloom filter: bag7qdpybzeaxwisgnfwhizlsknsxiir2ejaucr2kirivcssbifaucqkbkfbhgqkcm5aucqkbijbecslhmfbucu3hifcucrkbincuesksjfausqkbkfbeeu2ciztueqkbifaucrcvkfawoqkfjfavcqkbn5tumz2nifjwoukbinivculhijaucqkriffuewsbm5tuqrkbkvaukskfif3ucqkcifiucqkdincwsskrifevcqkbifbecqkrnnaucuscifrucz2fifcucqkckjawqqkrifaucz2bivaukq2bjfau2tkbjfat2irmejjwk5cmn5rxgir2ge2h2  
Legend:  
Direction - in (incoming message), out (outgoing message)  
View - internal (requester Bitswap's view), wire (peer Bitswap's view)  
Msg - Blocks: \#Blocks, Wantlist: [{{CID Priority Type} Cancel sendDontHave} ...], BP [{CID Presence} ...]  

| Direction | View | Msg |
| --------- | ---- | --- |
| out | internal | Blocks: 0, Wantlist: [{{bahwacerahytszibb7o3d4lnlqykyq72y244kvhdkq6dbsay525jhalyd636q 2147483647 Have} false false}], BP: [] |
| out | wire | Blocks: 0, Wantlist: [{{bag7qdpybaa 1 Have} false false}], BP: [] |
| in | wire  | Blocks: 0, Wantlist: [], BP: [{"Bloom filter" Have}] |
| in | internal | Blocks: 0, Wantlist: [], BP: [{bahwacerahytszibb7o3d4lnlqykyq72y244kvhdkq6dbsay525jhalyd636q Have}] |
| out | internal | Blocks: 0, Wantlist: [{{bahwacerahytszibb7o3d4lnlqykyq72y244kvhdkq6dbsay525jhalyd636q 2147483646 Block} false true}], BP: [] |
| out | wire | Blocks: 0, Wantlist: [{{bahwacerahytszibb7o3d4lnlqykyq72y244kvhdkq6dbsay525jhalyd636q 2147483646 Block} false true}], BP: [] |
| in | wire | Blocks: 1, Wantlist: [], BP: [] |
| in | internal | Blocks: 1, Wantlist: [], BP: [] |
| out | internal | Blocks: 0, Wantlist: [{{bahwacerahytszibb7o3d4lnlqykyq72y244kvhdkq6dbsay525jhalyd636q 0 Block} true false}], BP: [] |
| out | wire | Blocks: 0, Wantlist: [{{bahwacerahytszibb7o3d4lnlqykyq72y244kvhdkq6dbsay525jhalyd636q 0 Block} true false}], BP: [] |

### PSI
The goal of PSI is to check whether an element or key is known by both parties, without revealing any additional information.
After the execution of PSI, the only information each party learns is the intersection of elements.
PSI may leak the set size of both parties, but this is often not to be considered sensitive.
This method uses Elliptic Curve Diffie-Hellman-PSI (see also: [psiMagic](https://github.com/epikd/psiMagic)).

Instead of requesting a CID, the CID of v=H(CID.Hash())^r1 is requested.
The peer answers with a *DontHave* CID of v^r2.
Only CIDs with a specific multi-codec identifier are transformed.  

Example:  
CID requested: QmSmX4KZma8Y6111KRptHhScP3UpNVKPxCcoG1RfqQrGtz  
As CIDv1 with specific multi-codec identifier bahwaceraihgz3sxxdcb7rwqhqw3gzvmfk37ytbmkmvfq6ieivn6yiy7wtr5q  
Legend:  
Direction - in (incoming message), out (outgoing message)  
View - internal (requester Bitswap's view), wire (peer Bitswap's view)  
Msg - Blocks: \#Blocks, Wantlist: [{{CID Priority Type} Cancel sendDontHave} ...], BP [{CID Presence} ...]  

| Direction | View | Msg |
| --------- | ---- | --- |
| out | internal | Blocks: 0, Wantlist: [{{bahwaceraihgz3sxxdcb7rwqhqw3gzvmfk37ytbmkmvfq6ieivn6yiy7wtr5q 2147483645 Have} false false}], BP: [] |
| out | wire | Blocks: 0, Wantlist: [{{bahwacajattwxepitmdoc5b6umrc6vekog4jegdqp3sg4wvzwerwwvarqovmq 2147483645 Have} false true}], BP: [] | 
| in | wire | Blocks: 0, Wantlist: [], BP: [{bahwacaja3kxrceqc7vvcsdn4wskkxbybdrxv4g25asvnt4ss25unql5hfepa DontHave}] |
| in | internal | Blocks: 0, Wantlist: [], BP: [{bahwaceraihgz3sxxdcb7rwqhqw3gzvmfk37ytbmkmvfq6ieivn6yiy7wtr5q Have}] |
| out | internal | Blocks: 0, Wantlist: [{{bahwaceraihgz3sxxdcb7rwqhqw3gzvmfk37ytbmkmvfq6ieivn6yiy7wtr5q 2147483644 Block} false true}], BP: [] |
| out | wire | Blocks: 0, Wantlist: [{{bahwaceraihgz3sxxdcb7rwqhqw3gzvmfk37ytbmkmvfq6ieivn6yiy7wtr5q 2147483644 Block} false true}], BP: [] |
| in | wire | Blocks: 1, Wantlist: [], BP: [] |
| in | internal | Blocks: 1, Wantlist: [], BP: [] |
| out | internal | Blocks: 0, Wantlist:[{{bahwaceraihgz3sxxdcb7rwqhqw3gzvmfk37ytbmkmvfq6ieivn6yiy7wtr5q 0 Block} true false}], BP: [] |
| out | wire | Blocks: 0, Wantlist: [{{bahwaceraihgz3sxxdcb7rwqhqw3gzvmfk37ytbmkmvfq6ieivn6yiy7wtr5q 0 Block} true false}], BP: [] |

## License

MIT © Juan Batiz-Benet
