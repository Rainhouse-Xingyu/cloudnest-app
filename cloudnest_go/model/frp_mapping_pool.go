package model

// FrpMappingPool is the database gate for the legacy global mapping namespace.
// It is seeded only by explicit migration; runtime never silently recreates it.
type FrpMappingPool struct {
	ID int64 `gorm:"primaryKey;autoIncrement:false"`
}

func (FrpMappingPool) TableName() string { return "frp_mapping_pool" }
