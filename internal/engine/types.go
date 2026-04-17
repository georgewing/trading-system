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

// Order 订单结构体
type Order struct {
	ID            uint64 // 系统级唯一订单ID
	ClientOrderID string // 客户侧ID（可选）
	AccountID     uint64
	Symbol        string

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

// Trade 成交记录
type Trade struct {
	ID          uint64
	Symbol      string
	Price       int64
	Quantity    int64
	BuyOrderID  uint64
	SellOrderID uint64
	Timestamp   int64
	MakerOrder  uint64 // maker order id
}

// ExecReport 为执行反馈/回报结构
type ExecReport struct {
	OrderID   uint64
	Status    OrderStatus
	Filled    int64
	Leaves    int64
	Trade     *Trade
	Reason    string
	Timestamp int64
}
