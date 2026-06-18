# MVP Acceptance Audit

Date: 2026-06-18

This audit maps the design document MVP scope and acceptance criteria to the current implementation. The project is considered functionally complete for the local demo/MVP boundary: deployable, demonstrable, documented, and testable without enabling real DHCP/TFTP on the development machine. Real PXE, hardware BMC, and SSH validation remain external lab-network acceptance activities.

## Phase 1 Scope

| Area | Status | Evidence |
| --- | --- | --- |
| PXE/iPXE boot | Complete | `/boot/ipxe`, `/boot/discovery.ipxe`, `/boot/linux-installer.ipxe`, `/boot/events`; unknown MAC creates `discovered` assets and boot logs. |
| ProxyDHCP / DHCP choice | Complete as config boundary | Network configs record `dhcp_mode` and `proxy_dhcp`; readiness/network checks do not start real DHCP/TFTP. |
| HTTP image service | Complete | Image upload/register/verify/download with HTTP Range and storage safety checks. |
| Asset CMDB | Complete | CRUD, import, lifecycle states, tags, tenant/owner/location/rack fields, status history, inventory and BOM export. |
| Linux install templates | Complete | Demo seeder and UI/API support Ubuntu Autoinstall, Rocky Kickstart, Debian Preseed. |
| Deployment workflow | Complete | Single/batch create, preflight, explicit deployment network, erase confirmation, concurrency queue, retry/cancel, logs and task duration. |
| Metadata API | Complete | Token, server, MAC, IP, deployment ID, and client-IP metadata paths; hostname, network, ssh-keys, userdata, access logs. |
| BMC | Complete | Simulated, Redfish, and IPMI adapters for power/status/firmware; credential encryption and audit. |
| Monitoring and alerts | Complete | Simulated/SSH collectors, core metrics, alert rules, evaluation, dedupe, ack/resolve events. |
| Ops tools | Complete as MVP simulation | Simulated WebSSH sessions with transcript/TTL audit, batch scripts with concurrency/timeout, log collection. |
| Users/RBAC/audit | Complete | Local users, admin/operator/viewer roles, confirmation headers, request IDs, audit log query/detail. |
| Docker Compose | Complete as documented package | Compose manifests and production config/readiness docs are present; Docker runtime was not available in this workspace. |

## Acceptance Criteria

| Criterion | Status | Notes |
| --- | --- | --- |
| PXE creates `discovered` asset | Complete | Covered by router tests and `/boot/ipxe` behavior. |
| Display MAC, architecture, CPU, memory, disk, NIC, serial | Complete | Asset fields plus `HardwareInventory`; UI supports manual snapshot and BOM export. |
| Asset state changes audited | Complete | Status history, lifecycle logs, and audit records. |
| Ubuntu/Rocky/Debian install paths | Complete for MVP simulator | Templates and workflow exist; real VM installs belong to lab E2E validation. |
| Deployment stages, duration, logs, failure reason | Complete | `/deployments/{id}/logs` returns summary, runs, tasks, `duration_ms`, stdout/stderr/error. |
| Successful deployment sets server `running` | Complete | Workflow service updates deployment/server states; tested. |
| Disk erase/reinstall confirmation | Complete | `erase_confirmed` plus `X-Confirm-Action` required for create/retry/cancel. |
| Redfish/IPMI status and power operations | Complete | Redfish mock and IPMI command adapter implemented; hardware validation is external. |
| BMC credentials masked | Complete | API responses omit plaintext and internal credential refs; secrets encrypted. |
| CPU/memory/disk/network/process metrics | Complete | Collector returns and stores core metrics with retention cleanup. |
| Offline/disk/CPU/memory alerts | Complete | Default alert rules and eval/ack/resolve flow implemented. |
| Login, asset, deployment, BMC, scripts audited | Complete | Audit coverage is tested across high-risk flows. |
| Unauthorized deployment/BMC denied | Complete | RBAC middleware and tests cover viewer/operator/admin boundaries. |
| High-risk actions confirmed | Complete | Confirmation middleware covers destructive/power/deploy/ops actions. |
| Health checks available | Complete | `/healthz` and `/readyz` implemented. |
| Restart persistence | Complete within configured storage | DB-backed state and Docker volume docs; runtime depends on configured database/storage. |

## Verification

- Backend tests: `..\.tools\go\bin\go.exe test ./... -count=1`
- Backend build: `..\.tools\go\bin\go.exe build -o baremetal-api.exe .\cmd\api`
- Frontend build: `npm run build`
- Local smoke: `/healthz`, `/readyz`, deployment-network UI, and metadata by MAC/IP routes.

## External Lab Checklist

These are intentionally not executed on the development machine:

- Boot a VM or test host through the lab PXE/ProxyDHCP path.
- Run Ubuntu, Rocky, and Debian installs against real installer media.
- Validate Redfish/IPMI against at least one physical or lab BMC endpoint each.
- Validate SSH collection against a controlled SSH host.
- Run Docker Compose on a Linux host with production secrets and PostgreSQL.
