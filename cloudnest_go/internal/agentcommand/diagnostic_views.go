package agentcommand

import (
	"cloudenest_go/internal/frpview"
	"cloudenest_go/model"
	"gorm.io/gorm"
)

func (m *Manager) attachDiagnosticProxies(tx *gorm.DB, device int64, rows []model.AgentCommand) error {
	ids := make(map[int64]struct{})
	for _, row := range rows {
		if row.Diagnostic != nil && len(row.Diagnostic.ProxyStates) != 0 {
			ids[row.Diagnostic.ConfigID] = struct{}{}
		}
	}
	if len(ids) == 0 || m.keys == nil {
		return nil
	}
	configIDs := make([]int64, 0, len(ids))
	for id := range ids {
		configIDs = append(configIDs, id)
	}
	var configs []model.FrpConfig
	if err := tx.Where("device_id=? AND id IN ?", device, configIDs).Find(&configs).Error; err != nil {
		return err
	}
	mappingIDs := make(map[int64][]int64, len(configs))
	for _, cfg := range configs {
		mappingIDs[cfg.ID] = frpview.MappingIDs(m.keys, device, cfg)
	}
	for i := range rows {
		row := &rows[i]
		if row.Diagnostic != nil {
			row.DiagnosticProxies = frpview.Registration(row.Diagnostic, mappingIDs[row.Diagnostic.ConfigID])
		}
	}
	return nil
}
