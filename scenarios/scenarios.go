package scenarios

import (
	"context"
	"errors"
	"fmt"
	"github.com/ExchangeUnion/xud-simulation/xudrpc"
	"github.com/ExchangeUnion/xud-simulation/xudtest"
)

func AddPair(ctx context.Context, n *xudtest.HarnessNode, baseCurrency string, quoteCurrency string,
	swapClient xudrpc.AddCurrencyRequest_SwapClient) error {
	// Check the current number of pairs.
	resInfo, err := n.Client.GetInfo(context.Background(), &xudrpc.GetInfoRequest{})
	if err != nil {
		return fmt.Errorf("RPC GetInfo failure: %v", err)
	}

	prevNumPairs := resInfo.NumPairs

	// Add currencies.
	reqAddCurr := &xudrpc.AddCurrencyRequest{Currency: baseCurrency, SwapClient: swapClient}
	if _, err := n.Client.AddCurrency(ctx, reqAddCurr); err != nil {
		return fmt.Errorf("RPC AddCurrency failure: %v", err)
	}
	reqAddCurr = &xudrpc.AddCurrencyRequest{Currency: quoteCurrency, SwapClient: swapClient}
	if _, err := n.Client.AddCurrency(ctx, reqAddCurr); err != nil {
		return fmt.Errorf("RPC AddCurrency failure: %v", err)
	}

	// Add pair.
	reqAddPair := &xudrpc.AddPairRequest{BaseCurrency: baseCurrency, QuoteCurrency: quoteCurrency}
	if _, err := n.Client.AddPair(ctx, reqAddPair); err != nil {
		return fmt.Errorf("RPC AddPair failure: %v", err)
	}

	// Verify that pair was added.
	resGetInfo, err := n.Client.GetInfo(context.Background(), &xudrpc.GetInfoRequest{})
	if err != nil {
		return fmt.Errorf("RPC GetInfo failure: %v", err)
	}
	if resGetInfo.NumPairs != prevNumPairs+1 {
		return fmt.Errorf("RPC GetInfo: added pair (%v/%v) is missing", baseCurrency, quoteCurrency)
	}

	return nil
}

func Connect(ctx context.Context, srcNode, destNode *xudtest.HarnessNode) error {
	destNodeUri := fmt.Sprintf("%v@%v",
		destNode.PubKey(),
		destNode.Cfg.P2PAddr(),
	)

	// Connect srcNode to destNode.
	reqConn := &xudrpc.ConnectRequest{NodeUri: destNodeUri}
	_, err := srcNode.Client.Connect(ctx, reqConn)
	if err != nil {
		return fmt.Errorf("RPC Connect failure: %v", err)
	}

	// Assert srcNode's peer (destNode).
	resListPeers, err := srcNode.Client.ListPeers(ctx, &xudrpc.ListPeersRequest{})
	if err != nil {
		return fmt.Errorf("RPC ListPeers failure: %v", err)
	}
	if len(resListPeers.Peers) != 1 {
		return fmt.Errorf("RPC ListPeers: peers are missing")
	}
	if err := assertPeersNum(resListPeers.Peers, 1); err != nil {
		return err
	}
	if err := assertPeerInfo(resListPeers.Peers[0], destNode); err != nil {
		return err
	}

	// Assert destNode's peer (srcNode).
	resListPeers, err = destNode.Client.ListPeers(context.Background(), &xudrpc.ListPeersRequest{})
	if err != nil {
		return fmt.Errorf("RPC ListPeers failure: %v", err)
	}
	if err := assertPeersNum(resListPeers.Peers, 1); err != nil {
		return err
	}
	if err := assertPeerInfo(resListPeers.Peers[0], srcNode); err != nil {
		return err
	}

	return nil
}

func PlaceOrderAndBroadcast(ctx context.Context, srcNode, destNode *xudtest.HarnessNode,
	pairId string, orderId string) error {
	// 	Fetch nodes current order book state.
	prevSrcNodeCount, err := getOrdersCount(ctx, srcNode)
	if err != nil {
		return nil
	}
	prevDestNodeCount, err := getOrdersCount(ctx, destNode)
	if err != nil {
		return nil
	}

	// Initialize the order.
	req := &xudrpc.PlaceOrderRequest{
		Price:    10,
		Quantity: 10,
		PairId:   pairId,
		OrderId:  orderId,
		Side:     xudrpc.OrderSide_BUY,
	}

	// Subscribe to added orders on destNode.
	stream, err := destNode.Client.SubscribeAddedOrders(ctx, &xudrpc.SubscribeAddedOrdersRequest{})
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
				!ok || val.PeerPubKey != srcNode.PubKey() {
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

	// Place the order on srcNode.
	res, err := srcNode.Client.PlaceOrderSync(ctx, req)
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

	// Verify that the order was received.
	select {
	case <-ctx.Done():
		return errors.New("timeout reached before order was received")
	case err := <-errChan:
		return err
	case <-recvChan:
	}

	// 	Fetch nodes new order book state.
	srcNodeCount, err := getOrdersCount(ctx, srcNode)
	if err != nil {
		return nil
	}
	destNodeCount, err := getOrdersCount(ctx, destNode)
	if err != nil {
		return nil
	}

	// Verify that a new order was added to the order book.
	if srcNodeCount.Own != prevSrcNodeCount.Own+1 {
		return errors.New("added order is missing on the order count")
	}

	if destNodeCount.Peer != prevDestNodeCount.Peer+1 {
		return errors.New("added order is missing on the orders count")
	}

	return nil
}

func getOrdersCount(ctx context.Context, n *xudtest.HarnessNode) (*xudrpc.OrdersCount, error) {
	info, err := getInfo(ctx, n)
	if err != nil {
		return nil, err
	}

	return info.Orders, nil
}

func getInfo(ctx context.Context, n *xudtest.HarnessNode) (*xudrpc.GetInfoResponse, error) {
	info, err := n.Client.GetInfo(ctx, &xudrpc.GetInfoRequest{})
	if err != nil {
		return nil, fmt.Errorf("RPC GetInfo failure: %v", err)
	}

	return info, nil
}

func assertPeersNum(p []*xudrpc.Peer, num int) error {
	if len(p) != num {
		return errors.New("invalid peers num")
	}

	return nil
}

func assertPeerInfo(p *xudrpc.Peer, n *xudtest.HarnessNode) error {
	if p.NodePubKey != n.PubKey() {
		return errors.New("invalid peer NodePubKey")
	}

	if p.LndBtcPubKey != n.LndBtcNode.PubKeyStr {
		return errors.New("invalid peer LndBtcPubKey")
	}

	if p.LndLtcPubKey != n.LndLtcNode.PubKeyStr {
		return errors.New("invalid peer LndLtcPubKey")
	}

	return nil
}
