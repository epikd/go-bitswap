# ECDH-PSI Test

Very simple [Testground](https://github.com/testground/testground) testplan to test the ECDH-PSI functionality.  
The test starts bitswap nodes (at least 2).
One node is a Server (provider) the rest are clients (requester).
"count" blocks are added to the Server and the CIDs are shared with the Clients.
Then the Clients attempt to download the blocks using ECDH-PSI for the block discovery process.
The logging is set to "info", showing the changes in the messages.
Afterwards, the first block is downloaded again using the normal bitswap behavior.

## Running the test

To run the test [install](https://docs.testground.ai/getting-started) Testground.  

Import the testplan:

```
testground plan import --from ecdh-psi
```

Run the test with the desired parameters e.g.:

```
testground run single --plan=ecdh-psi --testcase=speed-test --builder="docker:go" --runner="local:docker" --instances=2 --tp size=512kiB --tp count=5
```