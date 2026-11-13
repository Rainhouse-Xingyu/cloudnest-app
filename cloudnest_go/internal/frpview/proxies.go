// Package frpview builds public metadata from a device's saved FRP configuration.
package frpview

import (
	"fmt"
	"strconv"
	"strings"

	"cloudenest_go/internal/agentstatus"
	"cloudenest_go/internal/frpconfig"
	"cloudenest_go/internal/frpwire"
	"cloudenest_go/model"
)

// MappingIDs returns IDs in configuration order. Unrecognized names have ID 0.
// Missing keys and unreadable configurations leave the snapshot unenriched.
func MappingIDs(keys *frpconfig.Keyring, device int64, cfg model.FrpConfig) []int64 {
	if keys == nil || cfg.DeviceID == nil || *cfg.DeviceID != device {
		return nil
	}
	plain, err := keys.OpenConfig(device, cfg.ID, cfg.ServerID, cfg.ServerRevisionID, cfg.ConfigText)
	if err != nil {
		return nil
	}
	config, err := frpwire.Decode([]byte(plain))
	if err != nil || len(config.Proxies) > 512 {
		return nil
	}
	ids := make([]int64, len(config.Proxies))
	prefix := fmt.Sprintf("device_%d_map_", device)
	for i, proxy := range config.Proxies {
		suffix, ok := strings.CutPrefix(proxy.Name, prefix)
		if !ok {
			continue
		}
		id, err := strconv.ParseInt(suffix, 10, 64)
		if err == nil && id > 0 && strconv.FormatInt(id, 10) == suffix {
			ids[i] = id
		}
	}
	return ids
}

// Registration joins validated packed results with the original proxy positions.
func Registration(report *agentstatus.Report, ids []int64) []agentstatus.ProxyView {
	if report == nil || len(report.ProxyStates) == 0 || report.Validate() != nil || len(ids) != report.Expected {
		return nil
	}
	states := [...]string{"pending", "registered", "failed"}
	out := make([]agentstatus.ProxyView, len(ids))
	for i, id := range ids {
		state := (report.ProxyStates[i/4] >> (2 * (i % 4))) & 3
		out[i] = agentstatus.ProxyView{MappingID: id, State: states[state]}
	}
	return out
}
