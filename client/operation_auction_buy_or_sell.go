package client

import (
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chriisong/albion-scanner-private/lib"
	"github.com/chriisong/albion-scanner-private/log"
)

// TradeReceiptSubject is the NATS subject the Python subscriber binds to via
// Config.nats_trade_receipts_subject. Changing this string requires coordinating
// both ends — the Python parser is in src/albion_flipper/trade_receipts.py.
const TradeReceiptSubject = "albion.private.trade_receipts"

// MarketOrderCacheSize mirrors AFM TradeService.marketOrdersCache cap.
const MarketOrderCacheSize = 500

// TradeReceipt is the on-wire JSON schema locked in the private-scan-pipeline
// spec. Field names are the authoritative contract with the Python parser.
type TradeReceipt struct {
	Direction   string `json:"direction"`
	ItemID      string `json:"item_id"`
	Quality     int    `json:"quality"`
	Enchantment int    `json:"enchantment"`
	LocationID  int    `json:"location_id"`
	UnitPrice   int    `json:"unit_price"`
	Amount      int    `json:"amount"`
	TotalSilver int    `json:"total_silver"`
	ObservedAt  string `json:"observed_at"`
}

// operationAuctionBuyOffer captures an instant-buy request.
// Ported from AFM AuctionBuyOfferRequest: param[1]=amount, param[2]=orderId.
type operationAuctionBuyOffer struct {
	Amount  int    `mapstructure:"1"`
	OrderID uint64 `mapstructure:"2"`
}

func (op *operationAuctionBuyOffer) Process(state *albionState) {
	log.Debugf("AuctionBuyOffer request: amount=%d orderId=%d", op.Amount, op.OrderID)
	stageUnconfirmedTrade(state, op.OrderID, op.Amount, "buy")
}

// operationAuctionBuyOfferResponse confirms the instant-buy.
// AFM no longer checks a success key — per the upstream comment in
// AuctionBuyOfferResponse.cs, "there's no key 0 to pass success anymore, so we
// assume always success". We mirror that: any response publishes the staged
// trade.
type operationAuctionBuyOfferResponse struct{}

func (op *operationAuctionBuyOfferResponse) Process(state *albionState) {
	log.Debug("AuctionBuyOffer response; confirming trade")
	confirmUnconfirmedTrade(state)
}

// operationAuctionSellSpecificItemRequest captures an instant-sell request.
// Ported from AFM AuctionSellSpecificItemRequestRequest: param[1]=orderId,
// param[4]=amount. Note the different parameter indexes from BuyOffer.
type operationAuctionSellSpecificItemRequest struct {
	OrderID uint64 `mapstructure:"1"`
	Amount  int    `mapstructure:"4"`
}

func (op *operationAuctionSellSpecificItemRequest) Process(state *albionState) {
	log.Debugf("AuctionSellSpecificItemRequest: amount=%d orderId=%d", op.Amount, op.OrderID)
	stageUnconfirmedTrade(state, op.OrderID, op.Amount, "sell")
}

type operationAuctionSellSpecificItemRequestResponse struct{}

func (op *operationAuctionSellSpecificItemRequestResponse) Process(state *albionState) {
	log.Debug("AuctionSellSpecificItemRequest response; confirming trade")
	confirmUnconfirmedTrade(state)
}

// stageUnconfirmedTrade resolves the cached MarketOrder and stages a trade
// receipt for publication on the paired response. A cache miss is logged and
// skipped — matches AFM's silent behavior when GetMarketOrderFromCache returns
// null (the client never saw the corresponding market search, so we cannot
// enrich).
func stageUnconfirmedTrade(state *albionState, orderID uint64, amount int, direction string) {
	order, ok := state.lookupMarketOrder(orderID)
	if !ok {
		log.Errorf("Trade request: order %d not in market cache; skipping receipt", orderID)
		return
	}
	state.setUnconfirmedTrade(tradeReceiptFromOrder(order, amount, direction))
}

// confirmUnconfirmedTrade publishes the staged trade receipt, if any, to the
// private broker. Single-slot pending state matches AFM's TradeService.
func confirmUnconfirmedTrade(state *albionState) {
	trade, ok := state.takeUnconfirmedTrade()
	if !ok {
		log.Debug("No unconfirmed trade to confirm")
		return
	}
	publishTradeReceipt(trade, state)
}

// publishTradeReceipt marshals and sends to the configured private broker.
// The trade receipt is not personalized (CharacterId/Name are intentionally
// not part of the locked JSON schema) so it routes through the public-upload
// path helper — the fork has collapsed both helpers onto a single broker anyway.
func publishTradeReceipt(trade TradeReceipt, state *albionState) {
	if ConfigGlobal.DisableUpload {
		log.Info("Upload disabled; skipping trade receipt")
		return
	}
	data, err := json.Marshal(trade)
	if err != nil {
		log.Errorf("Trade receipt marshal: %v", err)
		return
	}
	if dis == nil || dis.brokerUploader == nil {
		log.Debug("Trade receipt: broker uploader not configured; skipping emission")
		return
	}
	dis.brokerUploader.sendToIngest(data, TradeReceiptSubject, state, "")
	log.Infof("Published %s trade: %s x%d @ %d silver each", trade.Direction, trade.ItemID, trade.Amount, trade.UnitPrice)
}

// tradeReceiptFromOrder projects a cached MarketOrder + request amount onto
// the locked JSON schema. Albion stores prices in 10000-units-per-silver; the
// integer division matches AFM's `/ 10000.0` normalization (distance fees are
// not surfaced by Go upstream's MarketOrder struct and are excluded here;
// phase-5 parity check in Task 10 will flag any divergence).
func tradeReceiptFromOrder(order *lib.MarketOrder, amount int, direction string) TradeReceipt {
	unitPrice := order.Price / 10000
	return TradeReceipt{
		Direction:   direction,
		ItemID:      order.ItemID,
		Quality:     order.QualityLevel,
		Enchantment: order.EnchantmentLevel,
		LocationID:  parseLocationIDToInt(order.LocationID),
		UnitPrice:   unitPrice,
		Amount:      amount,
		TotalSilver: unitPrice * amount,
		ObservedAt:  time.Now().UTC().Format(time.RFC3339),
	}
}

// parseLocationIDToInt converts AFM-style location strings to integer IDs.
// Numeric prefixes like "0007" become 7; the "@Hideout-xxx" cluster suffix is
// stripped. Non-numeric strings (BLACKBANK, island UUIDs) fall back to -2,
// matching AFM's IdInt fallback sentinel.
func parseLocationIDToInt(s string) int {
	if s == "" {
		return -2
	}
	if idx := strings.Index(s, "@"); idx >= 0 {
		s = s[:idx]
	}
	if s == "" {
		return -2
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return -2
	}
	return n
}

// --- albionState extensions -------------------------------------------------

// marketOrderCacheEntry plus unconfirmedTradeSlot live on albionState so the
// dispatcher's concurrent Process goroutines share one cache/slot. Mutex-
// guarded to prevent races from the router's `go op.Process(state)` fanout.

type marketOrderCache struct {
	mu     sync.Mutex
	orders map[uint64]*lib.MarketOrder
	fifo   []uint64 // eviction order; oldest first
}

func (c *marketOrderCache) add(orders []*lib.MarketOrder) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.orders == nil {
		c.orders = make(map[uint64]*lib.MarketOrder, MarketOrderCacheSize)
	}
	for _, o := range orders {
		if o == nil {
			continue
		}
		id := uint64(o.ID)
		if _, seen := c.orders[id]; seen {
			continue
		}
		if len(c.orders) >= MarketOrderCacheSize {
			oldest := c.fifo[0]
			c.fifo = c.fifo[1:]
			delete(c.orders, oldest)
		}
		c.orders[id] = o
		c.fifo = append(c.fifo, id)
	}
}

func (c *marketOrderCache) lookup(id uint64) (*lib.MarketOrder, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	o, ok := c.orders[id]
	return o, ok
}

type unconfirmedTradeSlot struct {
	mu    sync.Mutex
	trade *TradeReceipt
}

func (s *unconfirmedTradeSlot) set(t TradeReceipt) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.trade = &t
}

func (s *unconfirmedTradeSlot) take() (TradeReceipt, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.trade == nil {
		return TradeReceipt{}, false
	}
	out := *s.trade
	s.trade = nil
	return out, true
}

// Methods on *albionState route through the two helpers above. Kept separate
// from albion_state.go to keep the upstream diff surface minimal — everything
// trade-receipt-related lives in this file.

func (s *albionState) cacheMarketOrders(orders []*lib.MarketOrder) {
	s.tradeCache.add(orders)
}

func (s *albionState) lookupMarketOrder(id uint64) (*lib.MarketOrder, bool) {
	return s.tradeCache.lookup(id)
}

func (s *albionState) setUnconfirmedTrade(t TradeReceipt) {
	s.tradePending.set(t)
}

func (s *albionState) takeUnconfirmedTrade() (TradeReceipt, bool) {
	return s.tradePending.take()
}
