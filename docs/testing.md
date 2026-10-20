# 全流程测试与部署验收

## 自动化门禁

| 阶段 | 入口 | 通过标准 |
| --- | --- | --- |
| 配置和内部清理 | `go test ./...` | 默认预演、继承/覆盖、字节范围、非法配置、触发阈值、递归、旧文件优先、目标停止、年龄、稳定性、删除前复核、取消、符号链接和 JSONL 审计通过 |
| 真实 CLI | 同一 Go 测试命令 | 编译真实二进制，验证默认预演、`-dry-run`/`-execute`、配置删除、错误退出码、Unicode/空格路径、重复扫描；Windows 真实 Ctrl+Break、Ubuntu SIGINT 均须在 5 秒内正常退出 |
| Windows 原生 | `pwsh -File scripts/test.ps1` | Go 1.25.12，格式、PowerShell 脚本语法、依赖校验、vet、测试、覆盖率、构建；真实文件锁保护通过 |
| Ubuntu 原生 | `bash scripts/test.sh` | Ubuntu 22.04/24.04 + Go 1.25.12，额外 Ubuntu 22.04 + Go 1.21.13；包括权限、FIFO 和 race 检查 |
| Windows 服务 | 管理员 `pwsh -File scripts/test-nssm.ps1 -Nssm <nssm.exe路径>` | 在 Windows Server 2022 实际安装唯一临时 NSSM 服务，验证预演、stop/start/restart、崩溃后恢复、优雅取消、真实删除、卸载；禁用窗口/线程消息和强制终止兜底，子进程必须退出码 0 |
| Ubuntu 服务 | `sudo bash scripts/test-systemd.sh` | 在 Ubuntu 22.04/24.04 实际安装仓库 unit（只替换测试路径和专用账号），验证 enable、启动、停止、重启、异常恢复、取消审计、真实删除和卸载；必须使用真实 systemd，停止后退出状态和 Result 均正常 |
| 交叉编译 | 脚本交叉编译参数或 CI Build only 作业 | Windows/Linux × amd64/arm64 全部编译成功；只证明可构建 |
| 合并门禁 | GitHub Actions `CI` | 所有矩阵作业 success；失败、取消和跳过作业均不能放行 |

脚本只检查格式；需要修正时执行 `gofmt`。不设覆盖率百分比门槛，报告用于发现遗漏。测试使用独立临时目录和审计文件。`-count=1` 避免缓存掩盖本次结果，单次测试超时 3 分钟。JSONL 测试报告包含用例输出与 pass/fail/skip，覆盖率在 `artifacts/coverage.out`；CI 保留报告 14 天。

Windows CLI 子进程使用独立隐藏控制台；辅助进程附加该控制台发送真实 Ctrl+Break，避免影响测试进程和用户控制台，要求退出码 0。NSSM 服务测试通过真实服务停止路径发送 Ctrl+C，并禁用其他停止方式，避免强制终止掩盖退出问题。Ubuntu 验证 SIGINT 和 systemd 默认 SIGTERM。服务停止期间还覆盖 60 秒稳定观察被及时取消，并检查最后一条取消审计已写出。强制杀进程只用于异常恢复测试和失败清理。

服务测试使用唯一服务名、专用临时目录和真实测试文件，结束时卸载；Ubuntu 创建并删除专用测试账号。缺少管理员/root 权限或真实服务管理器时直接失败。CI 下载固定 NSSM 2.24-101-g897c7ad，从固定 Chocolatey 包取出官方原始压缩包并校验 SHA256；服务审计与诊断保留在 `artifacts/nssm/`、`artifacts/systemd/`，随 CI 报告上传。先运行本机测试脚本构建二进制，再单独运行服务脚本；普通单元测试不要求管理员权限。

没有符号链接权限时记录 skip；Unix 以 root 运行时权限拒绝用例记录 skip。发布目标若依赖这些能力，必须使用具备能力的账号补测。Linux 无法保证识别其他进程持有的普通文件，年龄和稳定性只能降低风险。

## 每次较大修改后的流程

1. 为行为变化添加回归用例，运行本机完整脚本（含交叉编译）。较大修改包括清理行为、并发、平台接口、依赖和部署变化。
2. 提交 PR，等待 `CI` 全绿。失败必须修复；不得绕过删除保护或将失败改成 skip。GitHub 报告和 PR 记录平台、Go 版本、命令、结果及能力跳过项。
3. 从通过检查的同一提交为目标平台构建二进制。CI 自动服务验收必须通过；首次部署，以及部署方式/权限/退出逻辑变化时，在目标机器补验实际账号、权限、安装路径和开机行为。
4. 在实际部署机器先用预演配置确认路径和审计，再按业务授权启用删除。当前业务 YAML、真实日志和缓存禁止提交。

## Windows NSSM 目标机器补验（人工）

在管理员 PowerShell 使用唯一测试服务名称（例如 `DirectoryCleaner-Acceptance`），安装到专用测试根目录。该目录与任何业务目录隔离；二进制和 NSSM 必须来源明确。

1. 记录 Windows 版本/架构、Go 版本、提交 SHA、NSSM 版本。复制对应二进制、配置和安装脚本。
2. 在测试根目录建立 `data` 和 `logs`；仅在 `data` 创建三个 4 字节文件，将修改时间设为不同旧时间。配置使用绝对路径，`trigger_bytes: 10`、`target_bytes: 4`、`scan_interval: 1s`、`stable_for: 0s`、`min_age: 0s`、`dry_run: true`；审计位于 `logs/audit.jsonl`。
3. 执行 `deploy/install-nssm.ps1 -Root <测试根目录> -ServiceName DirectoryCleaner-Acceptance -Nssm <nssm.exe绝对路径>`。用 `Get-Service DirectoryCleaner-Acceptance` 确认 Running，检查连续多轮 JSONL 预演，三个文件均保留；检查 service.out.log/service.err.log。
4. 用 `Stop-Service` 验证及时停止，停止后日志不再增长、没有遗留 dircleaner 子进程；`Start-Service` 和 `Restart-Service` 后扫描恢复。记录停止耗时和是否依赖强制结束。验收 NSSM 自动重启策略时，仅结束此测试服务的 dircleaner 子进程，确认新 PID 和扫描恢复。
5. 停止测试服务，将配置改为 `dry_run: false`，启动后确认只删旧两个文件，剩余 4 字节，summary 删除 2 个/8 字节；新文件保留。
6. 记录服务账号和目录/日志权限；使用无写权限账号或目录验证错误出现在审计/错误日志，恢复权限后再次预演。服务账号只授权所需业务目录和日志目录。
7. 停止并使用 `nssm remove DirectoryCleaner-Acceptance confirm` 移除唯一测试服务，保存验收记录后清理本次测试根目录。

## Ubuntu systemd 目标机器补验（人工）

在有 systemd 的 Ubuntu 22.04/24.04 VM 或主机操作；容器内普通进程运行不能代替服务验收。使用独立测试服务名称，检查每条命令的返回码。

1. 记录 `/etc/os-release`、`uname -m`、Go 版本和提交 SHA。创建专用测试用户以及 `/opt/directory-cleaner-acceptance`、`/var/log/directory-cleaner-acceptance`，测试数据放前者的 `data`，授予测试账号所需权限。
2. 安装通过 CI 提交构建的二进制；建立与 Windows 相同的三个测试文件和预演配置，全部使用绝对路径。审计放独立日志目录。
3. 将仓库 unit 复制为 `directory-cleaner-acceptance.service`；将 `WorkingDirectory`、`ExecStart`、`User`、`Group`、`ReadWritePaths` 替换为本次测试路径和账号。`ReadWritePaths` 包含日志目录以及需要写入的测试数据目录，目录须预先创建。先执行 `systemd-analyze verify <测试unit路径>`。
4. 安装测试 unit 后执行 `sudo systemctl daemon-reload`、`sudo systemctl enable --now directory-cleaner-acceptance`，用 `systemctl is-active`、`systemctl status` 和 `journalctl -u directory-cleaner-acceptance` 检查；确认多轮审计有效、文件保留。
5. 执行 stop/start/restart，确认无停止超时、停止后不再扫描、重启后审计追加。仅结束测试服务主进程，验证 `Restart=on-failure` 约 10 秒后恢复。需要验证开机自启时，仅重启隔离 VM。
6. 停止测试服务，配置改为真实删除，启动后核验剩余 4 字节和删除 2 个/8 字节；验证限制权限时错误可见，恢复权限后工作正常。
7. `disable --now` 此测试服务，移除本次测试 unit 后 daemon-reload；保留验收记录，再清理测试用户和目录。

生产 unit 的默认审计相对路径会落在 `/opt/directory-cleaner/logs`；生产部署应将 `audit_log` 明确配置为 `/var/log/directory-cleaner/audit.jsonl`，预先创建目录并授权 `directory-cleaner` 用户。自动服务测试覆盖托管 Runner 的安装和生命周期，实际生产路径、账号、ACL、开机自启和机器重启仍须目标机器补验。

## 验收记录

每次记录：提交 SHA、系统/架构、Go 与服务管理器版本、使用的配置路径、测试命令、CI run URL、通过/失败/skip、预演与删除的 JSONL 样例、剩余文件、服务停止/重启结果及验收人。ARM64 必须标注“仅交叉编译，未做运行时验收”。有未通过项目时不得标注部署通过。
