package app

import (
	"context"
	"errors"
	"net/http"

	"cloudenest_go/internal/blob"
	"cloudenest_go/internal/config"
	"cloudenest_go/internal/handler"
	"cloudenest_go/internal/middleware"
	"cloudenest_go/internal/service"
	storageMysql "cloudenest_go/internal/storage/mysql"
	"cloudenest_go/internal/transfer"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

// Container 统一管理仓储、服务、处理器依赖。
type Container struct {
	Cfg   config.Config
	DB    *gorm.DB
	RDB   *redis.Client
	Blobs *blob.Manager

	PermissionRepo    *storageMysql.PermissionRepo
	OperationLogRepo  *storageMysql.OperationLogRepo
	AuthRepo          *storageMysql.AuthRepo
	AdminRepo         *storageMysql.AdminRepo
	SettingsRepo      *storageMysql.SettingsRepo
	StorageRepo       *storageMysql.StorageRepo
	FileRepo          *storageMysql.FileRepo
	CollaborationRepo *storageMysql.CollaborationRepo
	LivePhotoRepo     *storageMysql.LivePhotoRepo
	FrpRepo           *storageMysql.FrpRepo
	AIRepo            *storageMysql.AIRepo
	TxRunner          *storageMysql.TxRunner

	AuthzService         *service.AuthzService
	AuthService          *service.AuthService
	AdminService         *service.AdminService
	CollaborationService *service.CollaborationService
	SettingsService      *service.SettingsService
	StorageService       *service.StorageService
	OperationLogService  *service.OperationLogService
	FrpService           *service.FrpService
	LivePhotoService     *service.LivePhotoService
	AIService            *service.AIService
	FileService          *service.FileService

	AuthHandler          *handler.AuthHandler
	AdminHandler         *handler.AdminHandler
	FileHandler          *handler.FileHandler
	SettingsHandler      *handler.SettingsHandler
	StorageHandler       *handler.StorageHandler
	CollaborationHandler *handler.CollaborationHandler
	AIHandler            *handler.AIHandler
	FrpHandler           *handler.FrpHandler
	LivePhotoHandler     *handler.LivePhotoHandler
}

// NewContainer 负责集中构造依赖。
func NewContainer(cfg config.Config, db *gorm.DB, rdb *redis.Client) *Container {
	blobs := blob.NewManager(db)
	permissionRepo := storageMysql.NewPermissionRepo(db)
	operationLogRepo := storageMysql.NewOperationLogRepo(db)
	authRepo := storageMysql.NewAuthRepo(db)
	adminRepo := storageMysql.NewAdminRepo(db)
	settingsRepo := storageMysql.NewSettingsRepo(db)
	storageRepo := storageMysql.NewStorageRepo(db)
	fileRepo := storageMysql.NewFileRepo(db)
	collaborationRepo := storageMysql.NewCollaborationRepo(db)
	livePhotoRepo := storageMysql.NewLivePhotoRepo(db)
	frpRepo := storageMysql.NewFrpRepo(db)
	aiRepo := storageMysql.NewAIRepo(db)
	txRunner := storageMysql.NewTxRunner(db)

	authzService := service.NewAuthzService(permissionRepo)
	authService := service.NewAuthService(authRepo, rdb, cfg)
	adminService := service.NewAdminService(adminRepo, rdb)
	collaborationService := service.NewCollaborationService(collaborationRepo)
	settingsService := service.NewSettingsService(settingsRepo)
	storageService := service.NewStorageService(storageRepo)
	operationLogService := service.NewOperationLogService(operationLogRepo)
	frpService := service.NewFrpService(frpRepo, rdb)
	livePhotoService := service.NewLivePhotoService(livePhotoRepo)
	aiService := service.NewAIService(aiRepo, cfg, nil)
	fileService := service.NewFileService(fileRepo)

	return &Container{
		Cfg:   cfg,
		DB:    db,
		RDB:   rdb,
		Blobs: blobs,

		PermissionRepo:    permissionRepo,
		OperationLogRepo:  operationLogRepo,
		AuthRepo:          authRepo,
		AdminRepo:         adminRepo,
		SettingsRepo:      settingsRepo,
		StorageRepo:       storageRepo,
		FileRepo:          fileRepo,
		CollaborationRepo: collaborationRepo,
		LivePhotoRepo:     livePhotoRepo,
		FrpRepo:           frpRepo,
		AIRepo:            aiRepo,
		TxRunner:          txRunner,

		AuthzService:         authzService,
		AuthService:          authService,
		AdminService:         adminService,
		CollaborationService: collaborationService,
		SettingsService:      settingsService,
		StorageService:       storageService,
		OperationLogService:  operationLogService,
		FrpService:           frpService,
		LivePhotoService:     livePhotoService,
		AIService:            aiService,
		FileService:          fileService,

		AuthHandler:          &handler.AuthHandler{Svc: authService},
		AdminHandler:         &handler.AdminHandler{Svc: adminService},
		FileHandler:          &handler.FileHandler{Transfers: transfer.NewManager(db, cfg), Blobs: blobs, RDB: rdb, Cfg: cfg, Svc: fileService},
		SettingsHandler:      &handler.SettingsHandler{Svc: settingsService},
		StorageHandler:       &handler.StorageHandler{Svc: storageService},
		CollaborationHandler: &handler.CollaborationHandler{Svc: collaborationService},
		AIHandler:            &handler.AIHandler{Cfg: cfg, Svc: aiService},
		FrpHandler:           &handler.FrpHandler{Svc: frpService},
		LivePhotoHandler:     &handler.LivePhotoHandler{Svc: livePhotoService},
	}
}

// NewRouter 统一构建路由与中间件。
func (c *Container) NewRouter() *gin.Engine {
	r := gin.New()
	r.Use(gin.Logger(), middleware.RecoverHTTP())
	r.Use(middleware.ProtectBlobReads(c.Blobs))
	// Forwarding headers are untrusted until an explicit ingress policy exists.
	_ = r.SetTrustedProxies(nil)

	r.POST("/api/auth/login", middleware.LoginRateLimit(c.RDB), c.AuthHandler.Login)
	r.GET("/api/public/shares/:code", c.FileHandler.PublicShareInfo)
	r.GET("/api/public/shares/:code/download", c.FileHandler.PublicShareDownload)

	api := r.Group("/api")
	api.Use(middleware.Authenticate(c.RDB, c.Cfg.JWTSecret, func(ctx context.Context, userID int64) ([]string, error) {
		return c.AuthzService.ResolveUserPermissions(ctx, userID)
	}, func(ctx context.Context, userID int64) (middleware.Identity, error) {
		u, err := c.AuthRepo.GetUserProfile(ctx, userID)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return middleware.Identity{}, nil
		}
		if err != nil {
			return middleware.Identity{}, err
		}
		isAdmin, err := c.AuthRepo.CheckUserIsAdmin(ctx, userID)
		if err != nil {
			return middleware.Identity{}, err
		}
		return middleware.Identity{AuthVersion: u.AuthVersion, Username: u.Username, Enabled: u.Status != 0, IsAdmin: isAdmin}, nil
	}))

	api.POST("/auth/logout", c.AuthHandler.Logout)
	api.POST("/auth/logout-all", c.AuthHandler.LogoutAll)
	api.GET("/auth/me", c.AuthHandler.Me)

	api.GET("/resource/health", func(ctx *gin.Context) {
		ctx.JSON(http.StatusOK, gin.H{"message": "protected resource ok"})
	})

	api.GET("/files", c.FileHandler.List)
	api.POST("/files/folders", c.FileHandler.CreateFolder)
	api.PUT("/files/:id/rename", c.FileHandler.Rename)
	api.DELETE("/files/:id", c.FileHandler.Delete)
	api.POST("/files/upload/init", c.FileHandler.InitUpload)
	api.POST("/files/upload/chunk", middleware.UploadBodyBudget(c.RDB, middleware.UploadBodyTimeout), c.FileHandler.UploadChunk)
	api.POST("/files/upload/pause", c.FileHandler.PauseUpload)
	api.POST("/files/upload/resume", c.FileHandler.ResumeUpload)
	api.POST("/files/upload/cancel", c.FileHandler.CancelUpload)
	api.POST("/files/upload/complete", c.FileHandler.CompleteUpload)
	api.GET("/files/upload/progress", c.FileHandler.UploadProgress)
	api.GET("/files/search", c.FileHandler.SearchFiles)
	api.GET("/files/:id/download", c.FileHandler.Download)
	api.GET("/files/:id/download-zip", c.FileHandler.DownloadFolderZip)
	api.POST("/files/batch-download", c.FileHandler.BatchDownload)
	api.POST("/files/batch-delete", c.FileHandler.BatchDelete)
	api.GET("/files/:id/stream", c.FileHandler.StreamVideo)
	api.GET("/files/:id/thumbnail", c.FileHandler.Thumbnail)
	api.POST("/files/move", c.FileHandler.Move)
	api.POST("/files/copy", c.FileHandler.Copy)
	api.GET("/files/recycle-bin", c.FileHandler.RecycleBin)
	api.POST("/files/:id/restore", c.FileHandler.Restore)
	api.DELETE("/files/:id/permanent", c.FileHandler.PermanentDelete)
	api.GET("/files/:id/preview", c.FileHandler.Preview)
	api.POST("/files/:id/share", c.FileHandler.CreateShare)
	api.POST("/files/:id/collaborators", c.CollaborationHandler.GrantFilePermission)
	api.GET("/files/:id/collaborators", c.CollaborationHandler.ListFilePermissions)
	api.DELETE("/files/:id/collaborators/:user_id", c.CollaborationHandler.RevokeFilePermission)
	api.POST("/files/:id/versions", c.CollaborationHandler.CreateFileVersion)
	api.GET("/files/:id/versions", c.CollaborationHandler.ListFileVersions)
	api.GET("/files/:id/versions/:version/download", c.FileHandler.DownloadVersion)
	api.POST("/files/:id/versions/:version/restore", c.CollaborationHandler.RestoreFileVersion)
	api.POST("/workspaces", c.CollaborationHandler.CreateWorkspace)
	api.GET("/workspaces", c.CollaborationHandler.ListWorkspaces)
	api.POST("/workspaces/:id/members", c.CollaborationHandler.AddWorkspaceMember)
	api.GET("/workspaces/:id/members", c.CollaborationHandler.ListWorkspaceMembers)
	api.PUT("/workspaces/:id/members/:user_id", c.CollaborationHandler.UpdateWorkspaceMemberRole)
	api.DELETE("/workspaces/:id/members/:user_id", c.CollaborationHandler.RemoveWorkspaceMember)
	api.GET("/shares", c.FileHandler.ListShares)
	api.DELETE("/shares/:code", c.FileHandler.RevokeShare)
	api.GET("/operation-logs", c.FileHandler.ListOperationLogs)
	api.GET("/operation-logs/export", c.FileHandler.ExportOperationLogs)
	api.GET("/settings", c.SettingsHandler.GetSettings)
	api.PUT("/settings", c.SettingsHandler.UpsertSettings)
	api.GET("/settings/permissions", c.SettingsHandler.GetPermissionSummary)
	api.GET("/settings/rate-window", c.SettingsHandler.GetMyRateWindow)
	api.PUT("/settings/rate-window", c.SettingsHandler.UpdateMyRateWindow)
	api.GET("/storage/overview", c.StorageHandler.GetOverview)
	api.POST("/frp/devices", c.FrpHandler.CreateDevice)
	api.GET("/frp/devices", c.FrpHandler.ListDevices)
	api.GET("/frp/devices/:id", c.FrpHandler.GetDevice)
	api.PUT("/frp/devices/:id", c.FrpHandler.UpdateDevice)
	api.DELETE("/frp/devices/:id", c.FrpHandler.DeleteDevice)
	api.POST("/frp/mappings", c.FrpHandler.CreateMapping)
	api.GET("/frp/devices/:id/mappings", c.FrpHandler.ListDeviceMappings)
	api.PUT("/frp/mappings/:id", c.FrpHandler.UpdateMapping)
	api.DELETE("/frp/mappings/:id", c.FrpHandler.DeleteMapping)
	api.POST("/frp/configs/preview", c.FrpHandler.PreviewConfig)
	api.POST("/frp/configs/save", c.FrpHandler.SaveConfig)
	api.GET("/frp/devices/:id/configs", c.FrpHandler.ListDeviceConfigs)
	api.POST("/frp/configs/:id/rollback", c.FrpHandler.RollbackConfig)
	api.POST("/frp/devices/:id/deploy-config", c.FrpHandler.DeployConfig)
	api.POST("/frp/devices/:id/reload", c.FrpHandler.ReloadDevice)
	api.POST("/frp/devices/:id/status/report", c.FrpHandler.ReportDeviceStatus)
	api.GET("/frp/devices/:id/status", c.FrpHandler.GetDeviceStatus)
	api.GET("/frp/devices/:id/commands/next", c.FrpHandler.PullNextCommand)
	api.POST("/frp/commands/:command_id/result", c.FrpHandler.ReportCommandResult)
	api.GET("/frp/devices/:id/commands", c.FrpHandler.ListDeviceCommands)
	api.POST("/live-photos/bind", c.LivePhotoHandler.Bind)
	api.POST("/live-photos/auto-bind", c.LivePhotoHandler.AutoBind)
	api.DELETE("/live-photos/:id", c.LivePhotoHandler.Unbind)
	api.GET("/live-photos/:id", c.LivePhotoHandler.Detail)
	api.GET("/live-photos", c.LivePhotoHandler.List)
	api.GET("/files/:id/live-photo", c.LivePhotoHandler.GetByFileID)
	api.GET("/ai/index", c.AIHandler.ListIndex)
	api.GET("/ai/index/:id", c.AIHandler.IndexStatus)
	api.POST("/ai/index/rebuild", c.AIHandler.RebuildIndex)
	api.POST("/ai/ocr", c.AIHandler.OCR)
	api.GET("/ai/search", c.AIHandler.Search)
	api.POST("/ai/search/nl", c.AIHandler.NLSearch)
	api.POST("/ai/summarize", c.AIHandler.Summarize)
	api.POST("/ai/assistant/ask", c.AIHandler.AskAssistant)
	api.POST("/ai/assistant/settings", c.AIHandler.ApplySetting)

	admin := api.Group("/admin")
	admin.Use(middleware.RequireAdmin())
	admin.POST("/users", c.AdminHandler.CreateUser)
	admin.PUT("/users/:id/permissions", c.AdminHandler.SetUserPermissions)

	return r
}
