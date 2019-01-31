package main

import (
	"fmt"
	"github.com/ExchangeUnion/xud-simulation/lntest"
	"github.com/ExchangeUnion/xud-simulation/xudrpc"
	"github.com/ExchangeUnion/xud-simulation/xudtest"
	"github.com/go-errors/errors"
	ltcchaincfg "github.com/ltcsuite/ltcd/chaincfg"
	ltcchainhash "github.com/ltcsuite/ltcd/chaincfg/chainhash"
	ltctest "github.com/ltcsuite/ltcd/integration/rpctest"
	ltcclient "github.com/ltcsuite/ltcd/rpcclient"
	"github.com/ltcsuite/ltcutil"
	btcchaincfg "github.com/roasbeef/btcd/chaincfg"
	btcchainhash "github.com/roasbeef/btcd/chaincfg/chainhash"
	btctest "github.com/roasbeef/btcd/integration/rpctest"
	btcclient "github.com/roasbeef/btcd/rpcclient"
	"github.com/roasbeef/btcutil"
	"golang.org/x/net/context"
	"strings"
	"testing"
	"time"
)

type testCase struct {
	name string
	test func(net *xudtest.NetworkHarness, t *harnessTest)
}

// harnessTest wraps a regular testing.T providing enhanced error detection
// and propagation. All error will be augmented with a full stack-trace in
// order to aid in debugging. Additionally, any panics caused by active
// test cases will also be handled and represented as fatals.
type harnessTest struct {
	t *testing.T

	// testCase is populated during test execution and represents the
	// current test case.
	testCase *testCase
}

// newHarnessTest creates a new instance of a harnessTest from a regular
// testing.T instance.
func newHarnessTest(t *testing.T) *harnessTest {
	return &harnessTest{t, nil}
}

var testsCases = []*testCase{
	{
		name: "verify connectivity",
		test: testVerifyConnectivity,
	},
	{
		name: "add currencies and pair",
		test: testAddCurrenciesAndPair,
	},
	{
		name: "connect to peer",
		test: testConnectPeer,
	},
	{
		name: "place order and broadcast",
		test: testPlaceOrderAndBroadcast,
	},
}

func testVerifyConnectivity(net *xudtest.NetworkHarness, ht *harnessTest) {
	for _, node := range net.ActiveNodes {
		info, err := node.Client.GetInfo(context.Background(), &xudrpc.GetInfoRequest{})
		if err != nil {
			ht.Fatalf("RPC GetInfo failure: %v", err)
		}

		if info.Lndbtc == nil {
			ht.Fatalf("RPC GetInfo: lnd-btc not connected.")
		}

		if info.Lndltc == nil {
			ht.Fatalf("RPC GetInfo: lnd-ltc not connected.")
		}

		if len(info.Lndbtc.Chains) != 1 || info.Lndbtc.Chains[0] != "bitcoin" {
			ht.Fatalf("RPC GetInfo: invalid lnd-btc chain: %v", info.Lndbtc.Chains[0])
		}

		if len(info.Lndltc.Chains) != 1 || info.Lndltc.Chains[0] != "litecoin" {
			ht.Fatalf("RPC GetInfo: invalid lnd-ltc chain: %v", info.Lndbtc.Chains[0])
		}

		node.SetPubKey(info.NodePubKey)
	}
}

func testAddCurrenciesAndPair(net *xudtest.NetworkHarness, ht *harnessTest) {
	for _, node := range net.ActiveNodes {
		reqAddCurr := &xudrpc.AddCurrencyRequest{Currency: "BTC", SwapClient: 0}
		if _, err := node.Client.AddCurrency(context.Background(), reqAddCurr); err != nil {
			ht.Fatalf("RPC AddCurrency failure: %v", err)
		}

		reqAddCurr = &xudrpc.AddCurrencyRequest{Currency: "LTC", SwapClient: 0}
		if _, err := node.Client.AddCurrency(context.Background(), reqAddCurr); err != nil {
			ht.Fatalf("RPC AddCurrency failure: %v", err)
		}

		reqAddPair := &xudrpc.AddPairRequest{BaseCurrency: "LTC", QuoteCurrency: "BTC"}
		if _, err := node.Client.AddPair(context.Background(), reqAddPair); err != nil {
			ht.Fatalf("RPC AddPair failure: %v", err)
		}

		resGetInfo, err := node.Client.GetInfo(context.Background(), &xudrpc.GetInfoRequest{})
		if err != nil {
			ht.Fatalf("RPC GetInfo failure: %v", err)
		}

		if resGetInfo.NumPairs != 1 {
			ht.Fatalf("RPC GetInfo: added pair is missing.")
		}
	}
}

func testConnectPeer(net *xudtest.NetworkHarness, ht *harnessTest) {
	bobNodeUri := fmt.Sprintf("%v@%v",
		net.Bob.PubKey(),
		net.Bob.Cfg.P2PAddr(),
	)

	// Alice to connect to Bob

	reqConn := &xudrpc.ConnectRequest{NodeUri: bobNodeUri}
	_, err := net.Alice.Client.Connect(context.Background(), reqConn)
	if err != nil {
		ht.Fatalf("RPC Connect failure: %v", err)
	}

	// assert Alice's peer (bob)

	resListPeers, err := net.Alice.Client.ListPeers(context.Background(), &xudrpc.ListPeersRequest{})
	if err != nil {
		ht.Fatalf("RPC ListPeers failure: %v", err)
	}
	if len(resListPeers.Peers) != 1 {
		ht.Fatalf("RPC ListPeers: peers are missing.")
	}

	assertPeersNum(ht, resListPeers.Peers, 1)
	assertPeerInfo(ht, resListPeers.Peers[0], net.Bob)

	// assert Bob's peer (alice)

	resListPeers, err = net.Bob.Client.ListPeers(context.Background(), &xudrpc.ListPeersRequest{})
	if err != nil {
		ht.Fatalf("RPC ListPeers failure: %v", err)
	}

	assertPeersNum(ht, resListPeers.Peers, 1)
	assertPeerInfo(ht, resListPeers.Peers[0], net.Alice)
}

func testPlaceOrderAndBroadcast(net *xudtest.NetworkHarness, ht *harnessTest) {
	ctx, _ := context.WithTimeout(
		context.Background(),
		time.Duration(5*time.Second),
	)

	err := placeOrderAndBroadcast(ctx, net.Alice, net.Bob)
	if err != nil {
		ht.Fatalf("%v", err)
	}
}

// TODO: implement
func removeOrderAndInvalidate() {

}

func placeOrderAndBroadcast(ctx context.Context, node, peer *xudtest.HarnessNode) error {
	req := &xudrpc.PlaceOrderRequest{
		Price:    10,
		Quantity: 10,
		PairId:   "LTC/BTC",
		OrderId:  "123",
		Side:     xudrpc.OrderSide_BUY,
	}

	stream, err := peer.Client.SubscribeAddedOrders(ctx, &xudrpc.SubscribeAddedOrdersRequest{})
	if err != nil {
		return fmt.Errorf("SubscribeAddedOrders: %v", err)
	}

	recvChan := make(chan struct{})
	errChan := make(chan error)
	go func() {
		for {
			// Consume the subscription event.
			// This waits until  the node notifies us
			// that it received an order.
			recvOrder, err := stream.Recv()
			if err != nil {
				errChan <- err
				return
			}

			if recvOrder.Id == req.OrderId {
				errChan <- fmt.Errorf("SubscribeAddedOrders: " +
					"received order local id as global id")
				return
			}

			// If different order was received,
			// skip it and don't fail the test.
			if val, ok := recvOrder.OwnOrPeer.(*xudrpc.Order_PeerPubKey);
				!ok || val.PeerPubKey != node.PubKey() {
				continue
			}
			if recvOrder.Price != req.Price ||
				recvOrder.PairId != req.PairId ||
				recvOrder.Quantity != req.Quantity ||
				recvOrder.Side != req.Side ||
				recvOrder.IsOwnOrder == true {
				continue
			}

			// Order received.
			close(recvChan)
		}
	}()

	res, err := node.Client.PlaceOrderSync(context.Background(), req)
	if err != nil {
		return fmt.Errorf("PlaceOrderSync: %v", err)
	}
	if len(res.InternalMatches) != 0 {
		return fmt.Errorf("PlaceOrderSync: unexpected internal matches")
	}

	if len(res.SwapSuccesses) != 0 {
		return fmt.Errorf("PlaceOrderSync: unexpected swap successes")
	}

	if res.RemainingOrder == nil {
		return fmt.Errorf("PlaceOrderSync: expected remaining order missing")
	}

	if res.RemainingOrder.Id == req.OrderId {
		return fmt.Errorf("PlaceOrderSync: received order local id as global id")
	}

	if val, ok := res.RemainingOrder.OwnOrPeer.(*xudrpc.Order_LocalId);
		!ok || val.LocalId != req.OrderId {
		return fmt.Errorf("PlaceOrderSync: " +
			"invalid order local id")
	}

	select {
	case <-ctx.Done():
		return errors.New("timeout reached before order was received")
	case err := <-errChan:
		return err
	case <-recvChan:
		return nil
	}
}

func assertPeersNum(ht *harnessTest, peers []*xudrpc.Peer, num int) {
	if len(peers) != num {
		ht.Fatalf("Invalid peers num.")
	}
}

func assertPeerInfo(ht *harnessTest, peer *xudrpc.Peer, node *xudtest.HarnessNode) {
	if peer.NodePubKey != node.PubKey() {
		ht.Fatalf("Invalid peer NodePubKey")
	}

	if peer.LndBtcPubKey != node.LndBtcNode.PubKeyStr {
		ht.Fatalf("Invalid peer LndBtcPubKey")
	}

	if peer.LndLtcPubKey != node.LndLtcNode.PubKeyStr {
		ht.Fatalf("Invalid peer LndLtcPubKey")
	}
}

// Fatalf causes the current active test case to fail with a fatal error. All
// integration tests should mark test failures solely with this method due to
// the error stack traces it produces.
func (h *harnessTest) Fatalf(format string, a ...interface{}) {
	stacktrace := errors.Wrap(fmt.Sprintf(format, a...), 1).ErrorStack()

	if h.testCase != nil {
		h.t.Fatalf("Failed: (%v): exited with error: \n"+
			"%v", h.testCase.name, stacktrace)
	} else {
		h.t.Fatalf("Error outside of test: %v", stacktrace)
	}
}

func (h *harnessTest) Logf(format string, args ...interface{}) {
	h.t.Logf(format, args...)
}

// RunTestCase executes a harness test case. Any errors or panics will be
// represented as fatal.
func (h *harnessTest) RunTestCase(testCase *testCase, net *xudtest.NetworkHarness) {
	h.testCase = testCase
	defer func() {
		h.testCase = nil
	}()

	defer func() {
		if err := recover(); err != nil {
			description := errors.Wrap(err, 2).ErrorStack()
			h.t.Fatalf("Failed: (%v) panicked with: \n%v",
				h.testCase.name, description)
		}
	}()

	testCase.test(net, h)

	return
}

func TestExchangeUnionDaemon(t *testing.T) {
	ht := newHarnessTest(t)

	// LND-LTC network

	var lndLtcNetworkHarness *lntest.NetworkHarness

	ltcHandlers := &ltcclient.NotificationHandlers{
		OnTxAccepted: func(hash *ltcchainhash.Hash, amt ltcutil.Amount) {
			newHash := new(lntest.Hash)
			copy(newHash[:], hash[:])
			lndLtcNetworkHarness.OnTxAccepted(newHash)
		},
	}
	ltcdHarness, err := ltctest.New(&ltcchaincfg.SimNetParams, ltcHandlers, []string{"--rejectnonstd", "--txindex"})
	if err != nil {
		ht.Fatalf("ltcd: unable to create mining node: %v", err)
	}
	defer func() {
		if err := ltcdHarness.TearDown(); err != nil {
			ht.Fatalf("ltcd: cannot tear down harness: %v", err)
		} else {
			t.Logf("ltcd: harness teared down")
		}
	}()

	t.Logf("ltcd: launching node...")
	if err := ltcdHarness.SetUp(true, 50); err != nil {
		ht.Fatalf("ltcd: unable to set up mining node: %v", err)
	}
	if err := ltcdHarness.Node.NotifyNewTransactions(false); err != nil {
		ht.Fatalf("ltcd: unable to request transaction notifications: %v", err)
	}

	numBlocks := ltcchaincfg.SimNetParams.MinerConfirmationWindow * 2
	if _, err := ltcdHarness.Node.Generate(numBlocks); err != nil {
		ht.Fatalf("ltcd: unable to generate blocks: %v", err)
	}
	t.Logf("ltcd: %d blocks generated", numBlocks)

	lndLtcNetworkHarness, err = lntest.NewNetworkHarness(ltcdHarness, "litecoin")
	if err != nil {
		ht.Fatalf("lnd-ltc: unable to create network harness: %v", err)
	}
	defer func() {
		if err := lndLtcNetworkHarness.TearDownAll(); err != nil {
			ht.Fatalf("lnd-ltc: cannot tear down network harness: %v", err)
		} else {
			t.Logf("lnd-ltc: network harness teared down")
		}
	}()

	go func() {
		for {
			select {
			case err, more := <-lndLtcNetworkHarness.ProcessErrors():
				if !more {
					return
				}
				t.Logf("lnd-ltc: finished with error (stderr):\n%v", err)
			}
		}
	}()

	t.Logf("lnd-ltc: launching network...")
	if err = lndLtcNetworkHarness.SetUp(nil); err != nil {
		ht.Fatalf("lnd-ltc: unable to set up test network: %v", err)
	}

	// LND-BTC network

	var lndBtcNetworkHarness *lntest.NetworkHarness

	// First create an instance of the btcd's rpctest.Harness. This will be
	// used to fund the wallets of the nodes within the test network and to
	// drive blockchain related events within the network. Revert the default
	// setting of accepting non-standard transactions on simnet to reject them.
	// Transactions on the lightning network should always be standard to get
	// better guarantees of getting included in to blocks.
	args := []string{"--rejectnonstd", "--txindex"}
	handlers := &btcclient.NotificationHandlers{
		OnTxAccepted: func(hash *btcchainhash.Hash, amt btcutil.Amount) {
			newHash := new(lntest.Hash)
			copy(newHash[:], hash[:])
			lndBtcNetworkHarness.OnTxAccepted(newHash)
		},
	}
	btcdHarness, err := btctest.New(&btcchaincfg.SimNetParams, handlers, args)
	if err != nil {
		ht.Fatalf("btcd: unable to create mining node: %v", err)
	}
	defer func() {
		if err := btcdHarness.TearDown(); err != nil {
			ht.Fatalf("btcd: cannot tear down harness: %v", err)
		} else {
			t.Logf("btcd: harness teared down")
		}
	}()

	t.Logf("btcd: launching node...")
	if err := btcdHarness.SetUp(true, 50); err != nil {
		ht.Fatalf("btcd: unable to set up mining node: %v", err)
	}
	if err := btcdHarness.Node.NotifyNewTransactions(false); err != nil {
		ht.Fatalf("btcd: unable to request transaction notifications: %v", err)
	}

	// Next mine enough blocks in order for segwit and the CSV package
	// soft-fork to activate on SimNet.
	numBlocks = btcchaincfg.SimNetParams.MinerConfirmationWindow * 2
	if _, err := btcdHarness.Node.Generate(numBlocks); err != nil {
		ht.Fatalf("btcd: unable to generate blocks: %v", err)
	}

	t.Logf("btcd: %d blocks generated", numBlocks)

	// First create the network harness to gain access to its
	// 'OnTxAccepted' call back.
	lndBtcNetworkHarness, err = lntest.NewNetworkHarness(btcdHarness, "bitcoin")
	if err != nil {
		ht.Fatalf("lnd-btc: unable to create harness: %v", err)
	}
	defer func() {
		if err := lndBtcNetworkHarness.TearDownAll(); err != nil {
			ht.Fatalf("lnd-btc: cannot tear down network harness: %v", err)
		} else {
			t.Logf("lnd-btc: network harness teared down")
		}
	}()

	// Spawn a new goroutine to watch for any fatal errors that any of the
	// running lnd processes encounter. If an error occurs, then the test
	// case should naturally as a result and we log the server error here to
	// help debug.
	go func() {
		for {
			select {
			case err, more := <-lndBtcNetworkHarness.ProcessErrors():
				if !more {
					return
				}
				t.Logf("lnd-btc: finished with error (stderr):\n%v", err)
			}
		}
	}()

	t.Logf("lnd-btc: launching network...")
	if err = lndBtcNetworkHarness.SetUp(nil); err != nil {
		ht.Fatalf("lnd-btc: unable to set up test network: %v", err)
	}

	// XUD network

	xudHarness, err := xudtest.NewNetworkHarness(lndBtcNetworkHarness, lndLtcNetworkHarness)
	if err != nil {
		ht.Fatalf("unable to create xud network harness: %v", err)
	}
	defer func() {
		if err := xudHarness.TearDownAll(true, true); err != nil {
			ht.Fatalf("cannot tear down xud network harness: %v", err)
		} else {
			t.Logf("xud network harness teared down")
		}
	}()

	t.Logf("launching xud network...")
	if err := xudHarness.SetUp(); err != nil {
		ht.Fatalf("cannot set up xud network: %v", err)
	}

	go func() {
		for {
			select {
			case xudError, more := <-xudHarness.ProcessErrors():
				if !more {
					return
				}

				if strings.Contains(xudError.Err.Error(), "signal: killed") {
					t.Logf("xud process (%v-%v) did not shutdown gracefully. process (%v) killed",
						xudError.Node.Id, xudError.Node.Name, xudError.Node.Cmd.Process.Pid)

				} else {
					t.Logf("xud process finished with error (stderr):\n%v", err)
				}
			}
		}
	}()

	// Run tests

	t.Logf("Running %v integration tests", len(testsCases))
	for _, testCase := range testsCases {
		success := t.Run(testCase.name, func(t1 *testing.T) {
			ht := newHarnessTest(t1)
			ht.RunTestCase(testCase, xudHarness)
		})

		// Stop at the first failure. Mimic behavior of original test
		// framework.
		if !success {
			break
		}
	}
}
