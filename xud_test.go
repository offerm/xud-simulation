package main

import (
	"fmt"
	"github.com/ExchangeUnion/xud-simulation/lntest"
	"github.com/ExchangeUnion/xud-simulation/scenarios"
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
	"github.com/stretchr/testify/require"
	"golang.org/x/net/context"
	"log"
	"os"
	"os/exec"
	"path/filepath"
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

	// assertion methods to stop test execution upon failure.
	assert *require.Assertions
}

// newHarnessTest creates a new instance of a harnessTest from a regular
// testing.T instance.
func newHarnessTest(t *testing.T) *harnessTest {
	return &harnessTest{
		t:        t,
		testCase: nil,
		assert:   require.New(t),
	}
}

var testsCases = []*testCase{
	{
		name: "connectivity",
		test: testConnectivity,
	},
	{
		name: "network initialization",
		test: testNetworkInit,
	},
	{
		name: "order broadcast and invalidation",
		test: testOrderBroadcastAndInvalidation,
	},
	{
		name: "order matching and swap",
		test: testOrderMatchingAndSwap,
	},
}

func testConnectivity(net *xudtest.NetworkHarness, ht *harnessTest) {
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

func testNetworkInit(net *xudtest.NetworkHarness, ht *harnessTest) {
	ctx := context.Background()

	for _, node := range net.ActiveNodes {
		scenarios.AddPair(ht.assert, ctx, node, "LTC", "BTC", xudrpc.AddCurrencyRequest_LND)
	}

	scenarios.Connect(ht.assert, ctx, net.Alice, net.Bob)
}

func testOrderMatchingAndSwap(net *xudtest.NetworkHarness, ht *harnessTest) {
	ctx, _ := context.WithTimeout(
		context.Background(),
		time.Duration(5*time.Second),
	)

	// Placing an order for Alice
	req := &xudrpc.PlaceOrderRequest{
		Price:    10,
		Quantity: 0.00000001,
		PairId:   "LTC/BTC",
		OrderId:  "random_order_id",
		Side:     xudrpc.OrderSide_BUY,
	}

	scenarios.PlaceOrderAndBroadcast(ht.assert, ctx, net.Alice, net.Bob, req)

	// Placing a matching order for Bob
	req = &xudrpc.PlaceOrderRequest{
		Price:    req.Price,
		Quantity: req.Quantity,
		PairId:   req.PairId,
		OrderId:  req.OrderId,
		Side:     xudrpc.OrderSide_SELL,
	}

	scenarios.PlaceOrderAndSwap(ht.assert, ctx, net.Bob, net.Alice, req)
}

func testOrderBroadcastAndInvalidation(net *xudtest.NetworkHarness, ht *harnessTest) {
	ctx, _ := context.WithTimeout(
		context.Background(),
		time.Duration(5*time.Second),
	)

	req := &xudrpc.PlaceOrderRequest{
		Price:    10,
		Quantity: 10,
		PairId:   "LTC/BTC",
		OrderId:  "random_order_id",
		Side:     xudrpc.OrderSide_BUY,
	}

	order := scenarios.PlaceOrderAndBroadcast(ht.assert, ctx, net.Alice, net.Bob, req)


	scenarios.RemoveOrderAndInvalidate(ht.assert, ctx, net.Alice, net.Bob, order)

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
	cfg := loadConfig()

	log.Println("installing dependencies...")
	output, err := installDeps()
	if err != nil {
		ht.Fatalf("%v", err)
	}
	log.Printf("\n%v", output)

	dir, err := os.Getwd()

	// TODO: don't use the temp hardcoded values approach
	aliceResolverCfg := &lntest.HashResolverConfig{
		ServerAddr: "localhost:30002",
		TLS:        true,
		CaFile:     filepath.Join(dir, "xuddatadir-Alice", "tls.cert"),
	}
	bobResolverCfg := &lntest.HashResolverConfig{
		ServerAddr: "localhost:30001",
		TLS:        true,
		CaFile:     filepath.Join(dir, "xuddatadir-Bob", "tls.cert"),
	}

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
	if err = lndLtcNetworkHarness.SetUp(nil, aliceResolverCfg, bobResolverCfg); err != nil {
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
	if err = lndBtcNetworkHarness.SetUp(nil, aliceResolverCfg, bobResolverCfg); err != nil {
		ht.Fatalf("lnd-btc: unable to set up test network: %v", err)
	}

	// XUD network

	xudHarness, err := xudtest.NewNetworkHarness(lndBtcNetworkHarness, lndLtcNetworkHarness)
	if err != nil {
		ht.Fatalf("unable to create xud network harness: %v", err)
	}
	defer func() {
		if err := xudHarness.TearDownAll(cfg.xudKill, cfg.xudCleanup); err != nil {
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

func installDeps() (string, error) {
	cmd := exec.Command("sh", "./install.sh")

	data, err := cmd.Output()
	if err != nil {
		// The program has exited with an exit code != 0
		return "", fmt.Errorf("installation error: %v\n", string(data))
	}

	return string(data), nil
}
