package main

import (
	"context"
	"fmt"
	"log"
	"net/http"

	"cloudenest_go/internal/config"
	"cloudenest_go/internal/handler"
	"cloudenest_go/internal/middleware"
	"cloudenest_go/internal/service"
	storageMysql "cloudenest_go/internal/storage/mysql"
	"cloudenest_go/internal/store"

	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
)

func main() {
	godotenv.Load()

	cfg := config.Load()

	db, err := store.NewMySQL(cfg)
	if err != nil {
		log.Fatalf("init mysql failed: %v", err)
	}

	if err := store.SeedAdminUser(db); err != nil {
		log.Fatalf("seed admin failed: %v", err)
	}

	rdb, err := store.NewRedis(context.Background(), cfg)
	if err != nil {
		log.Fatalf("init redis failed: %v", err)
	}

	authHandler := &handler.AuthHandler{DB: db, RDB: rdb, Cfg: cfg}
	adminHandler := &handler.AdminHandler{DB: db, RDB: rdb}
	fileHandler := &handler.FileHandler{DB: db, RDB: rdb, Cfg: cfg}
	settingsHandler := &handler.SettingsHandler{DB: db}
	collabHandler := &handler.CollaborationHandler{DB: db}
	aiHandler := &handler.AIHandler{DB: db, Cfg: cfg}
	frpHandler := &handler.FrpHandler{DB: db, RDB: rdb}
	livePhotoHandler := &handler.LivePhotoHandler{DB: db}
	permissionRepo := storageMysql.NewPermissionRepo(db)
	authzService := service.NewAuthzService(permissionRepo)

	r := gin.Default()

	r.POST("/api/auth/login", authHandler.Login)
	r.GET("/api/public/shares/:code", fileHandler.PublicShareInfo)
	r.GET("/api/public/shares/:code/download", fileHandler.PublicShareDownload)

	skipPaths := map[string]bool{
		"/api/auth/login": true,
	}

	api := r.Group("/api")
	api.Use(middleware.Authenticate(rdb, cfg.JWTSecret, func(ctx context.Context, userID int64) ([]string, error) {
		return authzService.ResolveUserPermissions(ctx, userID)
	}, skipPaths))

	api.POST("/auth/logout", authHandler.Logout)
	api.GET("/auth/me", authHandler.Me)

	api.GET("/resource/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"message": "protected resource ok"})
	})

	api.GET("/files", fileHandler.List)
	api.POST("/files/folders", fileHandler.CreateFolder)
	api.PUT("/files/:id/rename", fileHandler.Rename)
	api.DELETE("/files/:id", fileHandler.Delete)
	api.POST("/files/upload/init", fileHandler.InitUpload)
	api.POST("/files/upload/chunk", fileHandler.UploadChunk)
	api.POST("/files/upload/complete", fileHandler.CompleteUpload)
	api.GET("/files/upload/progress", fileHandler.UploadProgress)
	api.GET("/files/search", fileHandler.SearchFiles)
	api.GET("/files/:id/download", fileHandler.Download)
	api.GET("/files/:id/download-zip", fileHandler.DownloadFolderZip)
	api.POST("/files/batch-download", fileHandler.BatchDownload)
	api.POST("/files/batch-delete", fileHandler.BatchDelete)
	api.GET("/files/:id/stream", fileHandler.StreamVideo)
	api.GET("/files/:id/thumbnail", fileHandler.Thumbnail)
	api.POST("/files/move", fileHandler.Move)
	api.POST("/files/copy", fileHandler.Copy)
	api.GET("/files/recycle-bin", fileHandler.RecycleBin)
	api.POST("/files/:id/restore", fileHandler.Restore)
	api.DELETE("/files/:id/permanent", fileHandler.PermanentDelete)
	api.GET("/files/:id/preview", fileHandler.Preview)
	api.POST("/files/:id/share", fileHandler.CreateShare)
	api.POST("/files/:id/collaborators", collabHandler.GrantFilePermission)
	api.GET("/files/:id/collaborators", collabHandler.ListFilePermissions)
	api.DELETE("/files/:id/collaborators/:user_id", collabHandler.RevokeFilePermission)
	api.POST("/files/:id/versions", collabHandler.CreateFileVersion)
	api.GET("/files/:id/versions", collabHandler.ListFileVersions)
	api.POST("/workspaces", collabHandler.CreateWorkspace)
	api.GET("/workspaces", collabHandler.ListWorkspaces)
	api.POST("/workspaces/:id/members", collabHandler.AddWorkspaceMember)
	api.GET("/workspaces/:id/members", collabHandler.ListWorkspaceMembers)
	api.PUT("/workspaces/:id/members/:user_id", collabHandler.UpdateWorkspaceMemberRole)
	api.DELETE("/workspaces/:id/members/:user_id", collabHandler.RemoveWorkspaceMember)
	api.GET("/shares", fileHandler.ListShares)
	api.DELETE("/shares/:code", fileHandler.RevokeShare)
	api.GET("/operation-logs", fileHandler.ListOperationLogs)
	api.GET("/operation-logs/export", fileHandler.ExportOperationLogs)
	api.GET("/settings", settingsHandler.GetSettings)
	api.PUT("/settings", settingsHandler.UpsertSettings)
	api.GET("/settings/permissions", settingsHandler.GetPermissionSummary)
	api.GET("/settings/rate-window", settingsHandler.GetMyRateWindow)
	api.PUT("/settings/rate-window", settingsHandler.UpdateMyRateWindow)
	api.POST("/frp/devices", frpHandler.CreateDevice)
	api.GET("/frp/devices", frpHandler.ListDevices)
	api.GET("/frp/devices/:id", frpHandler.GetDevice)
	api.PUT("/frp/devices/:id", frpHandler.UpdateDevice)
	api.DELETE("/frp/devices/:id", frpHandler.DeleteDevice)
	api.POST("/frp/mappings", frpHandler.CreateMapping)
	api.GET("/frp/devices/:id/mappings", frpHandler.ListDeviceMappings)
	api.PUT("/frp/mappings/:id", frpHandler.UpdateMapping)
	api.DELETE("/frp/mappings/:id", frpHandler.DeleteMapping)
	api.POST("/frp/configs/preview", frpHandler.PreviewConfig)
	api.POST("/frp/configs/save", frpHandler.SaveConfig)
	api.GET("/frp/devices/:id/configs", frpHandler.ListDeviceConfigs)
	api.POST("/frp/configs/:id/rollback", frpHandler.RollbackConfig)
	api.POST("/frp/devices/:id/deploy-config", frpHandler.DeployConfig)
	api.POST("/frp/devices/:id/reload", frpHandler.ReloadDevice)
	api.POST("/frp/devices/:id/status/report", frpHandler.ReportDeviceStatus)
	api.GET("/frp/devices/:id/status", frpHandler.GetDeviceStatus)
	api.GET("/frp/devices/:id/commands/next", frpHandler.PullNextCommand)
	api.POST("/frp/commands/:command_id/result", frpHandler.ReportCommandResult)
	api.GET("/frp/devices/:id/commands", frpHandler.ListDeviceCommands)
	api.POST("/live-photos/bind", livePhotoHandler.Bind)
	api.POST("/live-photos/auto-bind", livePhotoHandler.AutoBind)
	api.DELETE("/live-photos/:id", livePhotoHandler.Unbind)
	api.GET("/live-photos/:id", livePhotoHandler.Detail)
	api.GET("/live-photos", livePhotoHandler.List)
	api.GET("/files/:id/live-photo", livePhotoHandler.GetByFileID)
	api.POST("/ai/ocr", aiHandler.OCR)
	api.GET("/ai/search", aiHandler.Search)
	api.POST("/ai/search/nl", aiHandler.NLSearch)
	api.POST("/ai/summarize", aiHandler.Summarize)
	api.POST("/ai/assistant/ask", aiHandler.AskAssistant)
	api.POST("/ai/assistant/settings", aiHandler.ApplySetting)

	admin := api.Group("/admin")
	admin.Use(middleware.RequireAdmin())
	admin.POST("/users", adminHandler.CreateUser)
	admin.PUT("/users/:id/permissions", adminHandler.SetUserPermissions)

	addr := fmt.Sprintf(":%s", cfg.AppPort)
	log.Printf("server running on %s", addr)
	if err := r.Run(addr); err != nil {
		log.Fatalf("run server failed: %v", err)
	}
}
