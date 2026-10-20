# Directory Cleaner

[![Tests](https://github.com/ISkywalkerI/directory-cleaner/actions/workflows/test.yml/badge.svg)](https://github.com/ISkywalkerI/directory-cleaner/actions/workflows/test.yml)

独立的跨平台目录容量清理服务。Windows 和 Linux 使用同一份 YAML 配置；每个目录独立统计、触发和清理。

## 三平台发布与部署

目标机从 GitHub Releases 获取对应压缩包即可，无须 Go 或完整源码：Windows x64 为 `windows-amd64.zip`，Ubuntu x64 为 `linux-amd64.tar.gz`，Ubuntu ARM64 为 `linux-arm64.tar.gz`（文件名还包含版本）。Linux 用 `tar -xzf` 解压以保留权限。首次复制包内 `.yaml.example` 为 `.yaml`，修改路径后运行启动脚本预演；详细服务安装、校验、升级和回退见 [部署包说明](deploy/README.md)。

开发机安装 PowerShell 7.4+、Git、Go **1.25.12**，在仓库根目录执行：

```powershell
pwsh -NoProfile -File scripts/build.ps1
# 使用上一条输出的绝对版本目录
pwsh -NoProfile -File scripts/test-release.ps1 -ReleaseDirectory artifacts/releases/<版本>
```

同一脚本可在 Windows 或 Ubuntu 构建三个平台，不需要 C 交叉编译器。首次构建需要下载 Go 依赖和固定版 NSSM，支持 Go 模块缓存；离线构建可用 `-NssmArchive <nssm-2.24-101-g897c7ad.zip>` 提供原始工具压缩包，仍强制校验 SHA256。当前唯一运行时模块 `gopkg.in/yaml.v3 v3.0.1` 为纯 Go，编译进二进制；`check.v1` 为依赖测试用途，不作为独立运行文件分发。

脚本固定 `CGO_ENABLED=0`、`GOAMD64=v1`、`GOARM64=v8.0`，逐目标检查 CGO 依赖、PE/ELF 架构和 Linux 动态解释器、版本及压缩包内容。未来依赖引入 CGO 时会失败并要求重新评估 C 工具链及运行依赖，不静默跳过。Go 版本、系统和 CPU 支持仍以目标机器验收为准。

产物位于 `artifacts/releases/<版本>/`，包含三个平台部署目录、一个 ZIP、两个 tar.gz 及 `SHA256SUMS`。压缩包统一以 `directory-cleaner/` 为根目录，只含平台程序、配置模板、启动/服务资产、README、版本与第三方声明。构建失败保留 `.incomplete-*` 诊断目录，不生成正式版本目录；已存在的版本不会被覆盖。

版本取当前提交的 `v*` tag（多个时取最高版本），无 tag 时为 UTC 时间和短 commit；本地未提交修改标记 `dirty`。tag 发布要求干净工作区。`dircleaner -version` 无需读取配置即可查询构建信息；发布脚本固定工具链，普通源码开发仍支持 Go 1.21+。

每次 push、PR、手动运行，CI 构建并上传发布包，在 Windows、Ubuntu 22.04/24.04 上解压和验证本机包，并用发布二进制运行现有服务验收。`v*` tag push 在同一次运行的全部 `CI` 门禁通过后自动发布。按仓库流程先合并通过 CI 的 PR，再对已合并提交创建 tag；不要在未经检查的提交上发布。ARM64 包仅交叉编译和结构检查，尚无 ARM64 原生运行门禁。

发布包不含现场 YAML、日志和业务数据。升级前停止进程，将新包解压到暂存目录后只更新程序资产，保留现场文件；禁止清空安装目录或使用带删除语义的镜像同步。首次部署的配置复制步骤不适用于升级。

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

每次 push、PR 和手动触发均运行 Windows Server 2022、Ubuntu 22.04/24.04 x64 原生测试，额外校验 Go 1.21.13，并编译 Windows/Linux 的 amd64/arm64。Windows 连续运行测试发送真实 Ctrl+Break 并要求正常退出。CI 还会实际安装临时 NSSM/systemd 服务，验证预演、启停、重启、异常恢复、优雅取消、真实删除和卸载；NSSM 禁用强制终止兜底。`CI` 汇总检查是合并门禁。ARM64 仅验证可构建。

测试矩阵、跳过项和 NSSM/systemd 部署验收见 [全流程测试与部署验收](docs/testing.md)。较大修改必须补回归测试并通过完整 CI 后才可部署。
