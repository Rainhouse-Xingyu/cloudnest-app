# G06 Live Photo 后端

现有绑定、解绑、详情和自动绑定接口保持原有路径。绑定和解绑都要求操作者对图片与视频有写权限。详情和列表要求对两份文件有读权限。手动绑定、批量自动绑定和上传后自动配对在写入事务中重新检查两份源文件。文件锁与权限锁保持到绑定和审计一起提交，软删除源文件时清除绑定。

自动配对支持 JPG、JPEG、HEIC、HEIF 和 MOV，按同目录和同名主干识别。个人根目录按所有者隔离。该规则依据扩展名与名称，实际媒体内容识别和 macOS 播放在原生客户端阶段验收。

显式运行 `cloudnest migrate` 安装 `uq_live_photo_image_v1` 与 `uq_live_photo_video_v1`，普通启动只校验。升级前应备份并停止旧实例。若历史数据把同一文件用于多条绑定或同时用于两种角色，迁移会拒绝并保留历史记录。人工核对冲突可使用以下只读查询。

```sql
SELECT file_id, COUNT(*) AS references_count
FROM (
  SELECT image_file_id AS file_id FROM live_photo
  UNION ALL
  SELECT video_file_id FROM live_photo
) AS sources
GROUP BY file_id
HAVING COUNT(*) > 1;
```

数据库分别保证图片列和视频列唯一。应用在同一事务内锁定两份文件并检查两列，避免改名后把同一文件再次用于另一种角色。维护工具若直接写表，需要遵守相同的两列约束。
