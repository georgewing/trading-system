// 交易引擎核心数据类型定义

package engine

// Side 表示买/卖方向
type Side int8

const (
	SideBuy  Side = 1  // "BUY"
	SideSell Side = -1 //"SELL"
)

// OrderType 订单类型
type OrderType string

const (
	OrderTypeLimit  OrderType = "LIMIT"
	OrderTypeMarket OrderType = "MARKET"
)

// TimeInForce 表示时效策略

type TimeInForce string

const (
	TimeInForceGTC TimeInForce = "GTC"
	TimeInForceIOC TimeInForce = "IOC"
	TimeInForceFOK TimeInForce = "FOK"
)

// OrderStatus 订单状态
type OrderStatus int8

const (
	OrderStatusNew             OrderStatus = 1
	OrderStatusPartiallyFilled OrderStatus = 2
	OrderStatusFilled          OrderStatus = 3
	OrderStatusCanceled        OrderStatus = 4
	OrderStatusRejected        OrderStatus = 5
)

// ExecType describes the execution event that produced a report. It is
// intentionally separate from OrderStatus: an IOC order can have a final
// ExecTypeCanceled while its OrderStatus remains PartiallyFilled.
type ExecType string

const (
	ExecTypeNew      ExecType = "NEW"
	ExecTypeTrade    ExecType = "TRADE"
	ExecTypeCanceled ExecType = "CANCELED"
	ExecTypeRejected ExecType = "REJECTED"
)

// Liquidity identifies whether the reported fill was maker or taker.
type Liquidity string

const (
	LiquidityMaker Liquidity = "MAKER"
	LiquidityTaker Liquidity = "TAKER"
)

// Order 订单结构体
type Order struct {
	ID            uint64 // 系统级唯一订单ID
	ClientOrderID string // 客户侧ID（可选）
	AccountID     uint64
	Symbol        string
	// Strategy tags the order so risk breaks and metrics can be scoped to one
	// strategy. Manual orders and legacy clients leave it empty; consumers
	// normalize an empty value to "default".
	Strategy string

	// 订单核心属性
	Side     Side
	Type     OrderType
	TIF      TimeInForce
	Price    int64  // 以最小价格单位表示
	Quantity string // 原始委托数量
	Leaves   int64  // 剩余未成交数量
	Filled   int64  // 已成交数量
	Status   OrderStatus

	// 时间戳（纳秒）
	CreatedAt int64
	UpdatedAt int64

	// orderbook 内部链表指针，用于快速插入/删除（仅 engine/orderbook 内部使用）
	prev *Order
	next *Order

	// 内部索引/标记（可用于对象池或数组索引）
	index int32

	// 扩展标志
	Hidden   bool // 隐藏单
	PostOnly bool // 仅挂单
}

// PriceLevel 表示一个价位上的聚合信息以及链表入口（book 内部使用）
type PriceLevel struct {
	Price int64 // 价位（最小价格单位）
	Size  int64 // 该价位上的聚合剩余量
	Count int64 // 订单数

	// 链表头尾指针，按 insertion order 串联 Order.prev/next
	head *Order
	tail *Order
}

// LevelSnapshot 用于对外快照（简化）
type LevelSnapshot struct {
	Price int64
	Size  int64
	Count int64
}

// OrderBookSnapshot 用于对外/UI 的深度快照
type OrderBookSnapshot struct {
	Symbol    string
	Bids      []LevelSnapshot // 按价格降序
	Asks      []LevelSnapshot // 按价格升序
	Timestamp int64
}

// Trade 成交记录。
type Trade struct {
	ID          uint64
	Symbol      string
	Price       int64
	Quantity    int64
	BuyOrderID  uint64
	SellOrderID uint64
	Timestamp   int64
	MakerOrder  uint64 // maker order id

	// MakerAccountID/MakerSide identify the resting side of the trade. They are
	// zero for external venues that do not expose counterparty accounts.
	MakerAccountID uint64
	MakerSide      Side
}

// ExecReport 为执行反馈/回报结构。
type ExecReport struct {
	ExecID        uint64
	Seq           uint64
	OrderID       uint64
	ClientOrderID string
	AccountID     uint64
	Symbol        string
	Side          Side
	// Strategy echoes the order's strategy tag so persistence and risk can
	// attribute the report without another lookup.
	Strategy string

	ExecType  ExecType
	Status    OrderStatus
	Filled    int64
	Leaves    int64
	LastPx    int64
	LastQty   int64
	CumQty    int64
	LeavesQty int64
	Liquidity Liquidity

	// Order metadata allows persistence sinks to upsert the order row without
	// a separate lookup.
	OrderPx   int64
	OrderType OrderType
	TIF       TimeInForce
	Quantity  string

	Trade     *Trade
	Reason    string
	Venue     string
	Timestamp int64

	// MakerReported marks a taker report whose maker legs are published as
	// separate reports. Risk accounting uses it to avoid booking the maker leg
	// twice.
	MakerReported bool
}
