package scenarios

import (
	"context"
	"errors"
	"fmt"
	"github.com/ExchangeUnion/xud-simulation/xudrpc"
	"github.com/ExchangeUnion/xud-simulation/xudtest"
	"github.com/stretchr/testify/require"
)

func AddPair(assert *require.Assertions, ctx context.Context, node *xudtest.HarnessNode, baseCurrency string, quoteCurrency string,
	swapClient xudrpc.AddCurrencyRequest_SwapClient) {
	// Check the current number of pairs.
	resInfo, err := node.Client.GetInfo(context.Background(), &xudrpc.GetInfoRequest{})
	assert.NoError(err)

	prevNumPairs := resInfo.NumPairs

	// Add currencies.
	reqAddCurr := &xudrpc.AddCurrencyRequest{Currency: baseCurrency, SwapClient: swapClient}
	_, err = node.Client.AddCurrency(ctx, reqAddCurr)
	assert.NoError(err)

	reqAddCurr = &xudrpc.AddCurrencyRequest{Currency: quoteCurrency, SwapClient: swapClient}
	_, err = node.Client.AddCurrency(ctx, reqAddCurr)
	assert.NoError(err)

	// Add pair.
	reqAddPair := &xudrpc.AddPairRequest{BaseCurrency: baseCurrency, QuoteCurrency: quoteCurrency}
	_, err = node.Client.AddPair(ctx, reqAddPair)
	assert.NoError(err)

	// Verify that pair was added.
	resGetInfo, err := node.Client.GetInfo(context.Background(), &xudrpc.GetInfoRequest{})
	assert.NoError(err)
	assert.Equal(resGetInfo.NumPairs, prevNumPairs+1)
}

func Connect(assert *require.Assertions, ctx context.Context, srcNode, destNode *xudtest.HarnessNode) {
	destNodeUri := fmt.Sprintf("%v@%v",
		destNode.PubKey(),
		destNode.Cfg.P2PAddr(),
	)

	// Connect srcNode to destNode.
	reqConn := &xudrpc.ConnectRequest{NodeUri: destNodeUri}
	_, err := srcNode.Client.Connect(ctx, reqConn)
	assert.NoError(err)

	// Verify srcNode's peer (destNode).
	resListPeers, err := srcNode.Client.ListPeers(ctx, &xudrpc.ListPeersRequest{})
	assert.NoError(err)
	assert.Len(resListPeers.Peers, 1)
	assert.Equal(resListPeers.Peers[0].NodePubKey, destNode.PubKey())
	assert.Equal(resListPeers.Peers[0].LndBtcPubKey, destNode.LndBtcNode.PubKeyStr)
	assert.Equal(resListPeers.Peers[0].LndLtcPubKey, destNode.LndLtcNode.PubKeyStr)

	// Verify destNode's peer (srcNode).
	resListPeers, err = destNode.Client.ListPeers(context.Background(), &xudrpc.ListPeersRequest{})
	assert.NoError(err)
	assert.Len(resListPeers.Peers, 1)
	assert.Equal(resListPeers.Peers[0].NodePubKey, srcNode.PubKey())
	assert.Equal(resListPeers.Peers[0].LndBtcPubKey, srcNode.LndBtcNode.PubKeyStr)
	assert.Equal(resListPeers.Peers[0].LndLtcPubKey, srcNode.LndLtcNode.PubKeyStr)
}

func PlaceOrderAndBroadcast(assert *require.Assertions, ctx context.Context, srcNode, destNode *xudtest.HarnessNode,
	req *xudrpc.PlaceOrderRequest) *xudrpc.Order {
	// 	Fetch nodes current order book state.
	prevSrcNodeCount, prevDestNodeCount, err := getOrdersCount(ctx, srcNode, destNode)
	assert.NoError(err)

	// Subscribe to added orders on destNode
	destNodeAddedOrderChan := subscribeAddedOrder(ctx, destNode)

	// Place the order on srcNode and verify the result.
	res, err := srcNode.Client.PlaceOrderSync(ctx, req)
	assert.NoError(err)

	// Verify the response.
	assert.Len(res.InternalMatches, 0)
	assert.Len(res.SwapSuccesses, 0)
	assert.NotNil(res.RemainingOrder)
	assert.NotEqual(res.RemainingOrder.Id, req.OrderId)
	assert.IsType(new(xudrpc.Order_LocalId), res.RemainingOrder.OwnOrPeer)
	assert.Equal(res.RemainingOrder.OwnOrPeer.(*xudrpc.Order_LocalId).LocalId, req.OrderId)

	// Retrieve and verify the added order event on destNode.
	e := <-destNodeAddedOrderChan
	assert.NoError(e.err)
	assert.NotNil(e.order)
	peerOrder := e.order

	// Verify the peer order.
	assert.NotEqual(peerOrder.Id, req.OrderId) 	// Local id should not equal the global id.
	assert.Equal(peerOrder.Price, req.Price)
	assert.Equal(peerOrder.PairId, req.PairId)
	assert.Equal(peerOrder.Quantity, req.Quantity)
	assert.Equal(peerOrder.Side, req.Side)
	assert.False(peerOrder.IsOwnOrder)
	assert.Equal(peerOrder.Id, res.RemainingOrder.Id)
	assert.IsType(new(xudrpc.Order_PeerPubKey), peerOrder.OwnOrPeer)
	assert.Equal(peerOrder.OwnOrPeer.(*xudrpc.Order_PeerPubKey).PeerPubKey, srcNode.PubKey())

	// Verify that a new order was added to the order books.
	srcNodeCount, destNodeCount, err := getOrdersCount(ctx, srcNode, destNode)
	assert.NoError(err)
	assert.Equal(srcNodeCount.Own, prevSrcNodeCount.Own+1)
	assert.Equal(srcNodeCount.Peer, prevSrcNodeCount.Peer)
	assert.Equal(destNodeCount.Own, prevDestNodeCount.Own)
	assert.Equal(destNodeCount.Peer, prevDestNodeCount.Peer+1)

	return res.RemainingOrder
}

func RemoveOrderAndInvalidate(assert *require.Assertions, ctx context.Context, srcNode, destNode *xudtest.HarnessNode, order *xudrpc.Order) {
	// 	Fetch nodes current order book state.
	prevSrcNodeCount, prevDestNodeCount, err := getOrdersCount(ctx, srcNode, destNode)
	assert.NoError(err)

	// Subscribe to removed orders on destNode.
	destNodeRemovedOrdersChan := subscribeRemovedOrders(ctx, destNode)

	// Remove the order on srcNode.
	req := &xudrpc.RemoveOrderRequest{OrderId: order.OwnOrPeer.(*xudrpc.Order_LocalId).LocalId}
	res, err := srcNode.Client.RemoveOrder(ctx, req)
	assert.NoError(err)

	// Verify no quantity on hold.
	assert.Equal(res.QuantityOnHold, 0.0)

	// Retrieve and verify the removed orders event on destNode.
	e := <-destNodeRemovedOrdersChan
	assert.NoError(e.err)
	assert.NotNil(e.orderRemoval)

	// Verify the order removal.
	assert.Empty(e.orderRemoval.LocalId)
	assert.Equal(e.orderRemoval.Quantity, order.Quantity)
	assert.Equal(e.orderRemoval.PairId, order.PairId)
	assert.False(e.orderRemoval.IsOwnOrder)

	// Verify that the order was removed from the order books.
	srcNodeCount, destNodeCount, err := getOrdersCount(ctx, srcNode, destNode)
	assert.NoError(err)
	assert.Equal(srcNodeCount.Own, prevSrcNodeCount.Own-1)
	assert.Equal(srcNodeCount.Peer, prevSrcNodeCount.Peer)
	assert.Equal(destNodeCount.Own, prevDestNodeCount.Own)
	assert.Equal(destNodeCount.Peer, prevDestNodeCount.Peer-1)
}

func PlaceOrderAndSwap(assert *require.Assertions, ctx context.Context, srcNode, destNode *xudtest.HarnessNode,
	req *xudrpc.PlaceOrderRequest) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	destNodeSwapChan := subscribeSwaps(ctx, destNode, false)
	srcNodeSwapChan := subscribeSwaps(ctx, srcNode, true)

	// Place the order on srcNode and verify the result.
	res, err := srcNode.Client.PlaceOrderSync(ctx, req)
	assert.NoError(err)
	assert.Len(res.InternalMatches, 0)
	assert.Len(res.SwapSuccesses, 1)
	assert.Nil(res.RemainingOrder)

	// Retrieve and verify the swap events on both nodes.
	eMaker := <-destNodeSwapChan
	assert.NoError(eMaker.err)
	assert.NotNil(eMaker.swap)
	eTaker := <-srcNodeSwapChan
	assert.NoError(eTaker.err)
	assert.NotNil(eTaker.swap)

	// Verify that the swap event on the taker side is equal to PlaceOrder response swap.
	assert.Equal(eTaker.swap, res.SwapSuccesses[0])

	fmt.Printf("### %v\n\n", eTaker.swap)
	fmt.Printf("### %v\n\n", eMaker.swap)
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

func subscribeRemovedOrders(ctx context.Context, node *xudtest.HarnessNode) <-chan *subscribeOrderRemovalResult {
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
