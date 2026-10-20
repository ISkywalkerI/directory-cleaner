# Directory Cleaner

[![Tests](https://github.com/ISkywalkerI/directory-cleaner/actions/workflows/test.yml/badge.svg)](https://github.com/ISkywalkerI/directory-cleaner/actions/workflows/test.yml)

独立的跨平台目录容量清理服务。Windows 和 Linux 使用同一份 YAML 配置；每个目录独立统计、触发和清理。

```text
dircleaner -config directory-cleaner.yaml
dircleaner -config directory-cleaner.yaml -once
dircleaner -config directory-cleaner.yaml -once -execute
```

配置默认 `dry_run: true`。先查看 `audit_log` 中的预演记录，确认路径后把配置改为 `dry_run: false`，或仅在手工执行时使用 `-execute`。

容量值支持字节以及 `KB`、`MB`、`GB`、`TB` 后缀，例如 `100GB`。`trigger_bytes` 必须大于 `target_bytes`；目录超过触发上限后，按修改时间从旧到新删除，直到低于目标值。

程序递归扫描普通文件，不跟随符号链接。文件必须超过 `min_age`，并在 `stable_for` 观察期间大小和修改时间保持不变；Windows 还会尝试独占打开文件。Linux 无法从普通系统调用可靠判断其他进程是否持有文件，因此只能通过年龄和稳定性降低风险。

## Windows NSSM

准备 `dircleaner.exe`、`directory-cleaner.yaml` 和 NSSM 后，在管理员 PowerShell 中执行：

```powershell
.\deploy\install-nssm.ps1 -Root C:\OiSmart3D\directory-cleaner
```

## Linux systemd

复制二进制到 `/opt/directory-cleaner/dircleaner`，配置到 `/etc/directory-cleaner/config.yaml`，创建专用用户并安装 `deploy/directory-cleaner.service`，然后执行：

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now directory-cleaner.service
```

## 审计

`audit_log` 为 JSONL 文件，必须位于清理目录之外。未超过触发值时写入 scan，触发清理后写入 summary；预演或成功删除的每个文件另写一条 delete 事件；锁定、权限不足、文件变化和删除失败会写入 failures。预演只列出达到目标容量所需的文件，summary 的 after_bytes 仍反映实际容量，删除数量为零。

## 开发和测试

```text
git clone https://github.com/ISkywalkerI/directory-cleaner.git
cd directory-cleaner
```

安装 Go 1.21+；CI 主版本固定 Go 1.25.12。Windows 安装 PowerShell 7 后运行：

```powershell
pwsh -File scripts/test.ps1 -CrossCompile
```

Ubuntu 安装 Go 和 C 编译器（竞态检测需要）后运行：

```bash
bash scripts/test.sh --cross-compile
```

省略交叉编译参数只执行本机检查。报告在 `artifacts/`；测试通过 Go 标准库创建临时目录，调用真实二进制验证预演、删除和连续运行，不读取实际业务配置。

每次 push、PR 和手动触发均运行 Windows Server 2022、Ubuntu 22.04/24.04 x64 原生测试，额外校验 Go 1.21.13，并编译 Windows/Linux 的 amd64/arm64。`CI` 汇总检查是合并门禁。ARM64 仅验证可构建。

测试矩阵、跳过项和 NSSM/systemd 部署验收见 [全流程测试与部署验收](docs/testing.md)。较大修改必须补回归测试并通过完整 CI 后才可部署。
