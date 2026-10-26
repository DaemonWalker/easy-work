# 架构与关键约束

本文说明当前实现的主要结构。产品行为以 [REQUIREMENTS.md](../REQUIREMENTS.md) 为完整基线；本文与代码或需求不一致时，应先确认原因并同步修正。

## 总体结构

```text
Edge / Chrome
    │ 同源 HTTP + SSE
    ▼
Go 服务端（EasyWork.exe）
    ├── 提供 html/ 静态页面和本地词典
    ├── 提供 /api/*，保存 history/
    ├── 全局任务互斥与停止
    └── 启动、监控并代理 llama-server
             ├── Chat：127.0.0.1:18181
             └── VL：  127.0.0.1:18182
```

浏览器不直接访问 llama-server。对外服务默认绑定 `127.0.0.1:18765`，使用 `-lan` 时绑定 `0.0.0.0:18765`；两个模型端口仅用于本机进程间通信。

## 后端职责

后端是一个 `main` 包，主要文件如下：

| 文件 | 职责 |
| --- | --- |
| `backend/main.go` | 启动参数、监听地址、浏览器打开和退出清理 |
| `backend/api.go` | HTTP 路由、参数校验、对话与模型 API |
| `backend/models.go` | 模型扫描、内存检查、加载／卸载、健康检查与崩溃恢复 |
| `backend/stream.go` | llama.cpp 流式响应、统计信息与 SSE 转发 |
| `backend/images.go` | 图片校验、OCR、重试与翻译流程 |
| `backend/task.go` | 全局任务状态、互斥和取消 |
| `backend/store.go` | 设置、对话与图片任务的 JSON 持久化 |
| `backend/gguf.go` | GGUF 元数据读取 |
| `backend/process_*.go` | 平台相关的进程管理；Windows 使用 Job Object 清理子进程 |
| `backend/hardware_*.go` | CPU 与内存采样 |

## 前端职责

- `frontend/src/App.tsx` 负责全局状态、页面切换、轮询和任务事件分发。
- `frontend/src/components/` 包含普通对话、图片、设置、模型状态和统计等视图。
- `frontend/src/api.ts` 负责同源 HTTP 请求及 SSE 解析。
- `frontend/src/romaji.ts` 使用 Kuroshiro 在浏览器本地转换罗马音。
- `frontend/scripts/copy-dict.mjs` 在开发或构建前，将 Kuromoji 词典复制到静态资源目录。

页面每 2 秒刷新服务端状态。普通回复通过 SSE 流式显示，罗马音在完整日文回复到达后于浏览器生成。

## 必须保持的运行约束

### 全局互斥

所有翻译、自由 Chat、OCR 和图像翻译共享一个全局任务管理器。同一时刻最多执行一个模型任务；忙碌时直接拒绝新任务，**不排队，也不并行推理**。

停止操作必须取消服务端上下文并传播到实际 llama-server 请求，不能只隐藏前端进度。

### 模型生命周期

- 选择模型、切换页面或查看历史不应触发加载。
- 只有真正执行任务时才加载或切换模型。
- 最多保留一个 Chat 和一个 VL 进程，同类模型不能同时加载多个。
- Chat 和 VL 分别管理状态与卸载，但模型切换操作本身需要串行化。
- 加载 Chat 时至少保留 4 GB 可用内存，加载 VL 时至少保留 2 GB；内存不足时按既有规则卸载另一类模型。
- VL 必须加载配对的 mmproj 文件。
- 退出时清理所有由服务端启动的模型进程；异常重启不得无限重试。

### 数据一致性

- 所有持久化运行数据集中在 `history/`，不要写入其他位置。
- 普通回复只有在完整成功后才保存；停止、断流或失败时保留用户消息，但不保存不完整回复及统计。
- 图片 OCR 成功后立即保存原图和完整 OCR 结果。后续翻译失败或停止，不得破坏已保存的 OCR。
- 不完整 OCR 或翻译不能覆盖此前完整结果。
- 设置、页面模型选择和图像翻译开关由服务端共享；独立罗马音页面的转换模式不持久化。

### 离线与安全

- 页面、字体、词典和运行依赖均使用本地资源，不增加 CDN 或在线 API。
- 固定翻译场景只允许需求指定的两个 Chat 模型；自由 Chat 可使用 `gguf/chat/` 中的其他 Chat 模型。
- 默认仅监听本机。`-lan` 是无认证模式，不要把它描述为安全的多用户系统。
- 不开放自定义模型路径、llama-server 路径或端口等需求明确排除的配置。

## 持久化文件

| 路径 | 内容 |
| --- | --- |
| `history/settings.json` | 主题、线程、上下文、生成参数和页面偏好 |
| `history/conversations.json` | 各普通页面的独立对话历史 |
| `history/images.json` | 图片任务元数据、OCR 和翻译结果 |
| `history/images/` | 原始图片 |
| `history/chat-server.log` | Chat llama-server 输出 |
| `history/vl-server.log` | VL llama-server 输出 |

JSON 写入采用临时文件加重命名。修改存储结构时应考虑已有数据读取，避免无提示破坏用户历史。

## 扩展时的检查点

新增接口时同时检查：路由、请求校验、前后端类型、全局忙碌状态、停止传播、持久化时机和测试。新增页面或资源时同时检查：完全离线、全局模型状态、资源监控、全部主题以及 CSP。

新增需求不应悄悄改变“不排队”“不自动裁剪上下文”“不保留不完整结果”等核心约束。需求未决事项记录在 `REQUIREMENTS.md` 第 19 节，实现前需要确认，而不是自行假定。
