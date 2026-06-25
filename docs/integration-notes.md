# 真实环境接入说明

平台默认使用安全的本地/演示配置；真实 PXE/DHCP/TFTP、物理 Redfish/IPMI 和真实 SSH 主机能力已经接入，但必须在隔离实验网或生产部署网中显式启用和验证。

## 生产部署样例

- `deploy/docker-compose.yml` 仍用于本机演示，默认读取 `.env.example`，不会启动真实 DHCP/TFTP UDP 监听。
- `.env.production.example` 是真实 PXE/BMC/SSH 验收模板，复制为 `.env.production` 后必须替换 `REPLACE_ME`、示例域名、部署 VLAN IP、BMC/SSH 策略和数据库密码。模板中保留默认管理员密码和短密钥是刻意的，生产启动校验会阻止未修改配置启动。
- `deploy/docker-compose.production.yml` 是 Linux 实验网/生产网独立 Compose 文件。后端使用 `network_mode: host`，让 DHCP/ProxyDHCP 广播和 TFTP 直接绑定 `BOOT_DHCP_LISTEN_ADDR`/`BOOT_TFTP_LISTEN_ADDR` 指向的部署网接口 IP；该模式不适合普通办公网或未隔离共享网络。
- 启动前在 `deploy/tftp` 放入真实 `ipxe.efi`、`undionly.kpxe` 等 bootloader，并写好包含真实主机 host key 的 `deploy/known_hosts`，例如用 `ssh-keyscan <host>` 或 `ssh-keyscan -H <host>` 采集后人工核对指纹。覆盖文件会把它们挂载到 `/app/data/tftp` 和 `/etc/baremetal/known_hosts`，供 `/readyz`、PXE 服务和生产 SSH 验证使用；`/readyz` 支持普通主机名、`[host]:port`、通配 pattern 和 OpenSSH `|1|...` 哈希 pattern 的静态覆盖检查。Redfish BMC 使用私有 CA 时，可把 PEM 放入 `deploy/certs`，并设置 `REDFISH_CA_CERT_PATH=/etc/baremetal/certs/redfish-ca.pem`。
- 生产覆盖文件的后端 healthcheck 会调用 `/readyz` 并要求 `status=ok`。如果 TFTP bootloader 缺失、PXE 监听地址不在部署网、`ipmitool` 缺失、known_hosts 缺失/为空/不可解析或配置仍是演示值，容器会保持 unhealthy，便于上线前发现问题。
- 真实验收仍必须由外部物理设备闭环：物理 PXE 客户端产生 `source=http_ipxe|pxe_dhcp` 的 BootEvent，真实 Redfish/IPMI Endpoint 返回身份/固件字段，真实 SSH 主机在 known_hosts 策略下完成 host key 校验并执行只读探针。没有这些外部证据时，平台只能证明接入能力和内部探针通过，不能证明现场已验收完成。

## 实验室验收接口

- `GET /api/v1/system/lab-validation` 汇总真实能力验收状态，管理员可用。报告包含 PXE/DHCP/TFTP 配置和最近启动事件、启用的部署网段数量、BMC 适配器和端点状态、SSH 模式、SSH Access 状态、按资产聚合的目标矩阵、最近验收运行批次和最近记录的物理验收证据。`targets` 会列出每台候选物理资产的 PXE、BMC、SSH、证据状态、`bmc_required`、最近验收批次/结果、全链路 ready 标记和缺口原因；有 `primary_mac` 的库存资产和已有验收执行结果的资产都会进入候选目标，用于逐台收敛真实验收。BMC 是按目标可选的能力：配置了 BMC Endpoint 的资产要求 BMC proof，没有 BMC Endpoint 的资产只要求 PXE+SSH proof。PXE BootEvent 可通过 `server_id` 或事件 MAC 匹配库存 `primary_mac` 计入目标，覆盖首次实机启动尚未显式绑定资产的场景。
- BootEvent 会记录 `source`：`http_ipxe` 表示来自已启用 `deployment` 网络 CIDR 的客户端请求 iPXE 脚本，`pxe_dhcp` 表示内置 DHCP/ProxyDHCP 收到真实 PXE 请求，`api_event` 表示手工 API 补录、部署网段外的 HTTP iPXE 请求或带 `X-Lab-Validation-Probe: 1` 的内部验收 HTTP 探测。严格 PXE 检查、`ok` 级 PXE/full 证据和全链路 ready 只接受 7 天内 `http_ipxe`/`pxe_dhcp` 等真实启动链路来源；目标矩阵会优先采用最新真实 PXE 事件，后续 `api_event` 不会覆盖已有真实启动证据；`api_event`、空来源和 `unknown` 可用于兼容历史、补录和排障，但不会单独证明物理 PXE 成功。
- `POST /api/v1/system/lab-validation/run` 需要 `X-Confirm-Action: system.lab-validation.run`，默认执行 PXE HTTP/DHCP/TFTP 只读探测，并对最近 20 个 BMC 端点和 SSH Access 执行安全检查，可用 `{ "strict": true, "check_pxe": true, "check_bmc": true, "check_ssh": true, "limit": 20, "server_ids": [1,2], "pxe_macs": ["52:54:00:aa:bb:cc"], "pxe_probe_mac": "52:54:00:00:00:fe", "pxe_arch": 9, "ssh_probe_command": "printf 'ok '; hostname; id -un; uname -srm" }` 调整。`strict=true` 会启用物理验收闸门：PXE 检查必须提供真实 `pxe_macs`，BMC/SSH 检查必须提供真实 `server_ids`；BMC 是可选能力，只有请求目标配置了 BMC Endpoint 时才要求 `BMC_ADAPTER=redfish|ipmi` 和物理 BMC proof，未配置 BMC Endpoint 的目标会把 BMC 项记为 skipped。严格 BMC 检查会在连通性通过后执行只读固件/身份探针：Redfish 读取 Systems/Managers，IPMI 读取 `mc info`；批次结果会记录 `adapter=redfish|ipmi`、厂商、厂商 ID、型号、产品 ID、设备 ID、设备修订、序列号和版本摘要，并把这些非敏感身份字段写入 run result `details`，且至少需要一个身份字段才能通过。SSH 检查会在真实主机上执行单行探针命令，默认使用安全只读的 hostname/user/kernel 探针，批次结果会记录 stdout/stderr/exit_code 摘要，并把 command、exit_code、stdout、stderr 和错误摘要写入 run result `details`，便于证明不是仅做 TCP 连接。PXE HTTP 探测会带 `X-Lab-Validation-Probe: 1`，即使请求源 IP 位于部署网段，也只会记录为 `api_event`，不会作为物理 PXE 启动证据。当严格运行同时检查 PXE 和 SSH 且指定 `server_ids` 时，每台目标还会生成 `full_chain_target` 结果；有 BMC Endpoint 的资产需要新鲜 PXE BootEvent、BMC ok、SSH ok 和引用完整的新鲜物理证据，未配置 BMC Endpoint 的资产需要新鲜 PXE BootEvent、SSH ok 和引用完整的新鲜物理证据。`ok` full-chain 证据必须引用一次最近完成的严格 PXE+SSH `run_id`，且该运行包含目标 PXE/SSH 成功结果；如果目标有 BMC Endpoint，该运行还必须检查 BMC 并包含目标 BMC 成功结果。`server_ids` 用于把 BMC/SSH 检查限定到指定真实资产；`pxe_macs` 用于校验指定真实 PXE 客户端是否已经产生启动事件；`pxe_arch=0` 可验证 Legacy BIOS bootfile，`7/9/11` 验证常见 UEFI x86_64 bootfile；最大 50 个资产、20 个 PXE MAC。每次运行都会生成 `run_id`，持久化批次、目标、请求人、Request ID 和单项结果，便于把真实实验室执行记录和审计日志对应起来。
- 当严格运行同时传入 `server_ids` 和 `pxe_macs` 时，PXE BootEvent 不再只是全局按 MAC 通过：事件必须直接带有请求中的 `server_id`，或事件 MAC 必须匹配请求资产库存中的 `primary_mac`。这可以防止把同一实验网中另一台机器的启动事件误计入当前目标。
- `GET /api/v1/system/lab-validation/runs/{id}/evidence-bundle` 返回某次运行的只读验收包，包含环境快照、检查项、现场检查清单、单项结果、目标矩阵、关联 BootEvent、BMC Endpoint、SSH Access、真实终端会话 transcript、相关物理证据和配置/运行问题，适合实验室交付和复核。现场检查清单会逐项标出 PXE BootEvent、BMC 身份探针、SSH 命令证明和 full-chain 证据引用的完成状态、下一步动作和阻塞原因；BMC 清单项在目标没有 BMC Endpoint 时会标记为 `skipped` 且不阻断 full-chain 候选，有 BMC Endpoint 时只有本次运行结果成功且 `details` 含结构化身份字段才标记为 `ok`；SSH 清单项只有在 `details` 含 `host_key_policy=known_hosts`、`host_key_verified=true`、`host_key_sha256`、command/exit_code/stdout 命令证明时才标记为 `ok`。验收包本身不会自动创建 `ok` 证据；系统管理页可从验收包清单行预填 PXE/BMC/SSH 单项证据，且在同一资产 PXE、SSH 均为 `ok` 且 BMC 为 `ok` 或 `skipped` 时预填 full-chain 证据，提交后仍由 `POST /api/v1/system/lab-validation/evidence` 做真实引用校验。
- `POST /api/v1/system/lab-validation/evidence` 需要 `X-Confirm-Action: system.lab-validation.evidence`，用于把真实 PXE 客户端启动、物理 Redfish/IPMI 检查、真实 SSH 主机检查或全链路实验结果记录为非敏感证据摘要。`subject` 和 `summary` 必填；`subject` 建议填写 MAC、BMC endpoint、SSH host 或实验批次，PXE 证据会按常见 MAC 格式做规范化比对。可传 `run_id` 关联一次已完成且 7 天内的验收批次；`ok` BMC、SSH 和全链路证据必须传 `run_id`。BMC/SSH 单项证据引用的批次必须是最近完成的严格验收运行，并分别包含目标 `adapter=redfish|ipmi` 的结构化 BMC 身份 proof `details`，或 SSH known_hosts host key proof (`host_key_policy=known_hosts`、`host_key_verified=true`、`host_key_sha256`) 加 command/exit_code/stdout proof `details`；`ok` 全链路证据引用的批次必须为最近完成的严格 PXE+SSH 验收运行，并包含目标 PXE BootEvent 和 SSH 成功结果及上述结构化 proof；如果目标配置了 BMC Endpoint，该批次还必须检查 BMC 并包含目标 BMC 成功结果。常见闭环是：第一次严格运行完成真实 PXE/SSH 单项验证，但 `full_chain_target` 因缺少 full evidence 为 `error`；补录 full evidence 后再次执行严格运行，`full_chain_target` 才会通过。`ok` 证据必须绑定平台内真实引用：PXE 证据传 7 天内且 `source=http_ipxe|pxe_dhcp` 的 `boot_event_id`，其中 `http_ipxe` 必须由部署网段内客户端 IP 自动分类产生；如传 `server_id`，BootEvent 可通过 `server_id` 或 MAC 匹配库存 `primary_mac` 证明同一资产；BMC/SSH 证据传 `server_id` 并要求对应最近检查为 `ok`，且 BMC Endpoint 类型匹配当前 `BMC_ADAPTER`，生产 Redfish Endpoint 必须使用 `https://`，生产 SSH 验证必须使用 `SSH_HOST_KEY_POLICY=known_hosts` 和可读 `SSH_KNOWN_HOSTS_PATH`；全链路证据传同资产的 `server_id` 与真实链路 `boot_event_id`，可用 BootEvent `server_id` 或 MAC 对 `primary_mac` 的匹配证明同资产，并要求 SSH 最近检查为 `ok`；配置了 BMC Endpoint 的目标还要求 BMC 最近检查为 `ok`。`physical_evidence` 只把 7 天内且引用完整的 `ok` 证据计入通过，历史证据仍展示但不证明当前实验室状态。不要写入明文密码、私钥、完整 token 或厂商控制台会话 cookie。
- `tools/configure-physical-target.ps1` 是命令行真实目标配置辅助脚本。它会登录 API，按 `-ServerId` 查找现有资产，或按 `-AssetNo`/`-PXEMac` 查找并在不存在时创建资产，然后按参数写入 Redfish/IPMI BMC Endpoint 和 SSH Access；没有 BMC 的机器可传 `-SkipBMC`，仍能通过 PXE+SSH 验收。管理员密码、BMC 密码、SSH 密码或私钥分别从 `BAREMETAL_ADMIN_PASSWORD`、`BAREMETAL_BMC_PASSWORD`、`BAREMETAL_SSH_SECRET` 读取，也可通过 `-PasswordEnvVar`、`-BMCPasswordEnvVar`、`-SSHSecretEnvVar` 改名，避免明文进入命令行历史。示例：`pwsh -File .\tools\configure-physical-target.ps1 -BaseUrl https://baremetal.example.com -Email admin@example.com -AssetNo BM-RACK1-001 -Hostname rack1-node1 -PXEMac "52:54:00:aa:bb:cc" -BMCEndpoint https://192.168.10.21 -BMCUsername root -SSHHost 192.168.20.21 -SSHUsername root -ValidateNow -RecordEvidence -RecordFullEvidence`。设置 `-ValidateNow` 后脚本会继续调用 `tools/physical-validation.ps1`，对同一 `server_id` 和 `PXEMac` 执行严格 PXE+SSH 验收；若配置了 BMC Endpoint，则同一验收也会要求 BMC proof。脚本仍不会启停 DHCP/TFTP，也不会执行电源动作。
- `tools/physical-validation.ps1` 是命令行现场验收辅助脚本。它会检查 `/readyz`，登录后执行严格 PXE+SSH 验收，并在目标配置了 BMC Endpoint 时同时要求 BMC proof，最后导出 `readyz`、run result 和 evidence bundle JSON。默认交互读取管理员密码；自动化验收可设置 `BAREMETAL_ADMIN_PASSWORD`，或用 `-PasswordEnvVar` 指向其他环境变量，避免把密码写入命令行历史。示例：`pwsh -File .\tools\physical-validation.ps1 -BaseUrl http://127.0.0.1:8080 -Email admin@example.com -ServerId 1 -PXEMac "52:54:00:aa:bb:cc" -OutDir .\lab-validation-output`。多目标验收支持 PowerShell 数组或逗号分隔字符串，例如 `-ServerId 1,2,3 -PXEMac "52:54:00:aa:bb:01,52:54:00:aa:bb:02"`，便于 `pwsh -File`、CI 和远程执行器调用。默认脚本只读运行；显式追加 `-RecordEvidence` 时，脚本会从 evidence bundle 的 `operator_checklist` 或后端 `evidence_candidates` 中筛选状态为 `ok` 且属于本次 `-ServerId`/`-PXEMac` 的 PXE BootEvent、BMC identity 和 SSH command 清单项，分别调用受保护 evidence API 记录 `kind=pxe|bmc|ssh` 单项证据，导出 `*-item-evidence.json`。PXE 单项证据会反查 bundle 内 BootEvent 的真实 MAC 作为 `subject`，避免把资产号误写成 PXE subject；BMC/SSH 单项证据会携带本次严格运行的 `run_id` 和目标 `server_id`，继续受后端物理 proof 校验约束。显式追加 `-RecordFullEvidence` 时，脚本会筛选本次请求范围内 PXE BootEvent 和 SSH command 为 `ok`、且 BMC identity 为 `ok` 或 `skipped` 的目标，调用受保护 evidence API 记录 `kind=full` 证据，并自动复跑一次严格验收，导出 `*-full-evidence.json` 和 `*-rerun-*` 文件。两个开关可以一起使用。脚本不启停 DHCP/TFTP，也不执行电源动作；如果 `/readyz` 为 degraded，默认会停止，可显式传 `-AllowDegradedReadyz` 继续做排障运行。
- BMC 验收只调用当前 `BMC_ADAPTER` 的连通性检查，不执行开机、关机或重启；当 `BMC_ADAPTER=simulated` 时会跳过物理 BMC 检查，避免把模拟结果误判为真实通过。真实 Redfish/IPMI 模式下，BMC Endpoint 的 `type` 必须匹配当前 `BMC_ADAPTER`，Redfish `protocol` 必须与 URL scheme 一致，生产 Redfish Endpoint 必须使用 `https://`，否则检查、目标矩阵和物理证据都会失败。BMC 检查失败时 API 会返回 partial `proof.stage`：`config` 表示适配器、端点、协议、凭据等本地配置不满足要求，`connectivity` 表示已经进入 Redfish/IPMI 连通性或协议执行阶段但失败。`/readyz` 和真实验收报告都会返回 `bmc_tooling` 检查项；`BMC_ADAPTER=ipmi` 时必须能在 PATH 中找到 `ipmitool`。
- SSH 验收使用保存的 SSH 配置和加密凭据执行轻量只读命令，成功后把 SSHAccess 标记为 `ok`，失败标记为 `error`；run result `details` 会记录非敏感的 `stage`、`host_key_policy`、`host_key_verified`、`host_key_algorithm`、`host_key_sha256`、command、exit_code、stdout/stderr 摘要。`stage=config` 常见于 known_hosts/凭据配置错误，`stage=dial` 表示 TCP 未连通，`stage=handshake` 常见于 host key 不匹配或认证握手失败，`stage=command` 表示命令已执行但退出非 0。生产环境必须使用 `SSH_HOST_KEY_POLICY=known_hosts` 和可读、可解析且非空的 `SSH_KNOWN_HOSTS_PATH`；如果仍使用 `insecure_ignore`，历史 `SSHAccess.status=ok`、严格 SSH run 和 `ok` 级 SSH/full 证据都会被标记为不满足真实主机证明策略。
- PXE 验收不会动态启用 DHCP/TFTP；执行检查会请求 `BOOT_BASE_URL/boot/ipxe`，在 `BOOT_SERVICES_ENABLED=true` 且模式不是 `external` 时向当前监听地址发送合成 PXE DHCPDISCOVER 校验 bootfile 响应，并在 `BOOT_SERVICES_ENABLED=true` 时通过 UDP TFTP 拉取 `boot.ipxe`。合成 DHCP 探测带内部 marker，listener 会返回并报告 option 54 `server_identifier`、BOOTP `next_server`(`siaddr`)、option 66 `tftp_server_name`、option 67 和 BOOTP file，不写入 `BootEvent`，避免污染物理启动证据。真实物理启动证据仍来自 `BootEvent`，需要物理客户端经 DHCP/TFTP/iPXE 访问平台后才会出现。

## PXE/iPXE

- 后端 `PXEService` 负责 DHCP/ProxyDHCP 响应、TFTP 文件和动态 iPXE chain 脚本。
- 大文件下载走 HTTP，TFTP 只承载启动加载器和最小脚本。
- 默认不启动 UDP 监听；设置 `BOOT_SERVICES_ENABLED=true` 后才启用。`BOOT_SERVICE_MODE=proxy` 提供 ProxyDHCP 响应，`builtin` 提供内置 DHCP 地址分配，`external` 只启动 TFTP，供外部 DHCP 使用。
- 启用时必须配置 `BOOT_BIND_INTERFACE`、`BOOT_DHCP_SERVER_IP`、`BOOT_TFTP_ROOT`、`BOOT_TFTP_BOOTFILE_UEFI`、`BOOT_TFTP_BOOTFILE_BIOS`；生产环境还必须把 `BOOT_DHCP_LISTEN_ADDR`、`BOOT_TFTP_LISTEN_ADDR` 显式绑定到部署网接口 IP，例如 `192.168.100.10:67` 和 `192.168.100.10:69`，避免全接口监听；`builtin` 模式还必须设置 `BOOT_DHCP_LEASE_START` 和 `BOOT_DHCP_LEASE_END`。
- TFTP 只服务 `BOOT_TFTP_ROOT` 内文件，并内置 `boot.ipxe`、`auto.ipxe`、`default.ipxe`，链到 `GET /boot/ipxe`。真实启动加载器如 `ipxe.efi`、`undionly.kpxe` 需要由部署环境放入 TFTP 根目录。TFTP RRQ 支持常见 PXE/iPXE options：`blksize` 会限制传输块大小，`timeout` 会协商 ACK 等待时间，`tsize` 会返回文件大小。
- DHCP/ProxyDHCP 响应会同时写入 option 54 server identifier、BOOTP `siaddr` next-server、option 66 TFTP server name、option 67 bootfile 和传统 BOOTP `file` 字段，UEFI 默认返回 `BOOT_TFTP_BOOTFILE_UEFI`，Legacy BIOS 默认返回 `BOOT_TFTP_BOOTFILE_BIOS`。`/readyz` 和严格验收的 DHCP 探针会在响应中报告 `server_identifier`、`next_server`、`tftp_server_name` 和 `legacy_bootfile`，用于排查读取不同 PXE 引导字段的物理固件。
- `/readyz` 的 `pxe_services` 检查会报告服务启用状态、TFTP 根目录探针和启动加载器文件缺失 warning。
- `GET /boot/ipxe` 和 `POST /boot/events` 仍是 DHCP/ProxyDHCP/TFTP 之外的 HTTP 对接边界；`/boot/ipxe` 只有当客户端 IP 位于已启用 `deployment` 网络 CIDR 内且不是内部验收 HTTP 探测时才把 BootEvent 记为 `http_ipxe`，否则记为 `api_event`。
- 未知 MAC 请求 `/boot/ipxe` 或上报 `/boot/events` 时，平台会自动创建 `discovered` 资产和 `boot.discovery` 状态历史，后续由运维人员补充归属、位置、BMC 和用途。
- `BOOT_BASE_URL` 控制 iPXE、镜像、metadata 和安装器脚本中使用的平台地址。
- 平台提供 `/boot/discovery.ipxe` 和 `/boot/linux-installer.ipxe`，不再依赖占位安装器 URL。
- iPXE 安装脚本会暴露 `image-url={BOOT_BASE_URL}/images/{image_id}/file`，该接口仅分发已启用且已校验的镜像，并由 Go `http.ServeFile` 支持 Range 请求。

## 网络配置

- `/api/v1/network-configs` 记录管理网、部署网和业务网的 CIDR、网关、DNS、VLAN、DHCP 模式和 ProxyDHCP 开关。
- 网络配置写入时会校验 CIDR、网关归属、DNS IP 列表、VLAN 0-4094、用途、状态和 DHCP 模式枚举，避免无效部署网进入后续安装流程。
- `POST /api/v1/network-configs/{id}/check` 提供只读网络配置检查，覆盖格式、启用网段重叠、网关、DNS、状态、DHCP/ProxyDHCP 模式、当前 PXE/DHCP/TFTP runtime 配置匹配情况、runtime 地址与部署网 CIDR 一致性和部署网可用性。该检查不会启动或停止 DHCP/TFTP，也不会对真实网关或 DHCP 服务做破坏性探测。
- 真实 DHCP/TFTP 只由启动配置控制，不会因为创建网络配置而自动启用，避免在开发机或办公网误启动。
- 创建部署前要求至少存在一个启用的 `deployment` 网络配置；部署请求可传 `network_id` 显式绑定启用的部署网络，未传时保留旧的自动选择兼容行为。
- 生产启用内置 DHCP 前，应基于这些配置执行 DHCP 冲突检测、网关连通性测试和 VLAN 隔离验证。

## Metadata API

- 为部署任务生成 24 小时有效的 metadata token；过期后再次确保 token 时会自动轮换。
- 设置 `METADATA_REQUIRE_DEPLOYMENT_NETWORK=true` 后，Metadata API 只允许来自已启用 `deployment` 网络 CIDR 的客户端访问；生产环境必须开启该限制，开发/测试默认关闭以便本地演示。
- 用户数据中不得返回明文密码。
- MVP 已提供 `/metadata/by-token/{token}/instance-id`、`hostname`、`network`、`ssh-keys`、`userdata` 和 `/userdata/by-token/{token}`，部署 iPXE 脚本默认使用 token URL。
- 无 ID 的 `/metadata/instance-id`、`/metadata/hostname`、`/metadata/network`、`/metadata/ssh-keys`、`/metadata/userdata` 和 `/userdata` 会按客户端 IP 匹配服务器，适合 Cloud-init 风格本机访问。
- `/metadata/by-server/{id}/...`、`/metadata/by-mac/{mac}/{field}`、`/metadata/by-ip/{ip}/{field}`、`/metadata/by-deployment/{id}/{field}` 以及对应 `/userdata/...` 别名作为发现环境、本地演示和兼容路径保留；真实安装环境应优先使用 token 路径，避免枚举 server ID。
- `/metadata/.../network` 会输出服务器主网卡、部署任务绑定部署网的 CIDR、网关、DNS、VLAN 和 DHCP/ProxyDHCP 字段；旧部署未绑定网络时回退到最新启用部署网，供安装环境渲染网络配置。
- `/metadata/.../ssh-keys` 从部署变量 `ssh_authorized_keys`、`ssh_keys` 或 `ssh_public_key` 提取公钥并去重；未配置时返回空数组，禁止把明文密码放入 userdata 或 metadata。
- 每次成功访问 Metadata API 都会写入 `LogEvent`，`source=metadata`；被部署网段限制拒绝的访问会写入 warning 日志；日志记录端点、访问模式、客户端 IP、部署 ID 和 `X-Request-ID`，不得记录 metadata token。

## 安装模板与工作流模板

- `InstallTemplate` 保存 Kickstart、Autoinstall、Cloud-init、Unattend.xml 等模板内容和变量 schema。
- `WorkflowTemplate` 使用 JSON `steps` 定义顺序工作流，MVP 工作流执行器会按模板步骤生成 TaskExecution。
- 模板写入时后端会校验安装模板类型、启停状态、变量 schema JSON object，以及工作流 `definition.steps[].name/action`，防止坏模板进入部署链路后才降级。
- 部署任务支持 `template_id`、`workflow_id`、`network_id`、`variables`、`erase_policy` 和 `erase_confirmed`；创建部署必须记录磁盘擦除/重装确认，真实执行器接入后也不得绕过该记录。
- 没有 BMC Endpoint 的实体机仍可部署：预检会给出人工开机/PXE 操作项，workflow 会等待真实 `http_ipxe`/`pxe_dhcp` BootEvent、metadata token/endpoint 访问，以及 SSH 检查或 `ssh/full` 物理证据；部署详情会把当前等待项显示为现场动作，API 重启时会恢复 pending 的无 BMC 物理 PXE workflow，不需要重新创建部署任务。
- 批量部署入口单批最多 20 台，采用全通过才创建的 preflight 语义；同批次可绑定同一个 `network_id`。真实执行器接入后如支持更大批次，应继续保留批次上限、去重、逐目标 BMC/镜像/模板/网络检查和二次确认。
- 部署工作流受 `DEPLOYMENT_CONCURRENCY` 控制并具备 pending 排队语义；真实队列或外部执行器接入时应保留同等并发上限和可观测状态。
- 失败或已取消部署可通过 `POST /api/v1/deployments/{id}/retry` 重新入队，真实执行器接入时应继续复用原部署上下文、擦除策略确认记录、重新执行 preflight、创建新的 workflow run，并保留 `X-Confirm-Action: deployment.retry` 与高风险审计。
- 部署日志接口会返回 `summary`、最新 `workflow`、所有 `runs` 尝试历史和最新 run 的 `tasks`，真实执行器接入时应继续写入 `started_at`、`finished_at`、`stdout/stderr/error_message`，让前端保留步骤耗时、失败原因和重试历史展示。
- Metadata/Userdata 会合并服务器字段、部署变量和 metadata token，并渲染安装模板中的 `{{hostname}}`、`{{primary_ip}}`、`{{metadata_token}}` 等变量。
- Demo seeder 默认提供 Ubuntu Server 24.04 Autoinstall、Rocky Linux 9 Kickstart 和 Debian 12 Preseed 三类模板，用于验证 Phase 1 的多 Linux 安装模板边界。

## Redfish/IPMI

- service 层提供 `BMCAdapter` 接口，默认启用 `SimulatedBMCAdapter`；生产或实验室硬件验证可设置 `BMC_ADAPTER=redfish` 或 `ipmi`。
- 设置 `BMC_ADAPTER=redfish` 后，`RedfishBMCAdapter` 会使用 BMC 端点、用户名和加密凭据，通过 HTTP Basic Auth 调用 `/redfish/v1`，并从 `/redfish/v1/Systems`、`/redfish/v1/Managers` 集合发现真实 system/manager 资源。开机、关机、重启分别映射为 Redfish `On`、`ForceOff`、`ForceRestart`，Reset URL 优先使用资源里的 `Actions.#ComputerSystem.Reset.target`，固件查询从 ComputerSystem 和 Manager 资源读取厂商、型号、序列号、BIOS 版本和 BMC 固件版本。Redfish Endpoint 保存时要求 `protocol` 与 URL scheme 一致；`http://` 仅用于开发/实验室 mock 或隔离自签验证，生产环境会拒绝 `http://` Redfish Endpoint。`REDFISH_INSECURE_TLS=true` 只用于开发/实验室自签证书；生产环境使用 Redfish 时启动校验要求 `REDFISH_INSECURE_TLS=false`，并应把 BMC 管理口证书或 CA 加入运行环境信任链，也可通过 `REDFISH_CA_CERT_PATH` 指向 PEM CA bundle。
- 设置 `BMC_ADAPTER=ipmi` 后，`IPMICommandAdapter` 会调用系统 `ipmitool -I lanplus -H <host> -U <user> -E`，通过 `IPMI_PASSWORD` 环境变量传递解密后的密码，支持 `host:port` 端点。电源查询和控制分别映射为 `power status`、`power on`、`power off`、`power reset`，固件查询使用 `mc info` 解析 Device ID、Device Revision、Manufacturer ID、Product ID、BMC 固件版本、厂商和产品名。容器或主机运行环境需要预装 `ipmitool`，并可通过 `/readyz` 的 `bmc_tooling` 在上线前确认。
- BMC 配置写入会校验 Redfish URL、IPMI host/host:port 和协议组合，保存或更新后状态为 `unknown`；创建部署前会调用当前 `BMC_ADAPTER` 执行连通性检查，成功时标记为 `ok`，失败时阻止部署创建并把 Endpoint 状态标记为 `error`。真实适配器只接受同类型端点，例如 `BMC_ADAPTER=redfish` 只检查 `type=redfish`，`BMC_ADAPTER=ipmi` 只检查 `type=ipmi`。手动 `check` 和电源操作仍可用于日常验证与排障。
- BMC 配置、连通性检查和电源变更会拒绝 `retired`/`scrapped` 资产；电源状态和固件信息查询保持只读可用，用于终态资产排障与盘点核对。真实 Redfish/IPMI 接入时必须保留该生命周期保护。
- 批量电源接口为 `POST /api/v1/servers/bmc/batch-power`，单次最多 50 台，按目标返回成功/失败明细，并对每台目标写入高风险审计。真实接入时可在 service 层增加并发池，但必须保留重复目标校验、终态资产拒绝、二次确认和逐目标结果。
- Demo seeder 只创建模拟 BMC 端点，不写入可用凭据引用；启用真实 Redfish 前必须在前端重新保存 BMC 密码。
- BMC 密码通过 `CredentialService` 使用 AES-GCM 加密，密钥来自 `CREDENTIAL_KEY`。
- BMC 配置必须绑定已存在资产；`redfish` 端点只允许 `http/https` 协议，`ipmi` 端点只允许 `protocol=ipmi`。
- 固件升级、批量关机、批量重启必须保留二次确认和高风险审计。

## 资产退役/报废与数据擦除

- `POST /api/v1/servers/{id}/retire` 会把资产置为 `retired`，并记录 `RetirementRecord`、状态历史、高风险审计和 `source=lifecycle` 的日志事件。
- `POST /api/v1/servers/{id}/scrap` 会把资产置为 `scrapped`，并记录 `to_status=scrapped` 的同类终态记录；已退役资产可继续报废，已报废资产重复报废保持幂等。
- `DELETE /api/v1/servers/{id}` 仅用于清理无业务引用的误建资产，需要管理员角色和 `X-Confirm-Action: server.delete`；真实 CMDB 同步接入后仍应保留引用检查，常规下线继续走退役/报废。
- MVP 只记录终态原因、擦除状态、擦除方式和证据，不在开发机执行真实磁盘擦除；`erase_status=verified` 要求提供擦除方式和证据。
- 后续接入真实擦除执行器时，应把擦除任务和证据回写到同一终态记录或其扩展表，保留 `X-Confirm-Action: server.retire/server.scrap`、操作者、时间、审计和不可对 `scrapped` 资产回退到 `retired` 的保护。

## SSH Agentless 采集

- `POST /api/v1/servers/{id}/ssh` 可保存 SSH Agentless 采集目标，password/private key secret 通过 `CredentialService` 加密保存。
- SSH 配置、单机采集和指标查询必须绑定已存在资产；SSH 配置和单机采集会拒绝 `retired`/`scrapped` 资产；SSH host 必须是合法主机名或 IP，端口限制为 1-65535，`auth_type` 支持 `password` 和 `private_key`。
- `POST /api/v1/servers/{id}/ssh/check` 使用保存的 SSH 配置连接真实主机并执行轻量只读命令，成功后把 SSHAccess 状态标记为 `ok`，失败标记为 `error`，适合作为真实主机验收第一步。
- `COLLECTOR_MODE=simulated` 时写入模拟指标；`COLLECTOR_MODE=ssh` 时通过内置 Go SSH 执行器连接真实主机，执行只读采集命令并解析 `host_up`、CPU、内存、磁盘、网络收发、进程数量和僵尸进程数量指标，单次采集默认 30 秒超时。
- 指标查询和告警评估只读取 7 天保留窗口内的样本；采集成功后会清理超过 7 天的历史指标，避免长期运行后指标表无界增长。
- 内置 SSH 执行器支持保存的 `password` 和 `private_key` 凭据；host key 策略由 `SSH_HOST_KEY_POLICY` 控制，默认 `insecure_ignore` 仅适合开发/演示。生产环境启用 `COLLECTOR_MODE=ssh` 或 `SSH_OPERATIONS_MODE=ssh` 时，启动校验要求 `SSH_HOST_KEY_POLICY=known_hosts`，并通过 `SSH_KNOWN_HOSTS_PATH` 指向可读、可解析且非空的标准 known_hosts 文件。`/readyz` 和实验室验收报告会把当前 SSH Access 目标与 known_hosts pattern 做静态覆盖检查，支持非哈希条目和 `ssh-keyscan -H` 生成的 OpenSSH 哈希条目；真实 SSH 握手仍由 Go SSH known_hosts verifier 校验主机密钥。
- SSH 凭据必须加密保存，日志中不得输出私钥、密码和 token。

## 日志事件接入

- MVP 已提供 `GET /api/v1/log-events` 查询归一化日志事件，前端在“监控告警”的“日志事件”页签展示，并会记录 Metadata API 访问事件。
- `LogEvent` 用于承载 syslog、journald、BMC SEL、工作流执行器和 Agent 日志的统一索引字段：`server_id`、`source`、`level`、`message`、`trace_id` 和 `occurred_at`。
- `POST /api/v1/ops/log-collections` 默认用模拟方式为目标资产生成 `syslog`、`dmesg`、`hardware` 三类日志事件；设置 `SSH_OPERATIONS_MODE=ssh` 后会通过 Go SSH 从真实主机读取 syslog/journald、`dmesg` 和基础硬件摘要，保留 `X-Confirm-Action: ops.logs.collect` 和审计边界。
- 后续也可通过 syslog receiver、轻量 agent 或消息队列写入 `LogEvent`，原始大日志建议存对象存储或日志系统，平台只保存索引、摘要和跳转引用。
- 写入前必须做敏感信息脱敏，不得落库 BMC 密码、SSH 私钥、JWT、metadata token 或安装阶段临时凭据。

## 告警规则

- MVP 已提供 `/api/v1/alert-rules` 管理基础阈值规则，并通过 `POST /api/v1/alert-rules/evaluate` 对 7 天保留窗口内的最新指标做一次手动评估。
- Demo seeder 默认启用 `cpu.high`、`memory.high`、`disk.full`、`disk.smart.warning`、`host.offline` 五类基础规则，覆盖 CPU 高、内存高、磁盘满、磁盘 SMART 异常和离线告警。`disk_smart_health=0` 表示健康，`1` 表示异常；真实采集接入时可由 `smartctl`、厂商 RAID 工具或 BMC SEL 归一化写入该指标。
- 当前规则评估只读取 7 天保留窗口内每台服务器每个指标的最新样本，并避免重复创建同一服务器同一规则的未关闭告警。
- 告警规则写入时会校验规则 ID、指标名、操作符、级别和状态，避免无效规则静默存在但永不触发。
- 生产接入时可把评估逻辑迁移到定时任务或队列消费者，增加持续时间窗口、抑制、静默、通知渠道和自愈动作。
- 告警规则变更和评估均应保留审计记录，避免阈值被静默调高后绕过告警。

## 运维工具与批量脚本

- MVP 已提供 `POST /api/v1/ops/script-jobs`、`GET /api/v1/ops/script-jobs` 和结果查询接口。
- 当前批量脚本默认使用模拟执行器，只生成每台服务器的执行记录和审计；设置 `SSH_OPERATIONS_MODE=ssh` 后会通过 Go SSH 在真实目标主机执行脚本。
- 创建批量脚本任务时会校验目标资产存在、未退役/报废、无重复，单次最多 100 台，并发上限 50，超时上限 3600 秒。
- 执行器按 `concurrency` 分批推进每台服务器的执行状态，保留 `pending`、`running`、`success`/`failed` 状态边界；SSH 模式会复用 `SSHAccess` 与加密凭据，记录 stdout、stderr 和 exit code。
- 生产使用可继续增加命令白名单、输出脱敏、跳板机和更严格 host key 校验。
- 批量脚本属于高风险操作，必须保留 `X-Confirm-Action: ops.script.create` 二次确认。

## 远程终端 / WebSSH

- MVP 已提供 `POST /api/v1/ops/terminal-sessions`、`GET /api/v1/ops/terminal-sessions`、详情查询和关闭接口。
- 当前终端会话默认是模拟模式，只生成 session 记录和 transcript；设置 `SSH_OPERATIONS_MODE=ssh` 后，打开会话会通过 Go SSH 验证真实主机，并允许通过 `POST /api/v1/ops/terminal-sessions/{id}/commands` 执行命令，命令、输出、stderr、错误和 `exit_code` 都会追加到 transcript，便于作为真实 SSH 主机操作证据。
- 打开会话前会校验目标资产存在且未退役/报废，并限制 `reason` 不超过 255 字符。
- 打开、执行命令和关闭终端都属于高风险操作，必须保留 `X-Confirm-Action: ops.terminal.open`、`ops.terminal.command` 和 `ops.terminal.close`。
- 完整 WebSSH/PTTY 接入应通过后端会话代理建立短时授权，复用 `SSHAccess` 与加密凭据，强制 RBAC、空闲超时、最大会话时长、来源 IP 记录、完整 transcript/录屏审计和敏感输出脱敏。
- 前端只应拿到会话 ID 和 WebSocket 入口，不应暴露 SSH 密码、私钥或 BMC 凭据。

## 备份与恢复

- MVP 已提供 `GET /api/v1/ops/backup/export` 导出 JSON 备份，需管理员角色和 `X-Confirm-Action: ops.backup.export`。
- 导出数据覆盖租户、资产、硬件、镜像、模板、部署、验收运行批次与结果、工作流、指标、日志事件、采集任务、运维任务、告警和审计日志。
- MVP 已提供 `POST /api/v1/ops/backup/validate` 做恢复前 dry-run 预检，校验备份 schema、版本、引用完整性、验收运行历史引用、租户服务器配额、部署网络、告警规则和目标库是否已有数据，不写入数据库。
- MVP 已提供 `POST /api/v1/ops/backup/restore` 执行受保护恢复，需 `X-Confirm-Action: ops.backup.restore`，仅允许 fresh 目标库。普通恢复用户会写入不可登录占位密码并要求后续重置；执行恢复的当前管理员账号会保留目标环境中的现有密码作为恢复入口；恢复后会修正 PostgreSQL/SQLite 自增序列。
- 默认不导出 `credentials`、`ssh_accesses`、BMC 端点、启动事件、物理证据记录和 metadata token，避免凭据密文、管理网端点、账号信息和短期运行态数据进入普通备份包；非敏感的验收运行批次与结果会导出，帮助恢复环境保留实验室验收审计脉络。这些运行态/敏感表如果在目标库已有数据，也会让恢复预检/恢复执行判定目标库不 fresh。
- 生产恢复仍建议优先使用离线工具或维护窗口执行；恢复前必须校验备份版本、schema、租户边界、目标环境为空或显式覆盖，并生成恢复审计报告。
- PostgreSQL 生产环境仍应配置数据库级备份和 PITR；平台 JSON 备份只用于演示、迁移预检查和小规模配置快照。

## 多租户基础边界

- MVP 已提供 `/api/v1/tenants` 管理租户台账，服务器资产通过 `tenant_id` 归属到租户。
- 租户写入时后端校验 `tenant_id` 标识符、`active/disabled` 状态枚举和 quota JSON object，数值型 quota 不能为负，`quota.servers` 必须为非负整数且不能低于当前资产数。
- 资产创建、迁入和批量导入时会校验非空 `tenant_id` 必须对应存在且 `active` 的租户，并强制执行 `quota.servers` 服务器资产数上限；发现阶段资产可暂时不归属租户。
- 当前租户用于资产归属、筛选、基础配额和备份恢复边界标识，不强制执行跨租户数据隔离或计费。
- 后续多租户隔离应把用户与租户成员关系、资源查询过滤、审计租户字段和配额检查统一接入 middleware/service 层，避免只在前端隐藏数据。
- 配额字段当前作为 JSON 快照保存，并已对 `quota.servers` 做强制校验；生产接入时应继续定义更完整 schema，并在部署任务、成本计量和跨租户访问控制中统一执行。

## CMDB 与硬件 BOM

- 资产写入时会校验生命周期状态、架构、IP/MAC 格式和 `asset_no`/`hostname`/`primary_mac` 唯一性；MAC 会规范为小写冒号格式，PXE 启动请求也会做同样归一化以避免重复发现。
- `POST /api/v1/servers/{id}/inventory` 可接收人工录入、发现环境回传或 SSH Agentless 采集归一化后的硬件盘点快照；`raw_payload` 必须是 JSON object，便于后续字段演进。
- `GET /api/v1/servers/{id}/bom`、`/bom.csv` 和 `GET /api/v1/bom.csv` 会把服务器资产字段与最新硬件快照合并为 BOM，用于 CMDB 对账和采购/维保导出。
- 真实采集器接入时应优先写入结构化摘要字段，同时将厂商原始返回保存在 `raw_payload`，避免后续字段扩展丢失证据。
- BOM 导出不返回任何 BMC、SSH 或安装凭据。
