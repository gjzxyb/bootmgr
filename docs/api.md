# API 草案

后端 API 统一前缀为 `/api/v1`，除登录接口外均需要 `Authorization: Bearer <token>`。

启动和元数据接口不使用 `/api/v1` 前缀，供 iPXE、安装环境和 cloud-init 风格客户端访问。

公共镜像文件接口同样不使用 `/api/v1` 前缀，用于启动和安装阶段下载镜像。

## Health

- `GET /healthz`: 返回服务健康状态。会检查数据库连接；当 `REDIS_ADDR` 已配置时执行 Redis `PING`。Redis 未配置时返回 `redis: "disabled"`，便于本地 SQLite 开发。
- `GET /readyz`: 返回运行自检状态，不需要鉴权。会检查数据库、Redis、镜像存储目录和配置校验结果，响应包含 `status`、`checks` 和 `config_issues`。接口始终返回 JSON，适合部署前检查和系统管理页展示。

`/readyz` 响应示例：

```json
{
  "status": "ok",
  "checks": [
    { "name": "database", "status": "ok", "message": "database ping succeeded" },
    { "name": "redis", "status": "ok", "message": "redis ping succeeded" },
    { "name": "image_storage", "status": "ok", "message": "image storage is writable" },
    { "name": "config", "status": "ok", "message": "configuration passed validation" }
  ],
  "config_issues": []
}
```

## Request Tracing

- 客户端可以传入 `X-Request-ID`，后端会校验并原样写回响应头；未传入时后端自动生成。
- 前端 API client 会为每个请求自动设置 `X-Request-ID`。
- 后端访问日志为 JSON 行格式，包含 `request_id`、方法、路径、状态码、耗时、客户端 IP、User-Agent 和登录用户邮箱。
- 审计日志中的 `request_id` 与访问日志一致，可用于从审计事件反查单次 HTTP 请求。

## RBAC 与危险操作确认

- 管理写操作要求角色为 `admin` 或 `operator`。
- 凭据、BMC 配置、镜像删除等敏感操作要求 `admin`。
- 高风险操作必须携带 `X-Confirm-Action` 请求头，否则返回 `428 Precondition Required`。
- 前端会基于 `/auth/me` 返回的 `role` 隐藏无权限菜单和操作按钮；后端 RBAC 仍是最终权限边界。

角色边界：

- `admin`: 系统管理、租户/网络配置、凭据配置、BMC Endpoint、镜像删除、备份导出和全部日常运维操作
- `operator`: 资产、镜像、模板、部署、脚本、终端、采集和告警规则等日常运维操作
- `viewer`: 资产、镜像、模板、部署日志、告警、审计和运维记录查看

确认头取值：

- `server.retire`
- `server.scrap`
- `server.delete`
- `server.status-terminal`
- `bmc.upsert`
- `bmc.power-on`
- `bmc.power-off`
- `bmc.reboot`
- `bmc.batch-power-on`
- `bmc.batch-power-off`
- `bmc.batch-reboot`
- `image.delete`
- `install_template.delete`
- `workflow_template.delete`
- `deployment.create`
- `deployment.batch-create`
- `deployment.cancel`
- `deployment.retry`
- `ssh.upsert`
- `ops.script.create`
- `ops.logs.collect`
- `ops.terminal.open`
- `ops.terminal.command`
- `ops.terminal.close`
- `ops.backup.export`
- `ops.backup.restore`
- `system.lab-validation.run`

## Boot and Metadata

- `GET /boot/ipxe?mac={mac}&arch={arch}&firmware={firmware}`: 根据 MAC 和启动参数生成 iPXE 脚本；未知 MAC 会自动创建 `discovered` 资产并记录状态历史
- `GET /boot/discovery.ipxe`: 返回硬件发现环境入口脚本
- `GET /boot/linux-installer.ipxe`: 返回 Linux 安装器入口脚本
- `POST /boot/events`: 记录启动事件并返回 `201` 和匹配结果；未知 MAC 会自动创建 `discovered` 资产
- `GET /images/{id}/file`: 下载已启用且已校验的镜像文件，支持 HTTP Range
- `GET /metadata/instance-id`、`/metadata/hostname`、`/metadata/network`、`/metadata/ssh-keys`、`/metadata/userdata`、`/userdata`: 按客户端 IP 匹配服务器，返回 Cloud-init 风格元数据
- `GET /metadata/by-server/{id}/instance-id`: 返回实例 ID
- `GET /metadata/by-server/{id}/hostname`: 返回主机名
- `GET /metadata/by-server/{id}/network`: 返回网络配置 JSON
- `GET /metadata/by-server/{id}/ssh-keys`: 返回 `{ "keys": [] }` SSH 公钥 JSON
- `GET /metadata/by-server/{id}/userdata`: 返回 shell userdata
- `GET /userdata/by-server/{id}`: userdata 兼容别名
- `GET /metadata/by-token/{token}/instance-id`: 通过部署 metadata token 返回实例 ID
- `GET /metadata/by-token/{token}/hostname`: 通过部署 metadata token 返回主机名
- `GET /metadata/by-token/{token}/network`: 通过部署 metadata token 返回网络配置 JSON
- `GET /metadata/by-token/{token}/ssh-keys`: 通过部署 metadata token 返回 SSH 公钥 JSON
- `GET /metadata/by-token/{token}/userdata`: 通过部署 metadata token 返回渲染后的 userdata
- `GET /userdata/by-token/{token}`: token userdata 兼容别名
- `GET /metadata/by-mac/{mac}/{field}`: 按服务器主 MAC 返回 `instance-id`、`hostname`、`network`、`ssh-keys` 或 `userdata`
- `GET /metadata/by-ip/{ip}/{field}`: 按服务器主 IP 返回 `instance-id`、`hostname`、`network`、`ssh-keys` 或 `userdata`
- `GET /metadata/by-deployment/{id}/{field}`: 按部署任务返回 `instance-id`、`hostname`、`network`、`ssh-keys` 或 `userdata`
- `GET /userdata/by-mac/{mac}`、`GET /userdata/by-ip/{ip}`、`GET /userdata/by-deployment/{id}`: userdata 兼容别名

创建部署任务时平台会生成 24 小时有效的 metadata token，过期后再次确保 token 时会自动轮换；`/boot/ipxe` 为有活跃部署的服务器返回 `metadata-url={BOOT_BASE_URL}/metadata/by-token/{token}`。无 ID 的 `/metadata/...` 会按客户端 IP 匹配服务器；`/metadata/by-server/{id}`、`by-mac`、`by-ip` 和 `by-deployment` 保留给发现环境、本地演示和兼容；安装环境应优先使用 token 路径，避免枚举 server ID。启动事件中的 iPXE 脚本会脱敏 metadata token 后再落库。

`METADATA_REQUIRE_DEPLOYMENT_NETWORK` 开启时，所有 `/metadata/...` 和 `/userdata/...` 入口都会要求客户端 IP 位于任一启用的 `deployment` 网络 CIDR 内，否则返回 `403`。生产环境必须开启该限制；开发/测试环境默认关闭以便本地演示。

`/metadata/.../network` 会合并服务器主网卡字段和部署任务绑定的 `network_id` 对应网络；旧部署未绑定网络时回退到最新启用的 `deployment` 网络配置。响应返回 `network_id`、`cidr`、`gateway`、`dns`、`vlan_id`、`dhcp_mode` 和 `proxy_dhcp`，用于安装环境生成网络配置。

`/metadata/.../ssh-keys` 会从部署变量中的 `ssh_authorized_keys`、`ssh_keys` 或 `ssh_public_key` 提取公钥并去重；未配置时返回空数组，不生成或返回任何明文密码。

每次成功访问 Metadata API 都会写入 `LogEvent`，`source=metadata`，`message` 只记录端点、访问模式、客户端 IP 和部署 ID，不记录 URL 原文或 metadata token；被网段限制拒绝的访问会写入 warning 级别日志且同样不记录 token；`trace_id` 使用 `X-Request-ID` 便于关联网关与平台日志。

## Auth

- `POST /auth/login`: 本地账号登录；连续失败超过 `LOGIN_RATE_LIMIT_ATTEMPTS` 后，在 `LOGIN_RATE_LIMIT_WINDOW_SECONDS` 窗口内返回 `429` 和 `Retry-After`；失败和被限流登录会分别记录 `auth.login.failed`、`auth.login.blocked` 审计
- `GET /auth/me`
- `GET /dashboard`: 返回资产、镜像、部署、活跃告警、审计总数，资产状态分布、部署状态分布、告警级别分布，以及最近部署和最近审计记录

## Users

- `GET /users`: 查询用户，管理员可用。无分页参数时返回最近 200 条数组；携带 `page` 或 `page_size` 时返回 `{ items, total, page, page_size }`。支持 `keyword`、`role` 筛选
- `POST /users`: 创建用户，管理员可用
- `PATCH /users/{id}`: 更新用户姓名或角色，管理员可用；如果目标用户是最后一个 `admin`，降级为其他角色会返回 `409`
- `POST /users/{id}/reset-password`: 重置用户密码，管理员可用

用户邮箱会 trim 并转为小写保存；创建用户、更新角色和重置密码时，后端会校验角色必须为 `admin`、`operator`、`viewer`，密码至少 8 位且包含大小写字母和数字；系统始终要求至少保留一个管理员账号。

## System Validation

- `GET /system/lab-validation`: 管理员可用。返回真实验收报告，汇总 PXE/DHCP/TFTP 服务配置和启动事件证据、部署网段数量、BMC 适配器和端点状态、SSH 采集/运维模式和 SSH Access 状态。
- `POST /system/lab-validation/run`: 管理员可用，需 `X-Confirm-Action: system.lab-validation.run`。请求体可包含 `{ "check_pxe": true, "check_bmc": true, "check_ssh": true, "limit": 20 }`，默认检查 PXE、BMC 和 SSH，`limit` 最大 50。接口会只读请求 `BOOT_BASE_URL/boot/ipxe`，在 `BOOT_SERVICES_ENABLED=true` 且模式不是 `external` 时向当前监听地址发送合成 PXE DHCPDISCOVER 校验 bootfile 响应，并在 `BOOT_SERVICES_ENABLED=true` 时通过 UDP TFTP 拉取 `boot.ipxe`；同时对已配置 BMC 端点执行当前真实适配器的连通性检查，对已配置 SSH Access 执行轻量只读 SSH 检查，并返回更新后的验收报告；不会执行开机/关机/重启，也不会动态启用 DHCP/TFTP 监听。

验收报告的 `status` 为 `ok`、`warning` 或 `error`；`checks` 包含 `pxe_services`、`deployment_network`、`pxe_boot_events`、`pxe_http`、`pxe_dhcp`、`pxe_tftp`、`bmc_adapter`、`bmc_connectivity`、`ssh_modes` 和 `ssh_connectivity` 等检查项。`bmc.recent_endpoints` 与 `ssh.recent_ssh_accesses` 不返回凭据引用或明文 secret。

## Tenants

- `GET /tenants`: 查询租户，管理员可用。无分页参数时返回最近 200 条数组；携带 `page` 或 `page_size` 时返回 `{ items, total, page, page_size }`。支持 `keyword`、`status` 筛选
- `POST /tenants`: 创建租户，管理员可用。`tenant_id` 会 trim 后校验为字母/数字/点/下划线/短横线组成的标识符；`name` 必填；`status` 仅允许 `active`、`disabled`；`quota` 如提交必须是 JSON object，数值不能为负，`quota.servers` 必须是非负整数。
- `PATCH /tenants/{id}`: 更新租户名称、状态、负责人、描述或配额，管理员可用。`tenant_id`、`created_at`、`updated_at` 由后端维护，客户端提交会被忽略，更新后执行同样校验；`quota.servers` 不能低于该租户当前资产数。

MVP 阶段租户用于资产归属和筛选，并对 `quota.servers` 执行资产数配额；更细粒度的跨租户访问控制和计费仍属于后续生产接入范围。

## Network Configs

- `GET /network-configs`: 查询网络配置。无分页参数时返回数组；携带 `page` 或 `page_size` 时返回 `{ items, total, page, page_size }`。支持 `keyword`、`purpose`、`status` 筛选
- `POST /network-configs`: 创建网络配置，管理员可用；启用状态下同一 `purpose` 的 CIDR 不允许互相重叠
- `POST /network-configs/{id}/check`: 管理员可用；对单条网络配置执行只读检查，返回 `{ status, checks }`，覆盖 CIDR 格式、启用网段重叠、网关、DNS、状态、DHCP/ProxyDHCP 模式和部署网可用性
- `PATCH /network-configs/{id}`: 更新网络配置，管理员可用；启用状态下同一 `purpose` 的 CIDR 不允许互相重叠

网络用途建议使用 `management`、`deployment`、`business`。创建和更新时后端会校验 CIDR 格式、网关必须位于 CIDR 内、DNS 必须为 IP 列表、VLAN 必须在 0-4094 范围内，`dhcp_mode` 仅允许 `proxy`、`builtin`、`external`。默认只记录 CIDR、网关、DNS、VLAN、DHCP 模式和 ProxyDHCP 开关；真实 DHCP/TFTP 监听必须通过启动环境变量显式启用。网络检查接口只做平台侧静态检查和运行边界提示，不会探测或修改真实 DHCP/TFTP。

真实 PXE/DHCP/TFTP 服务由环境变量显式启用，默认关闭。设置 `BOOT_SERVICES_ENABLED=true` 后，后端会启动只读 TFTP 服务，并在 `BOOT_SERVICE_MODE=proxy` 或 `builtin` 时启动 DHCP/ProxyDHCP UDP 监听；`external` 模式只启动 TFTP，供外部 DHCP 指向平台。启用时必须配置 `BOOT_BIND_INTERFACE`、`BOOT_DHCP_SERVER_IP`、`BOOT_TFTP_ROOT`、`BOOT_TFTP_BOOTFILE_UEFI`、`BOOT_TFTP_BOOTFILE_BIOS`；`builtin` 模式还必须配置 `BOOT_DHCP_LEASE_START` 和 `BOOT_DHCP_LEASE_END`。TFTP 会服务 `BOOT_TFTP_ROOT` 内文件，并提供动态 `boot.ipxe`/`auto.ipxe`/`default.ipxe`，链到 `{BOOT_BASE_URL}/boot/ipxe?mac=${net0/mac}`。`/readyz` 会增加 `pxe_services` 检查，报告 TFTP 根目录可写性和启动加载器文件是否存在。生产或实验室启用前必须绑定隔离部署网/VLAN，避免影响现网 DHCP。

## Servers

- `GET /servers`: 查询资产。无分页参数时返回数组；携带 `page` 或 `page_size` 时返回 `{ items, total, page, page_size }`。支持 `keyword`、`status`、`owner`、`tenant_id` 筛选
- `POST /servers`
- `POST /servers/import`: 批量导入资产，payload 为 `{ "servers": [ ...Server ] }`，单次最多 500 台
- `GET /servers/{id}`
- `PATCH /servers/{id}`: 更新资产基础字段；状态改为 `retired` 或 `scrapped` 时必须携带 `X-Confirm-Action: server.status-terminal`
- `DELETE /servers/{id}`: 删除无业务引用的资产，需管理员角色和 `X-Confirm-Action: server.delete`；已有部署、BMC/SSH 配置、硬件盘点、监控、日志、脚本执行、终端会话、告警、退役/报废记录等引用时返回 `409` 和 blockers，应改为退役/报废或先清理相关配置
- `POST /servers/{id}/retire`: 退役资产，需 `X-Confirm-Action: server.retire`；请求体可包含 `reason`、`erase_status`、`erase_method`、`evidence`，`erase_status` 支持 `not_required`、`pending`、`verified`、`failed`，`verified` 必须提供擦除方式和证据；重复退役是幂等操作，不会重复写状态历史或退役记录，`scrapped` 资产会返回 `409`
- `POST /servers/{id}/scrap`: 报废资产，需 `X-Confirm-Action: server.scrap`；请求体与退役接口一致，会写入 `to_status=scrapped` 的终态记录；重复报废是幂等操作，不会重复写状态历史或终态记录
- `GET /servers/{id}/inventory`
- `GET /servers/{id}/status-history`: 查询资产生命周期状态变更历史
- `GET /servers/{id}/retirement-records`: 查询资产退役/报废原因、擦除状态、擦除方式、证据和操作人记录
- `POST /servers/{id}/inventory`: 写入一条硬件盘点快照，支持 CPU、内存、磁盘、网卡、GPU、RAID/HBA 摘要和 `raw_payload`；`raw_payload` 如提交必须是 JSON object
- `GET /servers/{id}/bom`: 返回该资产最新硬件盘点快照与 CMDB 字段合并后的 BOM JSON
- `GET /servers/{id}/bom.csv`: 下载该资产 BOM CSV
- `GET /bom.csv`: 下载全量资产 BOM CSV

资产创建、更新和批量导入会校验 `status` 只能是 `discovered`、`ready`、`deploying`、`running`、`maintenance`、`retired`、`scrapped`，`architecture` 只能是 `x86_64` 或 `arm64`，`primary_ip` 必须是合法 IP，`primary_mac` 必须是合法 MAC 并会统一保存为小写冒号格式。`asset_no`、`hostname`、`primary_mac` 任一非空即可创建资产，这些非空身份字段会在应用层和数据库唯一索引层做大小写不敏感唯一约束。

资产 `tenant_id` 可以为空；如果创建、迁入或批量导入时传入非空 `tenant_id`，后端会要求对应租户存在、状态为 `active` 且未超过 `quota.servers`，否则返回 `400`。`tags` 支持 JSON 数组或对象，用于 CMDB 标签；资产关键字查询会匹配标签内容。

资产生命周期状态建议使用 `discovered`、`ready`、`deploying`、`running`、`maintenance`、`retired`、`scrapped`。退役和报废接口会写入 `RetirementRecord`、状态历史、高风险审计和 `lifecycle` 日志事件，用于沉淀终态原因、数据擦除状态、方式和证据；MVP 不在开发机执行真实磁盘擦除，真实擦除工作流将在后续接入。资产删除使用软删除，只允许清理无业务引用的误建资产，并会删除该资产的状态历史以避免备份中出现孤儿状态记录；已有业务记录的资产应退役或报废。MVP 允许手动改为 `scrapped`；`scrapped` 视为比 `retired` 更终态，不允许通过退役接口改回。

## Images

- `GET /images`: 查询镜像。无分页参数时返回数组；携带 `page` 或 `page_size` 时返回 `{ items, total, page, page_size }`。支持 `keyword`、`os_family`、`architecture`、`status`、`test_status` 筛选
- `POST /images`
- `POST /images/upload`: 上传镜像文件并登记镜像，multipart 字段为 `file`、`name`、`os_family`、`os_version`、`architecture`、`status`
- `PATCH /images/{id}`
- `DELETE /images/{id}`: 删除未被部署任务引用的镜像，需管理员角色和 `X-Confirm-Action: image.delete`；如果镜像已有部署引用则返回 `409`，应改为禁用
- `POST /images/{id}/verify`

手工 `POST /images` 登记本地路径时要求 `name` 和 `file_path`，且 `file_path` 必须位于 `IMAGE_STORAGE_DIR` 内；相对路径会按 `IMAGE_STORAGE_DIR` 解析，目录外路径会返回 `400`。`status` 仅允许 `enabled`、`disabled`，`architecture` 仅允许 `x86_64`、`arm64`。后端会忽略客户端提交的 `test_status`、`sha256` 和 `size_bytes`，统一从 `untested` 开始；`PATCH /images/{id}` 同样不能直接写入这些校验字段，修改 `file_path` 会自动重置为 `untested`。

`POST /images/upload` 会校验同样的 `status` 和 `architecture`，把文件保存到 `IMAGE_STORAGE_DIR`，流式计算 SHA256 和文件大小，并将 `test_status` 设置为 `tested_passed`。默认单文件大小限制由 `IMAGE_UPLOAD_MAX_MB` 控制。公共 `/images/{id}/file` 只服务 `IMAGE_STORAGE_DIR` 内且已启用、已校验的镜像文件。

`POST /images/{id}/verify` 会读取 `file_path` 指向的本地文件，计算 SHA256 和文件大小，并把 `test_status` 更新为 `tested_passed`。文件不存在、读取失败、符号链接解析后位于 `IMAGE_STORAGE_DIR` 外时会标记为 `test_failed`。

镜像删除使用软删除并保护部署引用。已被任意部署任务引用的镜像不能删除，避免历史部署、部署日志和启动脚本引用失效；需要下线时应把 `status` 改为 `disabled`。

## Templates

- `GET /install-templates`: 查询安装模板。无分页参数时返回数组；携带 `page` 或 `page_size` 时返回 `{ items, total, page, page_size }`。支持 `keyword`、`os_family`、`template_type`、`version`、`status` 筛选
- `POST /install-templates`: 创建安装模板。`name`、`template_type`、非空 `content` 必填；`template_type` 仅允许 `cloud-init`、`autoinstall`、`kickstart`、`preseed`、`unattend`；`status` 仅允许 `enabled`、`disabled`；`variables_schema` 如提交必须是 JSON object。
- `PATCH /install-templates/{id}`: 更新安装模板。会基于当前记录合并字段后执行同样校验，`id`、`created_by`、`created_at`、`updated_at` 由后端维护，客户端提交会被忽略。
- `DELETE /install-templates/{id}`: 删除未被部署任务引用的安装模板，需管理员角色和 `X-Confirm-Action: install_template.delete`；已有部署引用时返回 `409`，应改为禁用。
- `GET /workflow-templates`: 查询工作流模板。无分页参数时返回数组；携带 `page` 或 `page_size` 时返回 `{ items, total, page, page_size }`。支持 `keyword`、`version`、`status` 筛选
- `POST /workflow-templates`: 创建工作流模板。`name` 必填，`definition` 必须是 JSON object，且 `definition.steps` 至少包含一个对象步骤，每个步骤必须有非空 `name` 和 `action`；`status` 仅允许 `enabled`、`disabled`。
- `PATCH /workflow-templates/{id}`: 更新工作流模板。会基于当前记录合并字段后执行同样校验，系统字段由后端维护。
- `DELETE /workflow-templates/{id}`: 删除未被部署任务引用的工作流模板，需管理员角色和 `X-Confirm-Action: workflow_template.delete`；已有部署引用时返回 `409`，应改为禁用。

模板内容支持简单变量替换，例如 `{{hostname}}`、`{{primary_ip}}`、`{{primary_mac}}`、`{{metadata_token}}`。部署请求中的 `variables` 会覆盖同名默认变量。

Demo seeder 默认创建三类启用的 Linux 安装模板：Ubuntu Server 24.04 Autoinstall、Rocky Linux 9 Kickstart 和 Debian 12 Preseed，便于演示不同发行版的自动化安装入口。

模板创建、更新和删除会写入审计日志，前端“模板管理”页面可维护安装模板和工作流模板的启停状态；删除属于高风险操作，只允许管理员执行。

## Deployments

- `GET /deployments`: 查询部署任务。无分页参数时返回数组；携带 `page` 或 `page_size` 时返回 `{ items, total, page, page_size }`。支持 `status`、`server_id`、`image_id`、`network_id`、`requested_by` 筛选
- `POST /deployments`: 创建部署，需 `X-Confirm-Action: deployment.create`，请求体必须包含 `erase_confirmed: true`，可传 `network_id` 绑定启用的部署网络
- `POST /deployments/batch`: 批量创建部署，需 `X-Confirm-Action: deployment.batch-create`，请求体使用 `server_ids`，可传同一个 `network_id` 应用于整批部署，单批最多 20 台，全部目标 preflight 通过后才会创建部署
- `GET /deployments/{id}`
- `POST /deployments/{id}/cancel`
- `POST /deployments/{id}/retry`
- `GET /deployments/{id}/logs`: 返回部署日志包，包含 `summary`、最新 `workflow`、所有 `runs` 尝试记录和最新 run 的 `tasks`。`runs` 和 `tasks` 都包含 `duration_ms`，用于展示步骤耗时和重试历史。

创建部署前会校验 `server_id`/`server_ids`、`image_id` 必须非 0，`template_id`、`workflow_id`、`network_id` 如提交也必须大于 0，`variables` 如提交必须是 JSON object；`erase_policy` 支持 `none`、`quick`、`full`、`external_verified`，默认 `quick`，且必须提交 `erase_confirmed: true` 才会创建部署记录。服务器不存在返回 `404`；服务器必须处于 `ready`、`running` 或 `maintenance`，且不能已有 `pending` 或 `running` 部署，否则返回 `409`。随后执行 preflight 检查：必须已配置 BMC Endpoint，且会调用当前 `BMC_ADAPTER` 做连通性检查；平台必须存在启用的 `deployment` 网络配置；如提交 `network_id`，该网络必须存在且为启用的 `deployment` 网络；镜像必须 `enabled` 且 `tested_passed`，安装模板和工作流模板必须存在且启用。批量创建会先对全部目标执行 preflight 和重复校验，任一目标失败时不会创建任何部署。BMC 连通性失败会返回 `400`，`problems` 中包含 `bmc connectivity check failed: ...`，不会创建部署任务。MVP 的 BMC Endpoint 可使用模拟适配器；真实环境接入 Redfish/IPMI 前必须保存可用凭据并确保 API 主机可访问 BMC 管理网。

部署工作流受 `DEPLOYMENT_CONCURRENCY` 控制，默认最多 20 个任务同时执行；超出并发的部署会保持 `pending`，拿到执行槽后才会创建 workflow run 并进入 `running`。

取消部署会把 `pending` 或 `running` 的部署标记为 `cancelled`，模拟工作流会停止继续写入成功状态；如果服务器仍处于 `deploying`，会恢复为 `ready` 并记录状态历史。

失败或已取消的部署可通过 `POST /deployments/{id}/retry` 重新入队，需 `X-Confirm-Action: deployment.retry`。重试会复用原部署的服务器、镜像、模板、工作流、变量和擦除策略确认记录，重新执行部署前置检查，清理旧失败原因并创建新的 workflow run；`GET /deployments/{id}/logs` 的 `tasks` 返回最新一次 workflow run 的任务日志，`runs` 保留此前失败或取消的尝试历史，`summary` 给出最新 run 的任务统计和耗时。MVP 模拟器支持在工作流模板 step 中使用 `action: "simulate_failure"` 演示失败、失败原因和重试流程。

## BMC

- `POST /servers/{id}/bmc`
- `GET /servers/{id}/bmc/power`
- `GET /servers/{id}/bmc/firmware`: 查询 BMC/BIOS 固件信息，返回 `adapter`、`endpoint_status`、厂商、型号、序列号、BIOS 版本、BMC 版本和最近检查时间；该接口只读，不要求二次确认
- `POST /servers/{id}/bmc/power-on`: 开机，需 `X-Confirm-Action: bmc.power-on`
- `POST /servers/{id}/bmc/power-off`: 关机，需 `X-Confirm-Action: bmc.power-off`
- `POST /servers/{id}/bmc/reboot`: 重启，需 `X-Confirm-Action: bmc.reboot`
- `POST /servers/{id}/bmc/check`
- `POST /servers/bmc/batch-power`: 批量开机、关机或重启，payload 为 `{ "action": "power-on|power-off|reboot", "server_ids": [1,2] }`；确认头分别为 `bmc.batch-power-on`、`bmc.batch-power-off`、`bmc.batch-reboot`

BMC 和 SSH 配置响应不会返回明文密码，也不会返回内部凭据引用字段；凭据只保存在后端 `credentials` 表中。更新 BMC/SSH 配置时如果未提供新密码/secret，会保留已有凭据引用，客户端提交的内部凭据引用字段会被忽略。

`BmcEndpoint.type` 支持 `redfish` 和 `ipmi`。配置 BMC 时必须先存在对应服务器；保存或更新后状态为 `unknown`，连通性检查成功后变为 `ok`，失败后变为 `error`；`retired` 或 `scrapped` 资产不能再配置 BMC、执行连通性检查或执行开机/关机/重启，接口返回 `409`，但电源状态和固件信息只读查询仍可用于排障查看；`redfish` 端点必须是 `http` 或 `https` URL，`ipmi` 端点必须是合法主机名/IP 或 host:port，`ipmi` 类型只允许 `protocol=ipmi`。MVP 默认 `BMC_ADAPTER=simulated` 会返回固定的演示固件信息；真实 Redfish 适配器使用 HTTP Basic Auth 调用 Redfish `/redfish/v1`、电源状态、`Systems/1`、`Managers/1` 和 Reset API，真实 IPMI 适配器使用系统 `ipmitool` 调用 `chassis status`、`power status/on/off/reset` 和 `mc info`。连通性检查失败会把 BMC Endpoint 标记为 `error` 并返回 `502`。

批量电源接口单次最多 50 台，拒绝重复或 0 ID。每个目标独立返回 `success` 或 `failed`，不会因为单台缺少 BMC Endpoint、资产不存在或处于 `retired`/`scrapped` 就中断整批；每个目标都会写入高风险审计记录。前端资产表支持多选后批量开机、关机和重启，终态资产不可勾选。

## Monitoring

- `GET /servers/{id}/metrics`
- `POST /servers/{id}/ssh`: 保存 SSH Agentless 采集配置和加密凭据；目标资产不能是 `retired` 或 `scrapped`
- `GET /servers/{id}/ssh`: 查询 SSH Agentless 采集配置
- `POST /servers/{id}/ssh/check`: 使用保存的 SSH 配置连接真实主机并执行轻量只读检查，成功后把 SSHAccess 状态置为 `ok`，失败置为 `error`
- `POST /servers/{id}/collections`: 启动一次 SSH Agentless 采集任务；目标资产不能是 `retired` 或 `scrapped`
- `GET /servers/{id}/collections`: 查询采集任务列表
- `GET /collections`: 查询全局采集任务。无分页参数时返回最近 100 条数组；携带 `page` 或 `page_size` 时返回 `{ items, total, page, page_size }`。支持 `server_id`、`status`、`mode`、`requested_by` 筛选
- `GET /log-events`: 查询日志事件。无分页参数时返回最近 200 条数组；携带 `page` 或 `page_size` 时返回 `{ items, total, page, page_size }`。支持 `keyword`、`server_id`、`source`、`level` 筛选
- `GET /alerts`: 查询告警。无分页参数时返回数组；携带 `page` 或 `page_size` 时返回 `{ items, total, page, page_size }`。支持 `keyword`、`server_id`、`severity`、`status`、`rule_id` 筛选
- `GET /alert-rules`: 查询告警规则。无分页参数时返回最近 200 条数组；携带 `page` 或 `page_size` 时返回 `{ items, total, page, page_size }`。支持 `keyword`、`metric_name`、`severity`、`status` 筛选
- `POST /alert-rules`: 创建告警规则
- `PATCH /alert-rules/{id}`: 更新告警规则
- `POST /alert-rules/evaluate`: 按最新指标评估启用规则，生成新的 firing 告警，返回 `{ "created": 1, "deduplicated": 0, "alerts": [...] }`
- `POST /alerts/{id}/ack`: 确认告警，支持 `{ "note": "处理说明" }`
- `POST /alerts/{id}/resolve`: 关闭告警，支持 `{ "note": "处理说明" }`
- `GET /alerts/{id}/events`: 查询告警处理记录

SSH 配置、单机采集和单机指标查询都会校验对应服务器存在；SSH 配置和单机采集会拒绝 `retired`/`scrapped` 资产；SSH host 必须是合法主机名或 IP，端口必须为 1-65535，`auth_type` 支持 `password` 或 `private_key`。`COLLECTOR_MODE=ssh` 使用 Go SSH 执行器连接真实主机，支持保存的 password 或 private_key 凭据，执行只读指标采集命令，单次采集默认 30 秒超时。指标查询只返回 7 天保留窗口内的样本，成功采集后会清理更早的历史指标。

告警规则写入时会校验 `rule_id`、`metric_name` 仅使用字母、数字、`_`、`.`、`-`，`operator` 仅允许 `>`、`>=`、`<`、`<=`、`==`，`severity` 仅允许 `critical`、`warning`、`info`，`status` 仅允许 `enabled`、`disabled`。

MVP 默认启用 `cpu.high`、`memory.high`、`disk.full`、`disk.smart.warning`、`host.offline` 五类基础规则。采集样本包含 `host_up`、`cpu_usage`、`memory_usage`、`disk_usage`、`disk_smart_health`、`network_rx_mbps`、`network_tx_mbps`、`process_count`、`process_zombie_count`，其中 `disk_smart_health=0` 表示健康，`1` 表示异常，网络指标和进程指标用于展示和后续规则扩展。告警评估只读取 7 天保留窗口内每台服务器每个指标的最新样本；同一服务器同一规则已有 `firing` 或 `acknowledged` 告警时不会重复创建，只刷新级别、标题和最新指标说明并计入 `deduplicated`；已 `resolved` 告警保留历史，指标再次命中会创建新的 `firing` 告警。新告警会写入 `trigger` 处理记录，确认和关闭会写入 `ack`、`resolve` 处理记录；确认和关闭的 `note` 最长 1000 字符，已确认告警不能重复确认，已恢复告警不能再次确认或关闭。

## Operations

- `GET /ops/script-jobs`: 查询批量脚本任务。无分页参数时返回最近 100 条数组；携带 `page` 或 `page_size` 时返回 `{ items, total, page, page_size }`。支持 `keyword`、`status`、`requested_by` 筛选
- `POST /ops/script-jobs`: 创建批量脚本任务，需 `X-Confirm-Action: ops.script.create`
- `GET /ops/script-jobs/{id}`: 查询脚本任务详情
- `GET /ops/script-jobs/{id}/results`: 查询每台服务器的脚本执行结果
- `POST /ops/log-collections`: 一键采集日志，需 `X-Confirm-Action: ops.logs.collect`，payload 为 `{ "server_ids": [1,2], "sources": ["syslog","dmesg","hardware"] }`
- `GET /ops/terminal-sessions`: 查询远程终端会话。无分页参数时返回最近 100 条数组；携带 `page` 或 `page_size` 时返回 `{ items, total, page, page_size }`。支持 `server_id`、`status`、`mode`、`requested_by` 筛选。返回前会按 `TERMINAL_SESSION_TTL_MINUTES` 自动关闭过期 active 会话。
- `POST /ops/terminal-sessions`: 打开终端会话，需 `X-Confirm-Action: ops.terminal.open`
- `GET /ops/terminal-sessions/{id}`: 查询终端会话详情和 transcript，读取前同样会执行过期会话回收
- `POST /ops/terminal-sessions/{id}/commands`: 在 active 终端会话中追加执行一条命令，需 `X-Confirm-Action: ops.terminal.command`
- `POST /ops/terminal-sessions/{id}/close`: 关闭终端会话，需 `X-Confirm-Action: ops.terminal.close`
- `GET /ops/backup/export`: 导出 MVP 备份 JSON，需管理员角色和 `X-Confirm-Action: ops.backup.export`
- `POST /ops/backup/validate`: 校验备份 JSON，管理员可用。返回 schema、版本、内容格式、关键唯一性、引用完整性、租户服务器配额、网络配置格式/重叠、部署网络、告警规则和目标库非空检查结果；该接口只做 dry-run，不写入数据库
- `POST /ops/backup/restore`: 执行备份恢复，需管理员角色和 `X-Confirm-Action: ops.backup.restore`。恢复前会复用预检逻辑，存在 error 时拒绝；目标库必须是 fresh 状态（允许仅有 bootstrap admin 用户和登录审计，其他业务表、凭据表、SSH 配置、BMC 端点、启动事件和 metadata token 均必须为空），否则返回 `409`，避免覆盖或混入已有业务数据

脚本任务默认使用模拟执行器；设置 `SSH_OPERATIONS_MODE=ssh` 后，会复用每台资产的 `SSHAccess` 和加密凭据，通过 Go SSH 在真实主机上执行脚本并记录 stdout、stderr 和 exit code。创建任务时会校验 `server_ids` 必须存在、不能重复、目标资产不能处于 `retired` 或 `scrapped`，单次最多 100 台，`concurrency` 不超过 50，`timeout_seconds` 不超过 3600。真实 SSH 模式仍按 `concurrency` 分批执行，并对每台目标保留独立结果。前端目标选择会隐藏 `retired`/`scrapped` 资产。

日志采集默认生成 `syslog`、`dmesg`、`hardware` 三类模拟 `LogEvent`；设置 `SSH_OPERATIONS_MODE=ssh` 后，会通过 Go SSH 从真实主机读取 syslog 或 journald、`dmesg`、基础硬件摘要并写入 `LogEvent`。目标资产同样不能处于 `retired` 或 `scrapped`，单次最多 100 台，重复目标会返回 `400`。日志内容会做长度截断；生产接入外部日志系统时仍应在写入前做敏感信息脱敏。

终端会话默认只记录模拟 transcript；设置 `SSH_OPERATIONS_MODE=ssh` 后，打开会话会先通过 Go SSH 验证真实主机连接，并允许通过 `/commands` 接口追加执行命令，命令、输出、stderr、exit code 和错误都会追加到 transcript。打开会话时要求服务器存在且不能处于 `retired` 或 `scrapped`，`reason` 最长 255 字符，前端目标选择同样只展示未退役/未报废资产。会话 TTL 默认 60 分钟，由 `TERMINAL_SESSION_TTL_MINUTES` 配置；过期 active 会话会被标记为 `closed`、写入 transcript 自动关闭说明，并记录 `ops.terminal.auto_close` 高风险审计。完整交互式 WebSSH/PTY 可在该命令级审计基础上继续扩展。

MVP 阶段备份导出覆盖租户、网络配置、资产、状态历史、退役记录、镜像、模板、部署、工作流、监控、日志、运维任务、告警、告警规则和审计数据；默认不导出 `credentials`、`ssh_accesses`、BMC 端点、启动事件和 metadata token。恢复执行会清理受管表与这些运行态/敏感表，导入备份中的白名单集合，并重置 PostgreSQL/SQLite 自增序列，确保恢复后可以继续新增数据。用户密码哈希不会从备份恢复，普通导入用户会被写入不可登录的随机占位密码；执行恢复的当前管理员账号会保留目标环境中的现有密码作为恢复入口，其余用户恢复后需要管理员重置密码。

## Audit

- `GET /audit-logs`: 查询审计日志。无查询参数时返回最近 300 条数组；携带 `page` 或 `page_size` 时返回 `{ items, total, page, page_size }`。支持 `action`、`actor_email`、`resource_type`、`risk_level` 筛选。响应包含 `request_id`，可关联结构化访问日志。
- `GET /audit-logs/{id}`
