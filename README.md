# 统一裸金属生命周期管理平台 MVP

这是基于 `裸金属生命周期管理平台-设计方案.md` 创建的裸金属生命周期管理平台。默认以安全的本地/演示配置启动；真实 PXE/DHCP/TFTP、物理 Redfish/IPMI 和真实 SSH 主机能力需要在隔离实验网或生产部署网中显式启用和验证，避免影响现网。

## 功能范围

- 本地管理员登录和 JWT 鉴权
- 登录接口带基础失败次数限流，并审计失败/被限流登录，降低默认账号和弱口令被暴力尝试的风险
- 前端按 `admin`、`operator`、`viewer` 角色收敛菜单、页面入口和高风险操作按钮，并统一提示 403 无权限操作
- 前端提供路由错误边界、懒加载失败恢复入口、统一空状态和后端不可达/5xx 全局提示
- 前后端请求链路支持 `X-Request-ID`，后端输出 JSON 访问日志并把 request_id 写入审计日志，便于排障追踪
- 后端启动时执行配置校验和镜像目录写入探针，生产环境会阻止默认密钥、默认密码、SQLite、非法适配器或无效启动地址等不安全配置启动
- 真实 PXE/DHCP/TFTP 服务可通过 `BOOT_SERVICES_ENABLED=true` 显式启用，支持 ProxyDHCP、内置 DHCP 和外部 DHCP + TFTP 三种模式；默认关闭 UDP 监听
- 后端数据库初始化支持启动重试，Docker Compose 会等待 PostgreSQL/Redis 健康后再启动后端，降低冷启动竞态
- 提供 `/readyz` 运行自检接口，汇总数据库、Redis、镜像目录、BMC 工具链、SSH known_hosts 覆盖、PXE/DHCP/TFTP 运行探针和配置 warning/error，并在系统管理页展示
- 提供 `/api/v1/system/lab-validation` 真实验收报告、按资产聚合的目标矩阵、受保护的安全检查执行入口、验收包导出和物理证据记录入口，在系统管理页集中展示 PXE 启动事件、BMC 连通性、SSH 主机状态、每台资产最近验收批次和实验室证据
- 系统管理页支持网络配置只读检查，展示 CIDR、网关、DNS、DHCP/ProxyDHCP、PXE/DHCP/TFTP runtime 匹配状态、runtime 地址与部署网 CIDR 一致性和部署网可用性结果
- 管理员用户管理、角色调整和密码重置
- 管理员租户台账、状态和基础配额字段管理
- 管理网、部署网、业务网基础配置台账，部署前校验启用的部署网段
- 服务器资产管理、编辑、筛选分页、批量导入、身份字段应用层和数据库层唯一性校验、状态流转、状态历史、退役/报废和受保护资产删除
- PXE/iPXE 请求未知 MAC 时自动形成 `discovered` 资产并记录状态历史
- 资产生命周期状态覆盖 `discovered`、`ready`、`deploying`、`running`、`maintenance`、`retired`、`scrapped`；资产写入时校验状态/架构枚举、IP/MAC 格式，并把 MAC 统一为小写冒号格式
- 资产详情侧可查看 7 天保留窗口内的最新监控指标、单机采集历史，并可在资产上下文触发采集或配置 SSH 采集参数
- 硬件盘点快照、单机 BOM 和全量 BOM CSV 导出
- 镜像上传导入、手工路径登记、编辑、启停、未引用镜像删除、筛选分页和校验状态管理
- 安装模板和工作流模板筛选分页与管理，写入时校验模板类型、状态、变量 schema 和 workflow steps，默认内置 Ubuntu Server 24.04 Autoinstall、Rocky Linux 9 Kickstart、Debian 12 Preseed 三类 Linux 安装模板
- 镜像文件 SHA256/大小校验，以及部署前镜像、服务器、BMC Endpoint 连通性、部署网段和模板检查
- Demo seeder 会在 `IMAGE_STORAGE_DIR` 下生成 `demo-ubuntu-24.04.iso` 占位文件并登记为已校验镜像，便于首次启动直接演示模拟部署
- 上传镜像默认保存到 `IMAGE_STORAGE_DIR`，上传时会计算 SHA256 和大小并自动标记为已校验；手工登记的镜像路径也必须位于 `IMAGE_STORAGE_DIR` 内，公共下载接口不会服务目录外文件
- 手工登记镜像不能由客户端直接写入校验状态、SHA256 或大小，必须通过上传或 `/images/{id}/verify` 才能进入已校验状态；已被部署任务引用的镜像不能删除，应改为禁用
- 单台/批量部署任务创建、部署网络选择、擦除/重装显式确认、筛选分页、并发受限排队、模拟工作流执行、部署日志、步骤耗时、运行尝试历史和失败/取消后的受保护重试
- 部署取消会停止模拟工作流继续写入成功状态，并恢复服务器可部署状态；失败部署也会恢复服务器可部署状态
- BMC 模拟单机/批量电源操作、连通性检查和固件信息查询
- 运维工具中的批量脚本任务默认模拟执行；设置 `SSH_OPERATIONS_MODE=ssh` 后通过真实 SSH 主机执行并记录 stdout/stderr/exit code
- 运维工具中的系统日志、dmesg 和基础硬件日志一键模拟采集，并写入统一日志事件
- 运维工具中的模拟远程终端会话、会话记录、TTL 自动关闭和审计
- 运维工具中的 MVP 数据备份 JSON 导出、恢复预检、fresh 目标库恢复执行、恢复管理员入口保留、恢复后自增序列修正和审计
- 运维工具列表支持筛选分页，便于追踪批量脚本和终端会话
- 基础监控告警筛选分页、确认、关闭、处理说明和处理记录
- 基础告警规则管理和按 7 天保留窗口内的最新指标手动评估，默认覆盖离线、磁盘满、CPU 高、内存高
- SSH Agentless 采集任务可按服务器、状态、模式和发起人全局筛选分页
- 日志事件可按关键字、服务器、来源和级别筛选分页，为后续接入 syslog/agent 日志预留边界
- 审计日志
- 审计日志支持动作、用户、资源类型、风险级别筛选和服务端分页
- React + Ant Design 管理台
- 前端已启用路由级懒加载，页面代码按路由拆分，降低首屏 JavaScript 体积
- 总览仪表盘展示资源总数、状态分布、告警态势、最近部署和最近审计操作

## 默认账号

- 邮箱: `admin@example.com`
- 密码: `Admin@123456`

生产部署前必须设置 `APP_ENV=production`，并修改 `.env` 中的 `ADMIN_PASSWORD`、`JWT_SECRET` 和 `CREDENTIAL_KEY`。生产环境还必须使用 PostgreSQL，不能使用 SQLite，并必须设置 `ENABLE_DEMO_SEEDER=false`，避免真实环境自动写入演示资产、镜像、告警和模板数据。

## 本地开发

后端默认支持 SQLite，便于没有 PostgreSQL 的开发机启动：

```bash
cd backend
go mod tidy
go test ./...
go run ./cmd/api
```

SQLite 驱动依赖 CGO；本地运行需安装 Go 1.22+ 和可用 C 编译工具链。Docker Compose 默认使用 PostgreSQL，但后端二进制仍保留 SQLite 开发模式，因此容器构建阶段不关闭 CGO。

前端：

```bash
cd frontend
npm install
npm run dev
```

访问 `http://localhost:5173`。

前端生产构建验证：

```bash
cd frontend
npm run build
```

## Docker Compose

需要本机安装 Docker：

```bash
cd deploy
docker compose up -d --build
```

默认 `deploy/docker-compose.yml` 面向本机演示，会读取 `.env.example`，不开启真实 DHCP/TFTP UDP 监听。Linux 实验网或生产部署网需要先基于 `.env.production.example` 准备 `.env.production`，放入真实 bootloader 和 SSH host key 清单，再使用生产覆盖文件：

```bash
mkdir -p deploy/tftp
ssh-keyscan 192.0.2.10 > deploy/known_hosts
cd deploy
docker compose --env-file ../.env.production -f docker-compose.production.yml up -d --build
curl -fsS http://127.0.0.1:8080/readyz
```

生产覆盖文件会让后端使用 Linux `host` 网络，以便 DHCP/ProxyDHCP 广播和 TFTP 绑定部署 VLAN 接口；只应在隔离部署网使用。`deploy/tftp` 会挂载为 `BOOT_TFTP_ROOT`，需放入 `ipxe.efi`、`undionly.kpxe` 等真实启动加载器；`deploy/known_hosts` 会挂载为生产 SSH 验证使用的 `SSH_KNOWN_HOSTS_PATH`，必须包含真实 SSH 主机 host key，不能是空文件。生产 `/readyz` 要达到 `status=ok` 时，known_hosts 可以使用可静态匹配的主机名、`[host]:port`、通配 pattern，或 `ssh-keyscan -H` 生成的 OpenSSH 哈希主机 pattern。如 Redfish BMC 使用私有 CA，可把 PEM 放入 `deploy/certs` 并设置 `REDFISH_CA_CERT_PATH=/etc/baremetal/certs/redfish-ca.pem`。

物理 PXE 客户端启动、BMC Endpoint 和 SSH Access 配好后，可用脚本从命令行执行一次严格验收并导出 JSON 验收包：

```powershell
pwsh -File .\tools\physical-validation.ps1 -BaseUrl http://127.0.0.1:8080 -Email admin@example.com -ServerId 1 -PXEMac "52:54:00:aa:bb:cc" -OutDir .\lab-validation-output
```

默认脚本只读执行严格验收并导出 `readyz`、run result 和 evidence bundle；默认交互读取管理员密码，自动化场景可设置 `BAREMETAL_ADMIN_PASSWORD`，或用 `-PasswordEnvVar` 指向其他环境变量，避免把密码写入命令行历史。显式追加 `-RecordEvidence` 时，脚本会从现场清单中筛选已通过且属于本次 `-ServerId`/`-PXEMac` 的 PXE BootEvent、BMC 身份 proof 和 SSH known_hosts 命令 proof，分别记录 `kind=pxe|bmc|ssh` 单项证据并输出 `*-item-evidence.json`；如果本次请求范围内某台资产的 PXE/BMC/SSH 三项都已满足，可再追加 `-RecordFullEvidence`，脚本会通过受保护 API 记录 `kind=full` 证据并自动复跑一次严格验收，输出 `*-full-evidence.json` 和 `*-rerun-*` 结果。
多目标验收时，`tools/physical-validation.ps1` 同时接受 PowerShell 数组和逗号分隔字符串，例如 `-ServerId 1,2,3 -PXEMac "52:54:00:aa:bb:01,52:54:00:aa:bb:02"`，便于通过 `pwsh -File`、CI 或远程执行器调用。

- 前端: `http://localhost:8081` 或 `http://127.0.0.1:8081`
- 后端健康检查: `http://localhost:8080/healthz`
- 后端运行自检: `http://localhost:8080/readyz`

`/healthz` 会检查数据库连接；当 `REDIS_ADDR` 已配置时也会执行 Redis `PING`，Docker Compose 环境默认检查 `redis:6379`。
`/readyz` 会返回数据库、Redis、镜像存储目录、BMC 工具链、SSH known_hosts 覆盖、PXE/DHCP/TFTP 运行状态和配置校验结果；部署前建议确认 `status` 为 `ok`，或处理 `config_issues` 中的 warning/error。`BMC_ADAPTER=ipmi` 时会检查运行环境是否能找到 `ipmitool`；`SSH_HOST_KEY_POLICY=known_hosts` 时会静态检查当前已配置 SSH 目标是否匹配 known_hosts 条目，支持普通主机名、`[host]:port`、通配 pattern 和 OpenSSH `|1|...` 哈希 pattern；`BOOT_SERVICES_ENABLED=true` 时会通过带 `blksize/timeout/tsize` options 的 UDP RRQ 拉取 TFTP `boot.ipxe` 并要求 OACK 协商成功，再在 `proxy`/`builtin` 模式下发送合成 PXE DHCPDISCOVER 校验 bootfile 响应，报告 option 54 `server_identifier`、BOOTP `next_server`(`siaddr`)、option 66 `tftp_server_name`、option 67 和 BOOTP file，该合成探针不会写入 BootEvent。
系统管理页“真实验收”会调用 `/api/v1/system/lab-validation` 汇总 PXE/BMC/SSH 验收状态，并通过目标矩阵按资产展示 PXE、BMC、SSH、证据状态、最近验收批次/结果、全链路 ready 标记和缺口原因；有 `primary_mac` 的资产即使尚未产生 BootEvent，也会进入候选目标，PXE BootEvent 可通过 `server_id` 或事件 MAC 匹配库存 `primary_mac` 计入该目标，并可从行内直接发起严格检查或补录证据。BootEvent 会记录来源：`http_ipxe` 表示来自已启用 `deployment` 网络 CIDR 的客户端请求 iPXE 脚本，`pxe_dhcp` 表示内置 DHCP/ProxyDHCP 收到真实 PXE 请求，`api_event` 表示手工 API 补录、部署网段外的 HTTP iPXE 请求或带 `X-Lab-Validation-Probe: 1` 的内部验收 HTTP 探测；严格 PXE、`ok` 级 PXE/full 证据和全链路 ready 只接受 7 天内 `http_ipxe`/`pxe_dhcp` 这类真实启动链路来源，目标矩阵会优先采用最新真实 PXE 事件，后续 `api_event` 不会覆盖已有真实启动证据；`api_event`、空来源和 `unknown` 只用于兼容历史、补录和排障。执行检查需要管理员角色和 `X-Confirm-Action: system.lab-validation.run`，会只读探测 `BOOT_BASE_URL/boot/ipxe`、已启用的 DHCP/ProxyDHCP bootfile 响应、已启用的 TFTP `boot.ipxe`、BMC/SSH 连通性，不执行电源动作，也不动态启用 DHCP/TFTP。严格 BMC 检查会继续执行只读固件/身份探针，Redfish 读取 Systems/Managers，IPMI 读取 `mc info`，批次结果会记录厂商、型号、序列号和版本摘要，并把这些非敏感身份字段写入 run result `details`。SSH 检查会执行单行只读探针命令，默认 `printf 'ok '; hostname; id -un; uname -srm`，也可通过 `ssh_probe_command` 覆盖，批次结果会记录 stdout/stderr/exit_code 摘要，并把 command、exit_code、stdout、stderr 和错误摘要写入 run result `details`。合成 DHCP 探测不会写入 BootEvent，HTTP 探测也会强制降级为 `api_event`；执行检查时可开启 `strict=true`，要求 `pxe_macs` 指向真实 PXE 客户端、`server_ids` 指向真实 BMC/SSH 资产，且 BMC 不能使用模拟适配器。当严格运行同时检查 PXE、BMC 和 SSH 且传入 `server_ids` 时，会为每台资产额外写入 `full_chain_target` 结果，只有 PXE BootEvent、BMC、SSH 和新鲜完整物理证据都闭环时才通过。执行检查时可填写 `server_ids` 把 BMC/SSH 限定到指定真实资产，也可填写 `pxe_macs` 校验指定真实 PXE 客户端是否已产生启动事件。每次执行都会生成 `run_id` 并持久化批次、目标、请求人、Request ID 和单项结果，系统管理页会展示最近 10 次运行，并可通过 `/api/v1/system/lab-validation/runs/{id}/evidence-bundle` 查看该批次的环境快照、检查结果、关联 BootEvent、BMC、SSH、真实终端会话 transcript、脚本执行结果、日志事件和配置问题。真实 PXE 客户端、物理 Redfish/IPMI、真实 SSH 主机或全链路实验结果可通过 `POST /api/v1/system/lab-validation/evidence` 记录非敏感证据摘要，确认头为 `X-Confirm-Action: system.lab-validation.evidence`；证据可传 `run_id` 关联一次已完成且 7 天内的验收批次，`ok` 全链路证据必须传入一次严格 PXE+BMC+SSH 运行的 `run_id`，且该运行包含目标 PXE/BMC/SSH 成功结果；`tools/physical-validation.ps1 -RecordEvidence` 可在严格运行后按现场清单自动补录已通过的 `pxe`、`bmc`、`ssh` 单项证据，`-RecordFullEvidence` 则补录全链路证据并复跑；`ok` 级证据必须绑定真实平台引用：PXE 证据传真实链路 `boot_event_id`，其中 `http_ipxe` 必须由部署网段内客户端 IP 自动分类产生；BMC/SSH 证据传 `server_id` 且对应最近连通检查为 `ok`，全链路证据同时传 `server_id` 与真实链路 `boot_event_id`。`physical_evidence` 只把 7 天内且引用完整的 `ok` 证据计入通过。
严格运行同时传入 `server_ids` 和 `pxe_macs` 时，PXE BootEvent 必须通过事件 `server_id` 或事件 MAC 匹配资产 `primary_mac` 归属到请求资产，避免把同一部署网中其他机器的启动事件误计入当前目标。
验收包会额外返回 `operator_checklist` 和 `evidence_candidates`，按目标列出 PXE BootEvent、BMC 身份探针、SSH 命令证明和 full-chain 证据引用的完成状态、下一步动作、阻塞原因和后端生成的可记录证据草案；BMC 清单项只有在本次运行结果成功且 `details` 含结构化身份字段时才标记为 `ok`，SSH 清单项只有在 `details` 含 `host_key_policy=known_hosts`、`host_key_verified=true`、`host_key_sha256`、command、exit_code 和 stdout 时才标记为 `ok`，便于物理实验室现场复核。系统管理页打开验收包后，可从状态已满足的清单行或 `evidence_candidates` 直接预填并记录 PXE/BMC/SSH 单项证据；当同一资产的 PXE、BMC、SSH 三项都为 `ok` 且有 BootEvent 引用时，也可从候选项预填全链路证据，最终仍由后端 evidence API 执行真实引用校验。
IPMI 身份 proof 会额外包含 Device ID、Device Revision、Manufacturer ID 和 Product ID；这些字段与厂商、型号、序列号、BIOS/固件/BMC 版本一样，均属于非敏感结构化验收字段。
`ok` 全链路证据同样要求引用的严格运行包含目标 PXE BootEvent 成功结果、带 `adapter=redfish|ipmi` 和结构化身份 `details` 的 BMC 成功结果，以及带 known_hosts host key verified/SHA256、command、exit_code 和 stdout `details` 的 SSH 成功结果。
`ok` BMC 和 SSH 单项证据也必须引用最近完成的严格验收 `run_id`，并要求该批次中对应目标存在来自 `redfish`/`ipmi` 的结构化 BMC 身份 proof，或 SSH known_hosts host key proof 加 command/exit_code/stdout proof；生产 SSH 验证必须使用 `SSH_HOST_KEY_POLICY=known_hosts` 和可读、可解析且非空的 `SSH_KNOWN_HOSTS_PATH`；仅有当前 Endpoint/Access 状态为 `ok` 不再足以证明物理验收通过。
Docker Compose 为 PostgreSQL、Redis、后端和前端配置了容器健康检查；后端会等待 PostgreSQL/Redis healthy 后启动，前端会等待后端 `/healthz` healthy 后启动。生产覆盖文件会把后端健康检查切换为 `/readyz` 且要求 `status=ok`，用于提前暴露 PXE/TFTP、BMC 工具链、known_hosts 和配置问题。后端自身也会按 `DB_CONNECT_MAX_ATTEMPTS` 和 `DB_CONNECT_RETRY_DELAY_MS` 重试数据库初始化，避免数据库冷启动时短暂不可用导致容器直接退出。
Docker Compose 会把后端 `/app/data` 挂载到 `backend-data` 卷，用于持久化 demo 镜像和上传镜像文件。
前端镜像构建时可通过 `VITE_API_BASE_URL` 和 `VITE_API_ROOT_URL` 覆盖浏览器访问后端的地址；默认值适配本机 `localhost:8080` 演示。
生产前端构建建议设置 `VITE_SHOW_DEMO_CREDENTIALS=false`，避免登录页展示或预填演示账号。
后端会为每个请求生成或沿用 `X-Request-ID`，响应头会返回同名字段；访问日志为 JSON 行格式，审计日志中的 `request_id` 可与访问日志关联。

## 生产配置要求

- `APP_ENV=production` 会启用阻断式配置校验。
- `.env.production.example` 提供真实 PXE/BMC/SSH 验收的生产变量模板，复制后必须替换所有 `REPLACE_ME` 和示例地址，默认值会故意触发启动校验失败，避免误用。
- `CORS_ALLOWED_ORIGINS` 应设置为生产前端访问域名的 origin 列表，例如 `https://console.example.com`；同源反向代理部署也建议显式记录预期 origin。
- `JWT_SECRET` 和 `CREDENTIAL_KEY` 必须覆盖开发默认值，且长度至少 32 个字符。
- `LOGIN_RATE_LIMIT_ATTEMPTS` 和 `LOGIN_RATE_LIMIT_WINDOW_SECONDS` 控制登录失败限流窗口，必须为正整数。
- `TERMINAL_SESSION_TTL_MINUTES` 控制远程终端会话自动关闭窗口，必须为正整数，默认 60 分钟。
- `ADMIN_PASSWORD` 必须覆盖默认密码 `Admin@123456`。
- `DB_DRIVER` 必须为 `postgres`，`DATABASE_URL` 指向生产 PostgreSQL，且不能保留 `REPLACE_ME` 占位符。
- `DB_CONNECT_MAX_ATTEMPTS` 和 `DB_CONNECT_RETRY_DELAY_MS` 必须是正整数，用于控制数据库初始化重试次数和间隔。
- `DEPLOYMENT_CONCURRENCY` 控制部署工作流并发执行槽，必须是正整数，默认 20；超出并发的部署会保持 `pending` 等待执行。
- `BOOT_SERVICES_ENABLED` 默认必须为 `false`；仅在隔离实验网/部署网启用。启用后必须设置 `BOOT_BIND_INTERFACE`、`BOOT_DHCP_SERVER_IP`、`BOOT_TFTP_ROOT`、`BOOT_TFTP_BOOTFILE_UEFI` 和 `BOOT_TFTP_BOOTFILE_BIOS`；生产环境还必须把 `BOOT_DHCP_LISTEN_ADDR`、`BOOT_TFTP_LISTEN_ADDR` 写成部署网接口 IP + 端口，不能使用 `:67`/`:69` 这种全接口监听；`BOOT_SERVICE_MODE=builtin` 还必须设置 `BOOT_DHCP_LEASE_START` 和 `BOOT_DHCP_LEASE_END`。
- `BMC_ADAPTER` 必须为 `simulated`、`redfish` 或 `ipmi`；生产使用 `BMC_ADAPTER=redfish` 时 BMC Endpoint 必须使用 `https://`，必须设置 `REDFISH_INSECURE_TLS=false`，并让运行环境信任 BMC 管理口证书或其 CA；私有 CA 可通过 `REDFISH_CA_CERT_PATH` 指向 PEM 文件，启动校验会检查该文件可读且包含证书；`http://` Redfish 和 `REDFISH_INSECURE_TLS=true` 仅适合开发/实验室自签证书验证。
- `COLLECTOR_MODE` 必须为 `simulated` 或 `ssh`。
- `SSH_OPERATIONS_MODE` 控制运维脚本、日志采集和终端命令，支持 `simulated` 或 `ssh`；`ssh` 模式会复用资产 SSH 配置和加密凭据连接真实主机。`SSH_HOST_KEY_POLICY` 支持 `insecure_ignore` 和 `known_hosts`；生产环境只要启用 `COLLECTOR_MODE=ssh` 或 `SSH_OPERATIONS_MODE=ssh`，就必须设置 `SSH_HOST_KEY_POLICY=known_hosts` 并配置可读、可解析且至少包含一条 host key 的 `SSH_KNOWN_HOSTS_PATH`。`/readyz` 和真实验收报告会静态检查当前 SSH 目标覆盖情况，支持非哈希条目和 `ssh-keyscan -H` 生成的 OpenSSH 哈希条目；真实 SSH 握手会通过 Go SSH known_hosts verifier 校验主机密钥，并把非敏感的 host key 算法和 SHA256 指纹写入验收 proof。
- `BOOT_BASE_URL` 必须是包含 host 的 `http` 或 `https` 地址；生产环境不能使用 `localhost`、loopback 或 `0.0.0.0`，必须填写安装环境可访问的平台地址。
- `METADATA_REQUIRE_DEPLOYMENT_NETWORK` 在生产环境必须为 `true`；Metadata/Userdata API 只允许来自已启用 `deployment` 网络 CIDR 的客户端访问。
- `ENABLE_DEMO_SEEDER` 在生产环境必须为 `false`；演示环境可保持 `true` 以便首次启动生成示例资产、镜像、模板和告警。
- `IMAGE_STORAGE_DIR` 必须可创建、可写入并可删除临时探针文件；`IMAGE_UPLOAD_MAX_MB` 必须是正整数。
- 生产环境建议配置 `REDIS_ADDR`；未配置不会阻止启动，但 `/readyz` 会给出 warning。

## 演示流程

1. 使用默认账号登录。
2. 在“总览”查看资产状态、部署状态、告警级别、最近部署和最近操作。
3. 在“系统管理”创建 operator/viewer 用户、调整角色或重置密码。
4. 在“系统管理”维护租户台账、基础配额字段和网络配置。
5. 在“资产管理”查看、新建、编辑或批量导入服务器资产。
6. 在“资产管理”记录硬件信息，并导出单机或全量 BOM CSV。
7. 在“资产管理”查看资产状态历史、最新监控指标和单机采集历史。
8. 在“镜像管理”上传或登记、编辑、禁用/启用、校验或删除 ISO 镜像。
9. 在“模板管理”查看内置 Ubuntu/Rocky/Debian 安装模板，或维护自定义安装模板和工作流模板。
10. 在“资产管理”为目标服务器配置 BMC Endpoint；MVP 默认使用模拟 BMC。
11. 在“部署任务”选择一台或多台服务器、镜像、部署网络、擦除策略并确认重装风险后创建部署。
12. 等待模拟工作流完成，打开部署日志查看步骤、耗时和运行尝试历史。
13. 在“资产管理”执行 BMC 连通性检查、固件信息查询、电源状态查询、模拟单机或批量开机、关机、重启。
14. 在“运维工具”创建批量脚本任务并查看执行结果；默认使用模拟执行器，`SSH_OPERATIONS_MODE=ssh` 时会在真实 SSH 主机执行。
15. 在“运维工具”一键采集系统日志、dmesg 和基础硬件日志，并在“监控告警”的“日志事件”页签查看事件。
16. 在“运维工具”打开远程终端、查看会话记录、按需执行命令并关闭会话。
17. 在“运维工具”导出 MVP 备份 JSON，或上传备份文件执行恢复预检；fresh 目标库可执行受保护恢复，恢复后仍可继续新增业务数据。
18. 在“监控告警”维护告警规则、执行规则评估、确认或关闭告警，并在“采集任务”和“日志事件”页签查看采集历史与事件日志。
19. 在“审计日志”查看登录、部署、BMC、模板、用户/租户管理、运维工具、硬件盘点和告警操作记录。

## 当前实现边界

- 后端已实现基础 RBAC 和危险操作确认。高风险接口需要 `X-Confirm-Action` 请求头，前端已在对应操作中自动携带。
- 前端会在登录和刷新后调用 `/auth/me` 获取当前角色；`viewer` 默认只展示查看能力，`operator` 可执行日常运维写操作，`admin` 额外拥有系统管理、凭据配置、镜像删除、模板删除和备份导出能力。
- BMC 和 SSH 凭据使用 AES-GCM 加密保存；接口响应不返回明文密码或内部凭据引用；更新配置时未提供新密码/secret 会保留原凭据，客户端提交的内部凭据引用会被忽略。
- 用户管理接口为 `/api/v1/users`，仅 `admin` 角色可用；支持 `admin`、`operator`、`viewer` 三种基础角色，后端会校验邮箱格式、角色枚举和基础密码强度，并拒绝把最后一个管理员降级。
- 租户管理接口为 `/api/v1/tenants`，仅 `admin` 角色可用；租户 ID、状态和 quota JSON 写入时会校验，`quota.servers` 会作为服务器资产数上限强制执行，资产创建、迁入和批量导入时会校验非空 `tenant_id` 必须存在、为 `active` 且未超额，租户配额也不能调低到低于当前使用量。
- 网络配置接口为 `/api/v1/network-configs`，仅 `admin` 可写；MVP 记录管理网、部署网、业务网的 CIDR、网关、DNS、VLAN 和 DHCP/ProxyDHCP 模式，并校验 CIDR、网关归属、DNS、VLAN、枚举值和同用途启用网段重叠，部署前要求至少存在一个启用的 `deployment` 网络。资产 `tags` 支持 JSON 数组或对象，前端资产表可展示和编辑标签，关键字查询会匹配标签内容。
- `backend` 已提供 Go 源码、测试和 Dockerfile，但需要本机或 CI 环境安装 Go 1.22+ 才能执行 `go test`。
- `deploy/docker-compose.yml` 需要 Docker 环境。
- Redis 通过 `REDIS_ADDR`、`REDIS_PASSWORD`、`REDIS_DB` 配置，未配置时本地 SQLite 开发模式会跳过 Redis 健康检查。
- BMC、部署工作流和监控指标默认使用模拟实现，用于验证平台业务闭环；配置 `BMC_ADAPTER=redfish` 或 `ipmi` 后会连接物理 BMC，配置 `COLLECTOR_MODE=ssh` 后会通过 Go SSH 采集真实主机指标。部署创建会要求目标服务器已配置 BMC Endpoint，并调用当前 `BMC_ADAPTER` 做连通性检查，同时要求平台已配置启用的部署网段；请求可传 `network_id` 绑定本次部署使用的启用 `deployment` 网络，未传时保留旧的自动选择兼容行为，且请求必须包含 `erase_confirmed=true` 和擦除策略记录，确保重装/擦盘风险有显式确认。系统管理页可对单条网络配置执行只读检查，覆盖 CIDR、重叠、网关、DNS、状态、DHCP/ProxyDHCP 和部署网可用性；真实 DHCP/TFTP 监听只由 `BOOT_SERVICES_ENABLED=true` 启动。批量部署接口单批最多 20 台，全部目标 preflight 通过后才会创建部署，并可使用同一个 `network_id`。部署工作流受 `DEPLOYMENT_CONCURRENCY` 控制，超出并发的任务保持 `pending` 排队，拿到执行槽后才进入 `running`。工作流模板 action 可使用 `simulate_failure` 演示失败状态，失败或已取消部署可通过 `X-Confirm-Action: deployment.retry` 按原擦除策略重新执行；部署日志接口会保留所有 workflow run 尝试历史，并返回最新 run 的任务统计和 `duration_ms`。
- Demo seeder 默认提供 Ubuntu Server 24.04、Rocky Linux 9 和 Debian 12 三类 Linux 安装模板，覆盖 cloud-init/autoinstall、Kickstart 和 Preseed 三种常见自动化安装入口。
- 自定义安装模板写入时要求 `name`、`template_type` 和非空 `content`，`template_type` 仅允许 `cloud-init`、`autoinstall`、`kickstart`、`preseed`、`unattend`，`variables_schema` 必须是 JSON object；工作流模板要求 `definition.steps` 至少包含一个带 `name` 和 `action` 的步骤。
- 安装模板和工作流模板删除需要管理员角色与二次确认；已有部署引用的模板不能删除，应改为禁用，以保留历史部署可追溯性。
- 默认种子镜像是用于校验和模拟部署的占位文件，不是可启动的真实操作系统 ISO。
- `IMAGE_STORAGE_DIR` 控制上传镜像、手工登记镜像和默认种子镜像的可服务目录，默认 `data/images`；`IMAGE_UPLOAD_MAX_MB` 控制单文件上传大小，默认 20 MB。
- 告警确认和关闭会记录处理人、处理时间、处理说明和审计日志。
- 真实 PXE/DHCP/TFTP 通过 `PXEService` 接入：TFTP 只服务 `BOOT_TFTP_ROOT` 内文件并提供动态 `boot.ipxe`/`auto.ipxe`/`default.ipxe`，支持真实 PXE/iPXE 客户端常见 RRQ options `blksize`、`timeout`、`tsize` 的 OACK 协商；ProxyDHCP/内置 DHCP 会把 PXE 客户端链到平台 `/boot/ipxe`，并同时写入 option 54、BOOTP `siaddr`、option 66、option 67 和 BOOTP file，兼容读取不同字段的物理 PXE 固件。启用前必须绑定隔离部署网/VLAN，生产环境需显式监听部署网接口 IP，避免 DHCP/TFTP 暴露到非部署网卡。
- 后端已提供 iPXE 和 Metadata API 边界：`/boot/ipxe`、`/boot/events`、按客户端 IP 匹配的 `/metadata/hostname`/`network`/`ssh-keys`/`userdata`，`/metadata/by-token/{token}/...`，以及兼容用 `/metadata/by-server/{id}/...`、`/metadata/by-mac/{mac}/{field}`、`/metadata/by-ip/{ip}/{field}`、`/metadata/by-deployment/{id}/{field}`，未知 MAC 会进入 `discovered` 资产台账；部署 iPXE 脚本默认使用 24 小时有效的 metadata token，过期后会轮换；生产环境会限制 Metadata/Userdata API 只能从启用的部署网 CIDR 访问，启动事件和 metadata 访问日志落库时会脱敏 token。
- Metadata network 响应会合并服务器主网卡和部署任务绑定的部署网 CIDR、网关、DNS、VLAN、DHCP/ProxyDHCP 字段；旧部署未绑定网络时回退到最新启用的部署网，便于安装环境生成网络配置。`ssh-keys` 会从部署变量 `ssh_authorized_keys`、`ssh_keys` 或 `ssh_public_key` 输出公钥数组。
- `BOOT_BASE_URL` 用于生成 iPXE 脚本中的平台、镜像和 metadata 地址。
- BMC 默认使用 `BMC_ADAPTER=simulated`；配置 BMC 前会校验资产存在、端点类型、协议和主机/URL 格式，保存或更新后状态为 `unknown`，连通性检查成功后变为 `ok`、失败后变为 `error`，并拒绝对 `retired`/`scrapped` 资产执行 BMC 配置、连通性检查和电源变更；`POST /api/v1/servers/{id}/bmc/check` 成功时会尽量附带与固件查询一致的非敏感身份 proof，资产页会直接展示厂商、型号、序列号或 IPMI 硬件 ID 摘要；真实 Redfish/IPMI 模式下端点 `type` 必须匹配当前 `BMC_ADAPTER`，错配会使检查、固件探针和物理证据失败；电源状态和固件信息是只读查询，终态资产仍可用于排障查看；资产表支持多选后批量开机、关机、重启，批量接口单次最多 50 台并对每个目标写高风险审计；`redfish` 仅允许 `http/https` URL，且 `protocol` 必须与 URL scheme 一致，生产环境会拒绝 `http://` Redfish Endpoint；`ipmi` 仅允许合法 host 或 host:port；配置为 `redfish` 后会通过 Redfish HTTP Basic Auth 调用 `/redfish/v1`、`Systems`、`Managers` 和 ComputerSystem Reset 接口，支持 service-root 相对 `@odata.id`、相对 Reset target 和 `https://bmc/redfish/v1` endpoint，开机、关机、重启分别映射为 `On`、`ForceOff`、`ForceRestart`，失败时会带出 Redfish 错误体摘要；开发/实验室自签证书可临时使用 `REDFISH_INSECURE_TLS=true`，生产环境会阻止该配置并要求校验 BMC TLS 证书；配置为 `ipmi` 后会通过系统 `ipmitool` 执行 `power status/on/off/reset` 和 `mc info`，并把 Device ID、Device Revision、Manufacturer ID、Product ID、厂商名、产品名和固件版本作为结构化身份 proof，运行环境需预装 `ipmitool`。
- 监控采集默认使用 `COLLECTOR_MODE=simulated`；SSH 配置、单机采集和指标查询都会校验资产存在，SSH 配置和单机采集会拒绝 `retired`/`scrapped` 资产，SSH host 必须是合法主机名或 IP，端口必须为 1-65535，`auth_type` 支持 `password` 或 `private_key`；配置为 `ssh` 后会通过内置 Go SSH 执行器执行只读指标采集，支持已保存的 password 或 `private_key` 凭据，单次采集默认 30 秒超时，并可通过 `SSH_HOST_KEY_POLICY=known_hosts` 和 `SSH_KNOWN_HOSTS_PATH` 校验真实主机密钥。资产页 `SSH 检查` 响应会返回非敏感 proof，包括 command、exit_code、stdout/stderr 摘要和 host_key_policy、host_key_verified、host_key_algorithm、host_key_sha256、host_key_host、host_key_remote；只有 known_hosts verifier 通过时 `host_key_verified=true`。MVP 采集指标包括 `host_up`、`cpu_usage`、`memory_usage`、`disk_usage`、`disk_smart_health`、`network_rx_mbps`、`network_tx_mbps`、`process_count`、`process_zombie_count`；`disk_smart_health=0` 表示健康，`1` 表示异常，进程指标用于展示基础进程状态。指标查询和告警评估只读取 7 天保留窗口内的样本，成功采集后会清理更早的历史指标。
- 全局采集任务接口为 `GET /api/v1/collections`，前端“监控告警”页面的“采集任务”页签可按服务器、状态、模式和发起人查询。
- 日志事件接口为 `GET /api/v1/log-events`，MVP 使用演示种子数据并记录 Metadata API 访问事件，后续可接入 syslog、journald、BMC SEL 或 Agent 日志。
- 告警规则接口为 `/api/v1/alert-rules`，MVP 支持对 7 天保留窗口内的最新指标做阈值评估并生成 firing 告警；同一服务器同一规则的未关闭告警会去重并刷新最新指标说明，关闭后再次命中会重新触发新告警；规则写入时会校验规则 ID、指标名、操作符、级别和状态。
- BOM 导出基于服务器 CMDB 字段和最新一条 `HardwareInventory` 快照生成，接口为 `/api/v1/servers/{id}/bom`、`/api/v1/servers/{id}/bom.csv` 和 `/api/v1/bom.csv`。
- 状态历史通过 `ServerStatusHistory` 自动记录资产创建、手动状态修改、部署流转、退役和报废操作；退役/报废接口要求二次确认并写入 `RetirementRecord`、高风险审计和 `lifecycle` 日志，记录终态原因、擦除状态、擦除方式和证据；MVP 记录擦除证明但不在开发机执行真实磁盘擦除。通用资产编辑把状态改为 `retired` 或 `scrapped` 时需要二次确认，重复退役/报废不会重复写历史或终态记录，`scrapped` 资产不能通过退役接口改回 `retired`。资产删除仅限管理员清理无部署、BMC/SSH、硬件、监控、日志、运维、告警或终态记录引用的误建资产，需 `X-Confirm-Action: server.delete`；已有业务记录的资产应走退役或报废。
- 批量导入接口为 `POST /api/v1/servers/import`，接收 `{ "servers": [...] }`，会为每台导入资产写入初始状态历史和一次导入审计。
- 批量脚本接口为 `POST /api/v1/ops/script-jobs`；默认使用模拟执行器，设置 `SSH_OPERATIONS_MODE=ssh` 后会复用资产 `SSHAccess` 和加密凭据，在真实 SSH 主机执行脚本并记录 stdout、stderr 和 exit code。创建任务时会校验目标资产存在、未退役/报废、无重复，单次最多 100 台，并发不超过 50，超时不超过 3600 秒；执行器会按 `concurrency` 分批推进单机执行状态；前端目标选择会隐藏 `retired`/`scrapped` 资产。
- 日志采集接口为 `POST /api/v1/ops/log-collections`；默认为每台目标资产生成 `syslog`、`dmesg`、`hardware` 三类模拟 `LogEvent`，设置 `SSH_OPERATIONS_MODE=ssh` 后会通过真实 SSH 主机读取 syslog/journald、`dmesg` 和基础硬件摘要。单次最多 100 台，要求 `X-Confirm-Action: ops.logs.collect`，并对每台目标写入审计。
- 远程终端接口为 `POST /api/v1/ops/terminal-sessions`；默认创建模拟会话和 transcript，设置 `SSH_OPERATIONS_MODE=ssh` 后会验证真实 SSH 主机并允许通过会话命令接口执行命令、记录输出和审计。前端只允许选择未退役/未报废资产。`GET /api/v1/ops/terminal-sessions` 和详情查询会按 `TERMINAL_SESSION_TTL_MINUTES` 自动关闭过期 active 会话、补写 transcript 并记录 `ops.terminal.auto_close` 审计。
- 备份导出接口为 `GET /api/v1/ops/backup/export`，需要管理员角色和二次确认；恢复预检接口为 `POST /api/v1/ops/backup/validate`，会检查 schema、版本、引用完整性、验收运行历史、关键唯一性、退役记录、网络配置格式/重叠、部署网络、告警规则和目标库状态；恢复执行接口为 `POST /api/v1/ops/backup/restore`，需要管理员角色、二次确认和 fresh 目标库。备份包含非敏感的验收运行批次与结果，不包含 credentials、BMC/SSH 配置、启动事件、物理证据记录和 metadata token。普通用户密码会以不可登录占位哈希恢复并要求后续重置；执行恢复的管理员账号会保留当前目标环境密码作为恢复入口，恢复后会修正 PostgreSQL/SQLite 自增序列。

## 文档

- `docs/api.md`: API 草案和接口行为说明
- `docs/openapi.yaml`: 机器可读 OpenAPI 3.0 草案
- `docs/integration-notes.md`: 真实 PXE、BMC 和监控采集接入边界
- `docs/mvp-acceptance-audit.md`: MVP 验收标准对照和外部实验网检查清单

## 真实能力验证

真实 PXE、Redfish/IPMI、SSH Agentless 采集、远程脚本、日志采集和终端命令的接入边界与实验网验收说明见 `docs/integration-notes.md`。管理员也可以在系统管理页“真实验收”查看 `pxe_boot_events`、`bmc_connectivity`、`ssh_connectivity`、`lab_validation_runs`、`physical_evidence` 等检查结果，执行安全连通性检查，追踪最近验收批次，查看批次验收包，并记录真实实验室证据。
