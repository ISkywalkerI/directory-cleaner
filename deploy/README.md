# Directory Cleaner 部署包

本包只包含对应平台的运行文件，不需要源码、Go 或 C 运行库安装。Windows x64 使用系统 PowerShell；Ubuntu 使用 Bash。程序依赖目标操作系统内核及系统 API；Ubuntu 22.04/24.04 x64 由 CI 原生验收，Linux ARM64 的交叉编译检查不能代替目标机运行验收。

`VERSION.json` 记录版本、源码提交、UTC 构建时间和架构。发行页同时提供 `SHA256SUMS`：Windows 使用 `Get-FileHash <压缩包> -Algorithm SHA256` 对比；Ubuntu 将三个包和校验文件放在一起运行 `sha256sum -c SHA256SUMS`。

## 首次启动

解压后进入 `directory-cleaner`。仅首次部署复制 `directory-cleaner.yaml.example` 为 `directory-cleaner.yaml`，编辑清理目录、容量与审计路径；保持 `dry_run: true`。模板路径是示例，不会创建实际业务目录。

Windows：

```powershell
Copy-Item directory-cleaner.yaml.example directory-cleaner.yaml
notepad directory-cleaner.yaml
powershell -NoProfile -ExecutionPolicy Bypass -File .\start.ps1 -once -dry-run
.\dircleaner.exe -version
# 连续运行，Ctrl+C 停止
powershell -NoProfile -ExecutionPolicy Bypass -File .\start.ps1
```

Ubuntu（使用 tar 解压以保留执行权限）：

```bash
cp -n directory-cleaner.yaml.example directory-cleaner.yaml
editor directory-cleaner.yaml
./start.sh -once -dry-run
./dircleaner -version
# 前台连续运行，Ctrl+C 停止
./start.sh
```

Windows 的 `-ExecutionPolicy Bypass` 仅作用于当前启动进程，不修改机器策略；应先核对发布包 SHA256，现场组策略仍可能禁止运行脚本，此时可在包目录直接执行二进制。

启动脚本将工作目录固定为包目录，透传全部参数并保留退出码；从其他目录启动也使用包内默认配置。`-config` 和 YAML 中的相对路径相对于此工作目录。直接调用 `dircleaner.exe` 时，相对路径相对于当前工作目录，因此请先进入 `directory-cleaner` 安装目录再启动；通过本项目的 NSSM 安装脚本启动服务时，脚本会将工作目录设为安装目录，无需额外处理。

默认审计日志目录在 `logs`，可以位于清理目录内。程序每次启动和轮转时直接创建时间戳日志文件；日志目录中的文件按修改时间从旧到新作为普通清理候选。先查看 JSONL 中的预演结果，再按业务授权修改配置。`-execute` 会覆盖所有目录的预演设置并真实删除文件。

## Windows NSSM 服务（可选）

包内 `tools/nssm.exe` 为固定版 2.24-101-g897c7ad 的 x64 程序。管理员 PowerShell 执行：

```powershell
.\deploy\install-nssm.ps1 -Root C:\Apps\directory-cleaner
Get-Service DirectoryCleaner
Stop-Service DirectoryCleaner
Start-Service DirectoryCleaner
Restart-Service DirectoryCleaner
# 卸载前停止服务；不删除配置、日志和业务数据
.\tools\nssm.exe remove DirectoryCleaner confirm
```

安装脚本默认使用随包 NSSM，可用 `-Nssm`、`-Config`、`-ServiceName` 指定工具、配置和服务名。NSSM 服务日志在 `nssmlogs/service.out.log`、`nssmlogs/service.err.log`；程序审计日志仍按 `audit_log_dir` 配置写入。根据现场 ACL 配置服务账号，验证真实停止时审计已写完。

如果 PowerShell 提示“在此系统上禁止运行脚本”，可在管理员 PowerShell 中临时放行当前窗口，然后运行安装脚本：

```powershell
Set-ExecutionPolicy -Scope Process -ExecutionPolicy Bypass -Force
.\deploy\install-nssm.ps1
```

关闭该 PowerShell 窗口后，`Process` 设置自动失效。若希望当前 Windows 用户以后默认允许本地脚本运行，可设置一次 `RemoteSigned`：

```powershell
Set-ExecutionPolicy -Scope CurrentUser -ExecutionPolicy RemoteSigned
```

查看所有范围及其优先级：

```powershell
Get-ExecutionPolicy -List
```

如果 `MachinePolicy` 或 `UserPolicy` 已设置，组策略优先于本地设置，需要由域或主机管理员调整。对从网络下载且带有下载标记的可信脚本，也可以只解除该文件的标记：

```powershell
Unblock-File .\deploy\install-nssm.ps1
```

## Ubuntu systemd 服务（可选）

前台运行不需要 root。需要服务托管时，使用现有 unit 的约定：二进制在 `/opt/directory-cleaner/dircleaner`，配置在 `/etc/directory-cleaner/config.yaml`，审计使用 `/var/log/directory-cleaner`。

```bash
sudo useradd --system --user-group --no-create-home --shell /usr/sbin/nologin directory-cleaner
sudo install -d /opt/directory-cleaner /etc/directory-cleaner
sudo install -d -o directory-cleaner -g directory-cleaner /var/log/directory-cleaner
sudo install -m 755 dircleaner /opt/directory-cleaner/dircleaner
# 仅首次部署复制配置；已有配置必须保留
sudo test -e /etc/directory-cleaner/config.yaml || sudo cp directory-cleaner.yaml.example /etc/directory-cleaner/config.yaml
sudoedit /etc/directory-cleaner/config.yaml
```

将配置中的 `audit_log_dir` 改为上述绝对日志目录，业务目录也使用绝对路径。程序会在该目录中创建时间戳日志文件。给专用用户配置清理目录所需读写权限。根据现场路径编辑 `deploy/directory-cleaner.service`；若业务数据位于 `/usr`、`/boot` 或 `/etc` 等受 `ProtectSystem=full` 保护的位置，需明确加入 `ReadWritePaths`。涉及目录必须事先存在。

```bash
sudo -u directory-cleaner /opt/directory-cleaner/dircleaner -config /etc/directory-cleaner/config.yaml -once -dry-run
sudo systemd-analyze verify deploy/directory-cleaner.service
sudo install -m 644 deploy/directory-cleaner.service /etc/systemd/system/directory-cleaner.service
sudo systemctl daemon-reload
sudo systemctl enable --now directory-cleaner.service
sudo systemctl status directory-cleaner.service
sudo journalctl -u directory-cleaner.service
# 停止、启动、重启
sudo systemctl stop directory-cleaner.service
sudo systemctl start directory-cleaner.service
sudo systemctl restart directory-cleaner.service
# 卸载只移除服务注册
sudo systemctl disable --now directory-cleaner.service
sudo rm /etc/systemd/system/directory-cleaner.service
sudo systemctl daemon-reload
```

## 升级与回退

1. 核对架构、版本和 SHA256；将新包解压到单独的暂存目录，不要在运行中的目录直接解压。
2. 停止服务或前台进程并确认退出。备份原程序资产、当前配置；日志和业务数据保持原位。
3. 将暂存目录内的程序资产覆盖到安装目录。包内只有 `.yaml.example`，不含现场 `.yaml`、`logs/` 或业务数据；不要使用清空目录、`robocopy /MIR` 或 `rsync --delete` 的方式升级。systemd 的 `/etc` 配置保持原位，不重新执行首次配置步骤。
4. Windows 升级 NSSM 时必须先停止服务；已有 NSSM 进程仍占用工具文件时保留当前工具，不强制覆盖或终止其他服务。已有服务无需再次安装。
5. 运行 `-version`，使用现场配置执行 `-once -dry-run`，核对审计后恢复运行。unit 若有人工调整，逐项合并，不直接覆盖已安装的 unit。
6. 回退时先停止服务，仅恢复备份的程序资产，保留配置及已追加的审计，再预演并启动。未来版本若引入配置迁移，应另行审核，当前无迁移。

本包的构建校验不代替目标机器服务账号、权限、开机启动及 ARM64 运行验收。第三方声明位于 `licenses/`。
