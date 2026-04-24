package client

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/chriisong/albion-scanner-private/lib"
)

// Ported from AlbionDataAvalonia's TradeService.cs + AuctionBuyOfferRequestHandler.cs +
// AuctionSellSpecificItemRequestRequestHandler.cs. Parameter index bindings
// (buy: [1]=amount [2]=orderId; sell: [1]=orderId [4]=amount) are taken
// verbatim from AFM's Request DTOs.

// --- decoder registration ---------------------------------------------------

func TestDecodeRequest_AuctionBuyOffer_ReturnsTypedStructWithAmountAndOrderID(t *testing.T) {
	params := map[uint8]interface{}{
		253: uint16(83),  // opAuctionBuyOffer
		1:   int32(5),    // amount (wire: typeCompressedInt)
		2:   int64(12345), // orderId (wire: typeCompressedLong)
	}
	op, err := decodeRequest(params)
	if err != nil {
		t.Fatalf("decodeRequest error: %v", err)
	}
	buy, ok := op.(*operationAuctionBuyOffer)
	if !ok {
		t.Fatalf("expected *operationAuctionBuyOffer, got %T", op)
	}
	if buy.Amount != 5 {
		t.Errorf("amount: want 5, got %d", buy.Amount)
	}
	if buy.OrderID != 12345 {
		t.Errorf("orderId: want 12345, got %d", buy.OrderID)
	}
}

func TestDecodeRequest_AuctionSellSpecificItemRequest_UsesParam1OrderIDAndParam4Amount(t *testing.T) {
	params := map[uint8]interface{}{
		253: uint16(315), // opAuctionSellSpecificItemRequest
		1:   int64(99999),
		4:   int32(7),
	}
	op, err := decodeRequest(params)
	if err != nil {
		t.Fatalf("decodeRequest error: %v", err)
	}
	sell, ok := op.(*operationAuctionSellSpecificItemRequest)
	if !ok {
		t.Fatalf("expected *operationAuctionSellSpecificItemRequest, got %T", op)
	}
	if sell.OrderID != 99999 {
		t.Errorf("orderId: want 99999, got %d", sell.OrderID)
	}
	if sell.Amount != 7 {
		t.Errorf("amount: want 7, got %d", sell.Amount)
	}
}

func TestDecodeResponse_AuctionBuyOffer_ReturnsResponseStruct(t *testing.T) {
	params := map[uint8]interface{}{253: uint16(83)}
	op, err := decodeResponse(params)
	if err != nil {
		t.Fatalf("decodeResponse error: %v", err)
	}
	if _, ok := op.(*operationAuctionBuyOfferResponse); !ok {
		t.Fatalf("expected *operationAuctionBuyOfferResponse, got %T", op)
	}
}

func TestDecodeResponse_AuctionSellSpecificItemRequest_ReturnsResponseStruct(t *testing.T) {
	params := map[uint8]interface{}{253: uint16(315)}
	op, err := decodeResponse(params)
	if err != nil {
		t.Fatalf("decodeResponse error: %v", err)
	}
	if _, ok := op.(*operationAuctionSellSpecificItemRequestResponse); !ok {
		t.Fatalf("expected *operationAuctionSellSpecificItemRequestResponse, got %T", op)
	}
}

// --- location id parsing ----------------------------------------------------

func TestParseLocationIDToInt(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"0007", 7},
		{"4", 4},
		{"0007@Hideout-abc", 7},
		{"", -2},
		{"BLACKBANK-xxx", -2},
		{"@", -2},
		{"@island", -2},
	}
	for _, c := range cases {
		if got := parseLocationIDToInt(c.in); got != c.want {
			t.Errorf("parseLocationIDToInt(%q): want %d, got %d", c.in, c.want, got)
		}
	}
}

// --- market-order cache -----------------------------------------------------

func TestAlbionState_CacheMarketOrders_EvictsOldestWhenFull(t *testing.T) {
	state := &albionState{}
	orders := make([]*lib.MarketOrder, MarketOrderCacheSize+5)
	for i := range orders {
		orders[i] = &lib.MarketOrder{ID: i + 1, ItemID: "T4_BAG"}
	}
	state.cacheMarketOrders(orders)

	if _, ok := state.lookupMarketOrder(1); ok {
		t.Error("expected oldest ID=1 to be evicted after cap overflow")
	}
	if _, ok := state.lookupMarketOrder(uint64(MarketOrderCacheSize + 5)); !ok {
		t.Error("expected newest ID to be present")
	}
}

func TestAlbionState_CacheMarketOrders_SkipsDuplicates(t *testing.T) {
	state := &albionState{}
	state.cacheMarketOrders([]*lib.MarketOrder{
		{ID: 42, ItemID: "T5_ORE"},
		{ID: 42, ItemID: "T5_ORE"}, // duplicate — AFM's cache also skips dups
	})
	order, ok := state.lookupMarketOrder(42)
	if !ok {
		t.Fatal("expected ID=42 cached")
	}
	if order.ItemID != "T5_ORE" {
		t.Errorf("expected ItemID=T5_ORE, got %q", order.ItemID)
	}
}

// --- buy/sell staging -------------------------------------------------------

func TestBuyOfferRequest_StagesTradeFromCacheWithBuyDirection(t *testing.T) {
	state := &albionState{}
	state.cacheMarketOrders([]*lib.MarketOrder{
		{
			ID:               42,
			ItemID:           "T6_RUNE",
			QualityLevel:     1,
			EnchantmentLevel: 0,
			Price:            15000000, // 1500 silver * 10000 units
			LocationID:       "0007",
			AuctionType:      "offer",
		},
	})

	(&operationAuctionBuyOffer{Amount: 3, OrderID: 42}).Process(state)

	trade, ok := state.takeUnconfirmedTrade()
	if !ok {
		t.Fatal("expected unconfirmed trade staged")
	}
	if trade.Direction != "buy" {
		t.Errorf("direction: want buy, got %q", trade.Direction)
	}
	if trade.ItemID != "T6_RUNE" {
		t.Errorf("item_id: want T6_RUNE, got %q", trade.ItemID)
	}
	if trade.Quality != 1 {
		t.Errorf("quality: want 1, got %d", trade.Quality)
	}
	if trade.Enchantment != 0 {
		t.Errorf("enchantment: want 0, got %d", trade.Enchantment)
	}
	if trade.LocationID != 7 {
		t.Errorf("location_id: want 7, got %d", trade.LocationID)
	}
	if trade.UnitPrice != 1500 {
		t.Errorf("unit_price: want 1500, got %d", trade.UnitPrice)
	}
	if trade.Amount != 3 {
		t.Errorf("amount: want 3, got %d", trade.Amount)
	}
	if trade.TotalSilver != 4500 {
		t.Errorf("total_silver: want 4500, got %d", trade.TotalSilver)
	}
	if trade.ObservedAt == "" {
		t.Error("observed_at: want non-empty ISO-8601 timestamp")
	}
	if _, err := time.Parse(time.RFC3339, trade.ObservedAt); err != nil {
		t.Errorf("observed_at not RFC3339: %q (%v)", trade.ObservedAt, err)
	}
}

func TestSellRequest_StagesTradeWithSellDirection(t *testing.T) {
	state := &albionState{}
	state.cacheMarketOrders([]*lib.MarketOrder{
		{
			ID:               7,
			ItemID:           "T5_ORE",
			QualityLevel:     2,
			EnchantmentLevel: 1,
			Price:            80000,
			LocationID:       "0004",
			AuctionType:      "request",
		},
	})

	(&operationAuctionSellSpecificItemRequest{OrderID: 7, Amount: 10}).Process(state)

	trade, ok := state.takeUnconfirmedTrade()
	if !ok {
		t.Fatal("expected unconfirmed trade staged")
	}
	if trade.Direction != "sell" {
		t.Errorf("direction: want sell, got %q", trade.Direction)
	}
	if trade.LocationID != 4 {
		t.Errorf("location_id: want 4, got %d", trade.LocationID)
	}
}

func TestBuyOfferRequest_CacheMissDoesNotStage(t *testing.T) {
	state := &albionState{}
	(&operationAuctionBuyOffer{Amount: 1, OrderID: 999}).Process(state)
	if _, ok := state.takeUnconfirmedTrade(); ok {
		t.Error("expected no trade staged on cache miss")
	}
}

// --- response publication ---------------------------------------------------

func TestBuyOfferResponse_PublishesStagedTradeToTradeReceiptsSubject(t *testing.T) {
	fake := &fakeUploader{}
	withTestDispatcher(t, fake, false, false)

	state := &albionState{}
	state.cacheMarketOrders([]*lib.MarketOrder{
		{ID: 1, ItemID: "T3_WOOD", QualityLevel: 1, Price: 200000, LocationID: "0007", AuctionType: "offer"},
	})
	(&operationAuctionBuyOffer{Amount: 2, OrderID: 1}).Process(state)
	(&operationAuctionBuyOfferResponse{}).Process(state)

	calls := fake.snapshot()
	if len(calls) != 1 {
		t.Fatalf("expected 1 broker call, got %d", len(calls))
	}
	if calls[0].topic != TradeReceiptSubject {
		t.Errorf("topic: want %q, got %q", TradeReceiptSubject, calls[0].topic)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(calls[0].body, &payload); err != nil {
		t.Fatalf("emitted body not valid JSON: %v (%s)", err, string(calls[0].body))
	}
	for _, key := range []string{
		"direction", "item_id", "quality", "enchantment",
		"location_id", "unit_price", "amount", "total_silver", "observed_at",
	} {
		if _, ok := payload[key]; !ok {
			t.Errorf("emitted JSON missing required key %q (payload=%v)", key, payload)
		}
	}
	if payload["direction"] != "buy" {
		t.Errorf("direction in payload: want buy, got %v", payload["direction"])
	}
	if payload["item_id"] != "T3_WOOD" {
		t.Errorf("item_id in payload: want T3_WOOD, got %v", payload["item_id"])
	}

	// Second response with nothing staged: must not publish.
	(&operationAuctionBuyOfferResponse{}).Process(state)
	if post := fake.snapshot(); len(post) != 1 {
		t.Errorf("expected no further broker call on empty pending slot, got %d total", len(post))
	}
}

func TestSellResponse_PublishesStagedTrade(t *testing.T) {
	fake := &fakeUploader{}
	withTestDispatcher(t, fake, false, false)

	state := &albionState{}
	state.cacheMarketOrders([]*lib.MarketOrder{
		{ID: 2, ItemID: "T4_LEATHER", QualityLevel: 1, Price: 500000, LocationID: "0004", AuctionType: "request"},
	})
	(&operationAuctionSellSpecificItemRequest{OrderID: 2, Amount: 4}).Process(state)
	(&operationAuctionSellSpecificItemRequestResponse{}).Process(state)

	calls := fake.snapshot()
	if len(calls) != 1 {
		t.Fatalf("expected 1 broker call, got %d", len(calls))
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(calls[0].body, &payload); err != nil {
		t.Fatalf("emitted body not valid JSON: %v", err)
	}
	if payload["direction"] != "sell" {
		t.Errorf("direction in payload: want sell, got %v", payload["direction"])
	}
}

func TestResponse_WithoutPending_DoesNotPublish(t *testing.T) {
	fake := &fakeUploader{}
	withTestDispatcher(t, fake, false, false)
	(&operationAuctionBuyOfferResponse{}).Process(&albionState{})
	(&operationAuctionSellSpecificItemRequestResponse{}).Process(&albionState{})
	if calls := fake.snapshot(); len(calls) != 0 {
		t.Errorf("expected 0 broker calls when no pending trade, got %d", len(calls))
	}
}

func TestResponse_DisableUploadShortCircuits(t *testing.T) {
	fake := &fakeUploader{}
	withTestDispatcher(t, fake, true, false)
	state := &albionState{}
	state.cacheMarketOrders([]*lib.MarketOrder{
		{ID: 5, ItemID: "T4_BAG", QualityLevel: 1, Price: 10000, LocationID: "0007", AuctionType: "offer"},
	})
	(&operationAuctionBuyOffer{Amount: 1, OrderID: 5}).Process(state)
	(&operationAuctionBuyOfferResponse{}).Process(state)
	if calls := fake.snapshot(); len(calls) != 0 {
		t.Errorf("expected 0 broker calls when DisableUpload=true, got %d", len(calls))
	}
}

// TestTradeReceiptSubject_MatchesPythonContract locks the wire subject. The
// Python subscriber in src/albion_flipper/subscriber.py subscribes to
// Config.nats_trade_receipts_subject which defaults to the same string.
// Changing either end requires coordinating both.
func TestTradeReceiptSubject_MatchesPythonContract(t *testing.T) {
	if TradeReceiptSubject != "albion.private.trade_receipts" {
		t.Errorf("subject drift: Python parser binds to this exact string, got %q", TradeReceiptSubject)
	}
}
