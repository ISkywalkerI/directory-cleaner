# 审计日志

程序把审计事件按 JSON Lines（JSONL）格式写入 `audit_log_dir`，每行是一个独立 JSON 对象。启动和日志轮转时创建新文件，名称为 `dir-cleaner-YYYYMMDD-HHMMSS.jsonl`；同一秒内重名时追加序号。相对日志目录以程序启动时的工作目录为基准。

## 事件

| `action` | 含义 |
| --- | --- |
| `scan` | 目录总量未超过 `trigger_bytes` 时记录扫描结果。 |
| `delete` | 记录预演计划中的文件，或记录成功删除的文件；预演事件的 `reason` 为 `dry_run`。删除失败的文件不会产生此事件，而会进入汇总中的 `failures`。 |
| `summary` | 总量超过阈值后记录计划、实际删除结果、跳过项和错误。 |

## 字段

| 字段 | 含义 |
| --- | --- |
| `time` | 事件时间。 |
| `directory`、`path` | 配置的目录名称和扫描根路径。 |
| `dry_run` | 本轮是否为预演。 |
| `action` | 事件类型，见上表。 |
| `file` | `delete` 事件中的文件路径、大小和修改时间。 |
| `reason` | 可选原因；预演文件记录为 `dry_run`。 |
| `before_bytes` | 本轮扫描到的目录文件总字节数。 |
| `after_bytes` | 本轮结束时的估算剩余字节数。预演时保持为扫描总量，不代表预演后的实际容量。 |
| `current_bytes` | `summary` 中的扫描总量。 |
| `planned_files`、`planned_bytes` | 通过删除前最终复核并纳入清理计划的文件数和字节数。真实删除时，即使操作系统之后拒绝删除，该候选仍计入计划。 |
| `planned_mod_time_from`、`planned_mod_time_to` | 计划文件的最早和最晚修改时间。 |
| `deleted_files`、`deleted_bytes` | 成功删除的文件数和字节数；预演时为零。 |
| `deleted_mod_time_from`、`deleted_mod_time_to` | 实际成功删除文件的最早和最晚修改时间。预演或没有成功删除文件时省略。 |
| `skipped_files` | 因年龄、稳定性或访问检查未通过而跳过的文件数。 |
| `failures` | 扫描或删除过程中发生的错误及候选跳过原因。为空时省略。 |

计划和实际删除是不同统计：预演会产生 `planned_*`，但不会增加 `deleted_*`；真实运行中的失败删除也可能已通过最终复核并计入计划，却不会计入实际删除。`after_bytes` 是按本轮扫描快照估算的值，不是重新扫描结果。

审计目录中的旧日志是普通文件，会按该目录自身的清理配置参与扫描和清理；请为日志配置合适的阈值，并在预演时核对日志目录路径。
