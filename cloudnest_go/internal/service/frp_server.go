package service

import (
	"context"

	"cloudenest_go/internal/frpconfig"
	"cloudenest_go/internal/service/contract"
	"cloudenest_go/model"
)

func (s *FrpService) WriteServer(ctx context.Context, actor contract.FrpActor, id int64, input contract.FrpServerInput) (contract.FrpServerView, error) {
	return s.repo.WriteServer(ctx, actor, id, input)
}
func (s *FrpService) ListServers(ctx context.Context, actor contract.FrpActor, after int64, limit int) ([]contract.FrpServerView, error) {
	return s.repo.ListServers(ctx, actor, after, limit)
}
func (s *FrpService) GetServer(ctx context.Context, actor contract.FrpActor, id int64) (contract.FrpServerView, error) {
	return s.repo.GetServer(ctx, actor, id)
}
func (s *FrpService) buildServerConfig(ctx context.Context, uid int64, admin bool, req SaveConfigInput, preview bool) (model.FrpConfig, string, error) {
	if req.ServerAddr != "" || req.ServerPort != 0 || req.Token != "" {
		return model.FrpConfig{}, "", contract.ErrInvalidInput
	}
	return s.repo.BuildServerConfig(ctx, contract.FrpActor{UserID: uid, Admin: admin, ClientIP: req.ClientIP}, req.DeviceID, req.ServerID, req.ServerRevisionID, preview, func(spec frpconfig.ServerSpec, mappings []model.FrpMapping) (string, error) {
		return generateFRPCYaml(spec.Address, spec.Port, spec.Token, mappings)
	})
}
