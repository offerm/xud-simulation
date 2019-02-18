package scenarios

import (
	"context"
	"errors"
	"fmt"
	"github.com/ExchangeUnion/xud-simulation/xudrpc"
	"github.com/ExchangeUnion/xud-simulation/xudtest"
	"reflect"
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
	req *xudrpc.PlaceOrderRequest) (*xudrpc.Order, error) {
	// 	Fetch nodes current order book state.
	prevSrcNodeCount, prevDestNodeCount, err := getOrdersCount(ctx, srcNode, destNode)
	if err != nil {
		return nil, err
	}

	// Subscribe to added orders on destNode
	destNodeAddedOrderChan := subscribeAddedOrder(ctx, destNode)

	// Place the order on srcNode and verify the result.
	res, err := srcNode.Client.PlaceOrderSync(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("PlaceOrderSync: %v", err)
	}
	if len(res.InternalMatches) != 0 {
		return nil, errors.New("PlaceOrderSync: unexpected internal matches")
	}

	if len(res.SwapSuccesses) != 0 {
		return nil, fmt.Errorf("PlaceOrderSync: unexpected swap successes")
	}

	if res.RemainingOrder == nil {
		return nil, errors.New("PlaceOrderSync: expected remaining order missing")
	}

	if res.RemainingOrder.Id == req.OrderId {
		return nil, errors.New("PlaceOrderSync: received order local id as global id")
	}

	if val, ok := res.RemainingOrder.OwnOrPeer.(*xudrpc.Order_LocalId); !ok || val.LocalId != req.OrderId {
		return nil, errors.New("PlaceOrderSync: invalid order local id")
	}

	// Retrieve and verify the added order on destNode.
	r := <-destNodeAddedOrderChan
	if r.err != nil {
		return nil, r.err
	}
	peerOrder := r.order

	if peerOrder.Id == req.OrderId {
		return nil, errors.New("received order with local id as global id")
	}

	if val, ok := peerOrder.OwnOrPeer.(*xudrpc.Order_PeerPubKey); !ok || val.PeerPubKey != srcNode.PubKey() {
		return nil, errors.New("received order with unexpected peerPubKey")
	}

	if peerOrder.Price != req.Price ||
		peerOrder.PairId != req.PairId ||
		peerOrder.Quantity != req.Quantity ||
		peerOrder.Side != req.Side ||
		peerOrder.IsOwnOrder == true {
		return nil, errors.New("received unexpected order")
	}

	if peerOrder.Id != res.RemainingOrder.Id {
		return nil, errors.New("received order with inconsistent global id")
	}

	// Fetch nodes new order book state.
	srcNodeCount, destNodeCount, err := getOrdersCount(ctx, srcNode, destNode)
	if err != nil {
		return nil, err
	}

	// Verify that a new order was added to the order book.
	if srcNodeCount.Own != prevSrcNodeCount.Own+1 {
		return nil, errors.New("added order is missing on the order count")
	}

	if destNodeCount.Peer != prevDestNodeCount.Peer+1 {
		return nil, errors.New("added order is missing on the orders count")
	}

	return res.RemainingOrder, nil
}

func RemoveOrderAndInvalidate(ctx context.Context, srcNode, destNode *xudtest.HarnessNode, order *xudrpc.Order) error {
	// 	Fetch nodes current order book state.
	prevSrcNodeCount, prevDestNodeCount, err := getOrdersCount(ctx, srcNode, destNode)
	if err != nil {
		return err
	}

	// Subscribe to order removal on destNode.
	destNodeOrderRemovalChan := subscribeOrderRemoval(ctx, destNode)

	// Remove the order on srcNode.
	req := &xudrpc.RemoveOrderRequest{OrderId: order.OwnOrPeer.(*xudrpc.Order_LocalId).LocalId}
	res, err := srcNode.Client.RemoveOrder(ctx, req)
	if err != nil {
		return fmt.Errorf("RemoveOrder: %v", err)
	}

	// Verify no quantity on hold.
	if res.QuantityOnHold != 0 {
		return errors.New("unexpected quantity on hold")
	}

	// Retrieve and verify the removed order on destNode.
	r := <-destNodeOrderRemovalChan
	if r.err != nil {
		return r.err
	}
	orderRemoval := r.orderRemoval

	if orderRemoval.LocalId != "" {
		return errors.New("unexpected order removal LocalId")
	}

	if orderRemoval.OrderId != order.Id ||
		orderRemoval.Quantity != order.Quantity ||
		orderRemoval.PairId != order.PairId ||
		orderRemoval.IsOwnOrder == true {
		return errors.New("unexpected order removal")
	}

	// Fetch nodes new order book state.
	srcNodeCount, destNodeCount, err := getOrdersCount(ctx, srcNode, destNode)
	if err != nil {
		return err
	}

	// Verify that a new order was added to the order book.
	if srcNodeCount.Own != prevSrcNodeCount.Own-1 {
		return errors.New("removed order exists on the order count")
	}

	if destNodeCount.Peer != prevDestNodeCount.Peer-1 {
		return errors.New("removed order exists on the order count")
	}

	return nil
}

func PlaceOrderAndSwap(ctx context.Context, srcNode, destNode *xudtest.HarnessNode,
	req *xudrpc.PlaceOrderRequest) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	destNodeSwapChan := subscribeSwaps(ctx, destNode, false)
	srcNodeSwapChan := subscribeSwaps(ctx, srcNode, true)

	// Place the order on srcNode and verify the result.
	res, err := srcNode.Client.PlaceOrderSync(ctx, req)
	if err != nil {
		return err
	}

	if len(res.InternalMatches) != 0 {
		return errors.New("PlaceOrderSync: unexpected internal matches")
	}

	if res.RemainingOrder != nil {
		return errors.New("PlaceOrderSync: unexpected remaining order")
	}

	if len(res.SwapSuccesses) != 1 {
		return errors.New("PlaceOrderSync: unexpected swap successes")
	}

	// Retrieve and verify the swap event on destNode.
	makerEvent := <-destNodeSwapChan
	if makerEvent.err != nil {
		return makerEvent.err
	}
	makerSwap := makerEvent.swap

	takerEvent := <-srcNodeSwapChan
	if takerEvent.err != nil {
		return takerEvent.err
	}
	takerSwap := takerEvent.swap

	if !reflect.DeepEqual(takerEvent.swap, res.SwapSuccesses[0]) {
		return errors.New("non-matching taker swap event and PlaceOrder response")
	}

	fmt.Printf("### %v\n\n", makerSwap)
	fmt.Printf("### %v\n\n", takerSwap)

	return nil
}

type subscribeAddedOrderResult struct {
	order *xudrpc.Order
	err   error
}

func subscribeAddedOrder(ctx context.Context, node *xudtest.HarnessNode) <-chan *subscribeAddedOrderResult {
	out := make(chan *subscribeAddedOrderResult, 1)

	// Synchronously subscribe to the node removed orders.
	stream, err := node.Client.SubscribeAddedOrders(ctx, &xudrpc.SubscribeAddedOrdersRequest{})
	if err != nil {
		out <- &subscribeAddedOrderResult{nil, fmt.Errorf("SubscribeAddedOrders: %v", err)}
		return out
	}

	go func() {
		recvChan := make(chan *xudrpc.Order)
		errChan := make(chan error)
		go func() {
			// Consume the subscription event.
			// This waits until the node notifies us
			// that it received an order.
			recvOrder, err := stream.Recv()
			if err != nil {
				errChan <- err
				return
			}

			// Order received.
			recvChan <- recvOrder
		}()

		// Verify that the order was received.
		select {
		case <-ctx.Done():
			out <- &subscribeAddedOrderResult{nil, errors.New("timeout reached before order was received")}
		case err := <-errChan:
			out <- &subscribeAddedOrderResult{nil, err}
		case peerOrder := <-recvChan:
			out <- &subscribeAddedOrderResult{peerOrder, nil}
		}
	}()

	return out
}

type subscribeOrderRemovalResult struct {
	orderRemoval *xudrpc.OrderRemoval
	err          error
}

func subscribeOrderRemoval(ctx context.Context, node *xudtest.HarnessNode) <-chan *subscribeOrderRemovalResult {
	out := make(chan *subscribeOrderRemovalResult, 1)

	// Synchronously subscribe to the node added orders.
	stream, err := node.Client.SubscribeRemovedOrders(ctx, &xudrpc.SubscribeRemovedOrdersRequest{})
	if err != nil {
		out <- &subscribeOrderRemovalResult{nil, fmt.Errorf("SubscribeRemovedOrders: %v", err)}
		return out
	}

	go func() {
		recvChan := make(chan *xudrpc.OrderRemoval)
		errChan := make(chan error)
		go func() {
			// Consume the subscription event.
			// This waits until the node notifies us
			// that it received an order.
			recvOrder, err := stream.Recv()
			if err != nil {
				errChan <- err
				return
			}

			// Order received.
			recvChan <- recvOrder
		}()

		// Verify that order invalidation was received.
		select {
		case <-ctx.Done():
			out <- &subscribeOrderRemovalResult{nil, errors.New("timeout reached before order was received")}
		case err := <-errChan:
			out <- &subscribeOrderRemovalResult{nil, err}
		case orderRemoval := <-recvChan:
			out <- &subscribeOrderRemovalResult{orderRemoval, nil}
		}
	}()

	return out
}

type subscribeSwapsEvent struct {
	swap *xudrpc.SwapSuccess
	err  error
}

func subscribeSwaps(ctx context.Context, node *xudtest.HarnessNode, includeTaker bool) <-chan *subscribeSwapsEvent {
	out := make(chan *subscribeSwapsEvent, 1)

	// Subscribe before starting a non-blocking goroutine.
	req := xudrpc.SubscribeSwapsRequest{IncludeTaker: includeTaker}
	stream, err := node.Client.SubscribeSwaps(ctx, &req)
	if err != nil {
		out <- &subscribeSwapsEvent{nil, fmt.Errorf("SubscribeSwaps: %v", err)}
		return out
	}

	go func() {
		go func() {
			for {
				swap, err := stream.Recv()
				out <- &subscribeSwapsEvent{swap, err}
				if err != nil {
					break
				}
			}
		}()

		select {
		case <-ctx.Done():
			if e := ctx.Err(); e != context.Canceled {
				out <- &subscribeSwapsEvent{nil, errors.New("timeout reached before order was received")}
			}
		}
	}()

	return out
}

func getOrdersCount(ctx context.Context, n1, n2 *xudtest.HarnessNode) (*xudrpc.OrdersCount, *xudrpc.OrdersCount, error) {
	n1i, err := getInfo(ctx, n1)
	if err != nil {
		return nil, nil, err
	}

	n2i, err := getInfo(ctx, n2)
	if err != nil {
		return nil, nil, err
	}

	return n1i.Orders, n2i.Orders, nil
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
