# G06 公开分享

创建请求 `POST /api/files/:id/share` 接受可选的 `expire_hours`、`passcode` 和 `max_downloads`。提取码允许 6–72 个可见 ASCII 字符，服务端只存 bcrypt 哈希。`max_downloads` 接受 0–1,000,000，0 表示不限。省略新增字段沿用原有链接行为。创建和所有者列表提供 `requires_passcode` 与 `max_downloads`，所有者列表还提供 `download_count`。

访问 `GET /api/public/shares/:code` 或其 `/download` 时，通过 HTTPS 的 `X-Share-Passcode` 请求头提交提取码。受保护链接缺少正确提取码时返回 403 和 `share_passcode_required`，响应不含文件详情。带提取码的明文请求返回 426。提取码不从 URL 读取。详情查询不消耗下载次数。

服务端对单文件和目录 ZIP 都在校验内容后、发出 200 或 206 响应前登记一次下载。同一链接最后一次额度由数据库行锁保护。304 和 416 不扣减。分段下载的每个 206 分别计数，发送开始后断线也计数。额度用完返回 410 和 `share_download_limit`，过期保留 410，撤销后返回 404。下载发出前复核分享者当前权限、分享记录和源文件身份。已开始的下载可以继续传输。

公开详情与下载各有按 TCP 来源 IP 的共享 Redis 预算，分别为每分钟 60 和 20 个请求。预算跨链接共享，反向代理下以直连对端 IP 为准。生产环境用 HTTPS 提供受保护链接。

升级先备份并停旧版本，再执行原有显式 `cloudnest migrate`。启动只校验三列，旧链接沿用空哈希及零限额，不需重新分发。启用受保护或限次链接后，不要以旧程序继续提供该数据库上的公开分享接口，旧程序不识别新增限制。
