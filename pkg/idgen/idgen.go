package idgen

import (
	"github.com/bwmarrin/snowflake"
)

var node *snowflake.Node

// Init
func init(workerID int64) error {
	var err error
	node, err = snowflake.NewNode(workerID)
	return err
}

// NextID 生成一个递增的唯一 uint64
func NextID() uint64 {
	return uint64(node.Generate().Int64())
}
