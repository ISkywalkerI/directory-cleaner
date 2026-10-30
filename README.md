# Directory Cleaner

[![Tests](https://github.com/ISkywalkerI/directory-cleaner/actions/workflows/test.yml/badge.svg)](https://github.com/ISkywalkerI/directory-cleaner/actions/workflows/test.yml)

Directory Cleaner 是一个跨平台目录清理服务，可在后台持续监控，按容量阈值触发，并优先清理修改时间较早的文件。

Windows 和 Linux 使用同一份 YAML 配置。清理前可预演并查看审计日志；默认 `dry_run: true`。

许可证：[MIT](LICENSE)。

## 快速开始

从 [GitHub Releases](https://github.com/ISkywalkerI/directory-cleaner/releases) 下载对应系统的发布包并解压。首次运行时复制配置模板，修改要监控的目录和容量阈值，保持 `dry_run: true`，然后执行一次预演。

Windows PowerShell：

```powershell
Copy-Item .\directory-cleaner.yaml.example .\directory-cleaner.yaml
notepad .\directory-cleaner.yaml
.\start.ps1 -once -dry-run
```

Ubuntu：

```bash
cp -n directory-cleaner.yaml.example directory-cleaner.yaml
vi directory-cleaner.yaml
./start.sh -once -dry-run
```

检查 `audit_log_dir` 指定目录中的 JSONL 审计记录，确认清理计划后再决定是否启用真实删除。完整安装和服务配置见[部署说明](deploy/README.md)。

## 适用场景

如果你在 Windows 和 Linux 上分别维护目录清理脚本，Directory Cleaner 可以用同一份 YAML 配置持续监控各目录的容量。超过阈值后，程序会检查文件年龄和稳定性，再优先处理修改时间较早的文件。它不支持按业务内容制定保留规则，也没有回收站或恢复功能；需要这些能力时，应选支持相应策略的工具。

## 发布平台

提供 Windows x64、Linux x64 和 Linux ARM64 发布包，可从 [GitHub Releases](https://github.com/ISkywalkerI/directory-cleaner/releases) 下载。运行发布包不需要安装 Go；校验、服务安装和升级步骤见[部署说明](deploy/README.md)。

## 配置要点

每个目录单独设置容量阈值；总量必须严格超过 `trigger_bytes` 才触发，随后按修改时间从旧到新处理文件，尝试将估算容量降至不高于 `target_bytes`。`min_age` 和 `stable_for` 是删除前的保护条件，不会单独触发清理。默认 `dry_run: true`；先检查审计日志，再启用真实删除。完整参数、时间与容量单位见[配置参考](docs/configuration.md)。

## 后台运行

需要开机启动时，可将发布包安装为 Windows NSSM 或 Linux systemd 服务。安装、停止、升级和卸载步骤见[部署说明](deploy/README.md)。

## 审计

程序将扫描、预演、删除结果和错误写入 `audit_log_dir` 中的 JSONL 文件。`planned_*` 统计通过最终复核的计划文件；`deleted_*` 只统计实际成功删除的文件，预演时为零。事件字段及 `after_bytes` 的估算含义见[审计日志说明](docs/audit-log.md)。

## 开发和测试

源码开发需要 Go 1.21+。在仓库根目录运行本机测试：

```powershell
pwsh -File scripts/test.ps1
```

Ubuntu 需要 Go 和 C 编译器（用于 race detector）：

```bash
bash scripts/test.sh
```

测试矩阵、可选交叉编译（仅构建验证）和 NSSM/systemd 服务验收见[全流程测试与部署验收](docs/testing.md)。
