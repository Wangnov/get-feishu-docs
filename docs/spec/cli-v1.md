# get-feishu-docs CLI v1 规范

## 命令树

- `get-feishu-docs <url>`：直接抓取主入口
- `get-feishu-docs capture <url>`：抓取别名
- `get-feishu-docs doctor`
- `get-feishu-docs browser status`
- `get-feishu-docs browser install`

## 公共参数

`get-feishu-docs` 及 `capture` 共享以下参数：

- `--password <value>`
- `--password-stdin`
- `--out-dir <dir>`（默认 `./output`）
- `--output <formats>`（`html,png,pdf,md`）
- `--output-all`
- `--json`
- `--progress <text|jsonl>`
- `--browser <auto|system|managed>`（默认 `auto`）
- `--browser-path <path>`
- `--timeout-ms <ms>`
- `--settle-ms <ms>`
- `--debug`

## 交互约定

- 默认：stderr 输出阶段进度，stdout 输出人类可读摘要（标题、输出目录、主要文件、统计）
- `--json`：stdout 仅输出最终 JSON 结果
- `--progress=jsonl`：stderr 以 JSONL 输出阶段事件
- 密码优先级：`--password` > `--password-stdin` > TTY 提示输入
- `--output-all` 与 `--output` 同时出现时，`--output-all` 覆盖为 `html,png,pdf,md`

## 成功结果

- `ok`
- `title`
- `titleRaw`（可选）
- `outputDir`
- `selectedOutputs`
- `files`（含 `path`、`kind`）
- `stats`
- `debug`（可选）

## 失败结果

- `ok: false`
- `error.code`
- `error.message`

## 进度事件

每个事件包含：

- `type`
- `stage`
- `message`
- `stats`（可选）
- `timestamp`

## 退出码

- `0` 成功
- `2` usage
- `3` browser
- `4` auth
- `5` capture
- `6` write

## 错误码

- `browser_unavailable`
- `browser_install_failed`
- `password_required`
- `document_timeout`
- `document_selector_drift`
- `capture_failed`
- `write_failed`
