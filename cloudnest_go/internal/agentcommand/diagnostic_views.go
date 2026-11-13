package agentcommand

import (
	"fmt"
	"strconv"
	"strings"

	"cloudenest_go/internal/agentstatus"
	"cloudenest_go/internal/frpwire"
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
		plain, err := m.keys.OpenConfig(device, cfg.ID, cfg.ServerID, cfg.ServerRevisionID, cfg.ConfigText)
		if err != nil {
			continue // Preserve historical reports even if a key is unavailable.
		}
		config, err := frpwire.Decode([]byte(plain))
		if err == nil && len(config.Proxies) <= 512 {
			ids := make([]int64, len(config.Proxies))
			for pos, proxy := range config.Proxies {
				ids[pos] = diagnosticMappingID(device, proxy.Name)
			}
			mappingIDs[cfg.ID] = ids
		}
	}
	for i := range rows {
		row := &rows[i]
		report := row.Diagnostic
		if report == nil || len(report.ProxyStates) == 0 {
			continue
		}
		configured := mappingIDs[report.ConfigID]
		if len(configured) != report.Expected {
			continue
		}
		for pos, id := range configured {
			state := (report.ProxyStates[pos/4] >> (2 * (pos % 4))) & 3
			row.DiagnosticProxies = append(row.DiagnosticProxies, agentstatus.ProxyView{
				MappingID: id,
				State:     [...]string{"pending", "registered", "failed"}[state],
			})
		}
	}
	return nil
}

func diagnosticMappingID(device int64, name string) int64 {
	suffix, ok := strings.CutPrefix(name, fmt.Sprintf("device_%d_map_", device))
	if !ok {
		return 0
	}
	id, err := strconv.ParseInt(suffix, 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != suffix {
		return 0
	}
	return id
}
