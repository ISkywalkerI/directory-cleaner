# 配置参考

配置使用 YAML，顶层包含 `version`、`defaults` 和 `directories`。可从仓库中的 [`directory-cleaner.yaml.example`](../directory-cleaner.yaml.example) 复制模板；目录级设置覆盖 `defaults` 中的同名设置。

```yaml
version: 1

defaults:
  scan_interval: 5m
  stable_for: 2m
  min_age: 10m
  max_workers: 1
  dry_run: true
  audit_log_dir: logs

directories:
  - name: rundata
    path: C:/data/rundata
    trigger_bytes: 100GB
    target_bytes: 80GB
```

## 参数

| 参数 | 说明 |
| --- | --- |
| `version` | 配置格式版本，必须是 `1`。 |
| `defaults` | 所有目录的公共默认设置，可由目录项覆盖。 |
| `directories` | 至少包含一个目录。每个目录独立统计容量和执行清理。 |
| `name` | 审计日志中的目录标识，不能重复；省略时自动生成 `directory-1`、`directory-2` 等名称。 |
| `path` | 要递归扫描的根目录，必须填写。程序只处理普通文件，不跟随符号链接。 |
| `scan_interval` | 连续运行时的扫描间隔，启动后立即扫描一次；必须大于 `0`。 |
| `stable_for` | 一轮候选文件的统一稳定性观察时长；不能为负数。`0s` 跳过等待，但仍执行复核和访问检查。 |
| `min_age` | 文件进入清理检查前必须达到的最小未修改时长；不能为负数。`0s` 不设年龄门槛。 |
| `max_workers` | 稳定性检查的并发 worker 数，至少为 `1`；删除仍按顺序执行。 |
| `dry_run` | 是否只预演。默认 `true`；命令行 `-dry-run` 或 `-execute` 可覆盖所有目录。 |
| `audit_log_dir` | JSONL 审计日志目录，默认 `logs`。相对路径相对于程序启动时的工作目录。该目录中的旧日志也参与普通清理规则。 |
| `audit_log_max_bytes` | 活动日志轮转阈值，默认 `10MB`；`0` 表示不轮转，只能在 `defaults` 中配置。 |
| `trigger_bytes` | 目录普通文件大小合计必须严格超过此值才触发清理。 |
| `target_bytes` | 触发后希望达到的剩余容量。必须满足 `trigger_bytes > target_bytes >= 0`。 |

## 时间与容量

时间使用 Go duration 格式，数字后必须跟单位：

| 示例 | 含义 |
| --- | --- |
| `500ms` | 500 毫秒 |
| `30s` | 30 秒 |
| `5m` | 5 分钟 |
| `2h` | 2 小时 |
| `24h` | 24 小时 |

容量支持字节数字以及 `KB`、`MB`、`GB`、`TB` 后缀，按 1024 进位；后缀不区分大小写，也支持小数，例如 `1.5GB`。`KiB`、`MiB` 等写法不支持。

## 清理规则与预演

```yaml
trigger_bytes: 100GB
target_bytes: 80GB
```

目录普通文件总量必须**大于** `100GB` 才会触发。触发后，程序按最后修改时间从旧到新复核候选文件，并删除完整文件，尝试将估算剩余容量降至不高于 `80GB`。由于删除整个文件，容量可能低于目标；如果候选文件未通过复核或删除失败，容量也可能仍高于目标。`min_age` 和 `stable_for` 是删除保护条件，不会单独触发清理。

首次运行时保持 `dry_run: true`，查看 `audit_log_dir` 中的 `delete` 和 `summary` 事件，确认路径、候选顺序和预计容量。之后可在配置中设 `dry_run: false`，或在单次命令中使用 `-execute`。`-dry-run` 与 `-execute` 不能同时指定，且命令行覆盖作用于所有目录。

真实删除使用操作系统文件删除调用，没有回收站或恢复功能。Windows 会尝试独占打开候选文件；Linux 无法通过普通系统调用可靠识别其他进程是否持有文件，年龄和稳定性检查只能降低风险。
