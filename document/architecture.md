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
    ├── WASAPI 连续音频采集与语音分段
    ├── 启动、监控并代理 llama-server
    │            ├── Chat：127.0.0.1:18181
    │            └── VL：  127.0.0.1:18182
    └── 启动、监控并代理 whisper-server
                 └── Whisper：127.0.0.1:18183
```

浏览器不直接访问 llama-server 或 whisper-server。对外服务默认绑定 `127.0.0.1:18765`，使用 `-lan` 时绑定 `0.0.0.0:18765`；三个模型端口仅用于本机进程间通信。

源码仓库中的构建产物、模型、运行数据和本地分析资料统一放在已忽略的 `local/`。服务端仍以 `EasyWork.exe` 所在目录为运行根目录，因此开发构建的运行根目录是 `local/build/`，发布版内部的相对路径不变。

## 后端职责

后端是一个 `main` 包，主要文件如下：

| 文件 | 职责 |
| --- | --- |
| `backend/main.go` | 启动参数、监听地址、浏览器打开和退出清理 |
| `backend/logging.go` | 控制台与文件双写、按天切换和文件不可用时的控制台降级 |
| `backend/api.go` | HTTP 路由、参数校验、对话与模型 API |
| `backend/audio_api.go` | 连续音频模型、会议、会话操作和 SSE API |
| `backend/models.go` | Chat/VL 模型扫描、内存检查、加载／卸载、健康检查与崩溃恢复 |
| `backend/whisper.go` | Whisper 模型扫描、独立进程监管、推理、停止与崩溃恢复 |
| `backend/stream.go` | llama.cpp 流式响应、统计信息与 SSE 转发 |
| `backend/images.go` | 图片校验、OCR、重试与翻译流程 |
| `backend/audio.go` | 连续会议、识别优先队列、顺序补译、补全、订阅和会话清理 |
| `backend/audio_capture.go` | PCM 转换、混音、WAV 和连续语音分段 |
| `backend/audio_recording.go` | 完整会议流式 WAV、异常退出恢复和录音分段读取 |
| `backend/audio_transcription.go` | 独立高精度转录版本、会后翻译和录音删除 |
| `backend/audio_recovery.go` | 启动时恢复未完成录音及会后任务状态 |
| `backend/audio_wasapi_*.go` | 纯 Go Windows WASAPI 系统 loopback、麦克风与混合采集 |
| `backend/task.go` | 全局任务状态、互斥和取消 |
| `backend/store.go` | 设置、对话、图片任务和音频会议的 JSON 持久化 |
| `backend/gguf.go` | GGUF 元数据读取 |
| `backend/process_*.go` | 平台相关的进程管理；Windows 使用 Job Object 清理子进程 |
| `backend/hardware_*.go` | CPU 与内存采样 |

WASAPI 由 Go 直接通过 Windows COM 接口调用，不使用浏览器采集、CGo、Python 或 FFmpeg。系统声音是默认渲染设备的系统混音，不是 Zoom 专属采集，也不采集局域网客户端。音频统一转换为 16 kHz 单声道 PCM WAV；连续采集经过预卷和静音端点检测形成多个语音段，不是单段录音上传。同一全量 PCM 同时流式写入完整会议 WAV，包含静音，正常停止时封装文件，异常退出后下次启动尝试恢复。

## 前端职责

- `frontend/src/App.tsx` 负责全局状态、页面与中译日场景切换、轮询、任务事件分发和全局快捷键；五个中译日场景共用一个一级入口，分别使用独立场景 ID 读取历史与模型偏好。
- `frontend/src/components/` 包含普通对话、图片、设置、模型状态、统计、快捷键帮助及独立 `AudioView.tsx` 连续语音页面。
- `frontend/src/api.ts` 负责同源 HTTP 请求及 POST/GET SSE 解析。
- `frontend/src/romaji.ts` 使用 Kuroshiro 在浏览器本地转换罗马音。
- `frontend/scripts/copy-dict.mjs` 在开发或构建前，将 Kuromoji 词典复制到静态资源目录。

页面每 2 秒刷新服务端状态。普通回复通过 SSE 流式显示，罗马音在完整日文回复到达后于浏览器生成。Index 中译日自动校验使用同一 SSE 连接中的 `translation_complete`、`review_start`、`delta` 和 `complete` 事件切换两个阶段；手动校验复用独立 `/api/chat/{id}/review` 接口。

连续语音页面通过 `/api/audio/sessions/{id}/events` 接收 `snapshot`、`state`、`recognition_start`、`recognized`、`translation_start`、`translation_delta`、`translated`、`transcription_recognized`、`transcription_translation_start`、`transcription_delta`、`transcription_translated`、`error` 和 `complete`。`recognized` 先提供完整日文原文；`translation_delta` 和 `transcription_delta` 只进入明确标记的未完成草稿；只有 `translated` 或 `transcription_translated` 才能把中文标记为完整译文。SSE 断开时前端每 2 秒重连，离开页面不会因此停止后端采集或会后处理。

## 必须保持的运行约束

### 全局互斥

所有翻译、自由 Chat、OCR、图像翻译、连续音频识别／补全、高精度转录及高精度结果翻译共享一个全局任务管理器。同一时刻最多执行一个模型任务；忙碌时直接拒绝新任务，**不排队，也不并行推理**。

连续会议持有全局任务直至录音和处理结束或用户停止。会话内识别队列优先于翻译队列，但 Whisper 与 Chat 仍严格顺序执行：翻译落后时先完成积压原文，中文按段后补；识别落后时继续采集，不主动丢弃旧段。录音状态与模型处理状态分别展示。

停止操作必须取消服务端上下文并传播到实际模型请求。停止 Whisper 时终止当前 whisper-server，先标记预期退出，不能误触发崩溃恢复；Chat 继续使用 Context 取消。只停止录音会保留处理队列并继续补全，停止整个会话才同时取消采集与处理。

### 模型生命周期

- 选择模型、切换页面或查看历史不应触发加载。
- 只有真正执行任务时才加载或切换模型。
- 最多保留一个 Chat 和一个 VL 进程，同类模型不能同时加载多个；Whisper 由独立管理器加载一个模型。
- Chat 和 VL 分别管理状态与卸载，但模型切换操作本身需要串行化。Whisper 不套用 Chat/VL 卸载接口。
- 启动 Chat 前可用内存低于 4 GB 时卸载已加载的 VL，启动 VL 前可用内存低于 2 GB 时卸载已加载的 Chat。内存阈值只用于模型切换，不会因内存不足拒绝加载。
- VL 必须加载配对的 mmproj 文件。
- whisper-server 固定从 `whispercpp/whisper-server.exe` 启动并监听 `127.0.0.1:18183`，健康检查使用 `/health`，WAV 识别使用 `/inference`。
- 退出时先停止音频会话、等待采集和处理退出、关闭音频 SSE，再清理所有由服务端启动的 Whisper/Chat/VL 进程；异常重启不得无限重试。

### 数据一致性

- 持久化业务运行数据集中在 `history/`，日志独立保存在 `log/`。
- 普通回复只有在完整成功后才保存；停止、断流或失败时保留用户消息，但不保存不完整回复及统计。
- Index 中译日自动校验先保存完整译文，再发起第二次请求；校验停止或失败不得破坏译文，不完整校验结果及统计不持久化。校验消息不进入后续普通翻译上下文。
- 图片 OCR 成功后立即保存原图和完整 OCR 结果。后续翻译失败或停止，不得破坏已保存的 OCR。
- 不完整 OCR 或翻译不能覆盖此前完整结果。
- 音频会议先保存完整识别原文、时间码和识别统计，再保存完整译文和翻译统计。待翻译、识别失败、流式半截译文及其统计不持久化为完整结果。
- 实时临时 WAV 位于 `history/audio/<meeting>/`，在识别积压、失败、停止或待翻译时保留，完整翻译后删除。会后补全当前只恢复同一后端进程中的活动会话；服务重启后完整会议历史可读，但不重建未完成实时采集队列。
- 完整录音固定为 `history/audio/<meeting>/recording.wav`，独立保留并可下载或主动删除。高精度转录从该文件流式读取并重新分段，支持重启后的历史会议；每次转录是独立版本，不覆盖实时结果。高精度原文完成后可另选 Chat 模型翻译，逐段持久化完整结果，停止或失败不保存半截内容。
- 设置、页面模型选择、音频音源、实时与会后模型选择、图像翻译开关和中译日自动校验开关由服务端共享；独立罗马音页面的转换模式不持久化。

### 离线与安全

- 页面、字体、词典和运行依赖均使用本地资源，不增加 CDN 或在线 API。
- 模型能力按场景判断：五个中译日场景、日译中和图像翻译允许 Hy-MT2 与 Index-Translate 9B Q4/Q5；图像识别可选 PaddleOCR-VL 或 Index-Translate 9B Q4/Q5；自由 Chat 可使用 `gguf/chat/` 中的其他 Chat 模型。Index VL 复用 `gguf/chat/` 中的主模型，并配对 `gguf/vl/Index-Translate-9B.mmproj-Q8_0.gguf`。
- 连续语音翻译除上述日译中模型外，还允许 `Index-Translate-2B.Q4_K_M.gguf` 和 Q3_K_M；该扩展不放宽普通页面的模型白名单。Whisper 模型仅扫描 `gguf/whisper/` 下的 `.bin` 和 `.gguf`。
- Prompt 按「模型 + 场景 ID」路由：五个中译日场景均只发送 User Prompt，不另设 System Prompt；Hy-MT2 使用官方 Style 外壳，Git Commit 与代码注释共用开发文本保真段；Index 中译日使用 `backend/prompts_index.go` 内置的 instTrans 结构，按 `【源文】`、`【约束要求】` 和编号硬约束／软约束组织，开发场景的语义与格式保护为硬约束、文体为软约束，不使用 few-shot 样例；场景规则均为应用层设计。Index 日译中使用独立日译中 Prompt，不复用中译日 Prompt；不读取本地分析目录。
- 对话在首条消息后固定模型；历史用户消息统一按对话模型包装。Index 请求固定贪心解码，并在生成和 `/apply-template` 中传 `chat_template_kwargs={"enable_thinking":false}`，音频 Index 2B 也遵循此规则。
- 默认仅监听本机。`-lan` 是无认证模式，不要把它描述为安全的多用户系统。
- 不开放自定义模型路径、llama-server、whisper-server 路径或端口等需求明确排除的配置。

## 持久化文件

| 路径 | 内容 |
| --- | --- |
| `history/settings.json` | 主题、线程、上下文、生成参数和页面偏好 |
| `history/conversations.json` | 各普通页面的独立对话历史 |
| `history/images.json` | 图片任务元数据、OCR 和翻译结果 |
| `history/images/` | 原始图片 |
| `history/audio.json` | 音频会议、实时及高精度版本、录音元数据、完整原文、完整译文、时间码和完整统计 |
| `history/audio/` | 各会议的完整 `recording.wav`、实时临时 WAV 和会后转录临时文件 |
| `log/easywork-YYYY-MM-DD.log` | 后端关键操作、错误、完整请求与模型输出 |
| `log/chat-server-YYYY-MM-DD.log` | Chat llama-server 输出 |
| `log/vl-server-YYYY-MM-DD.log` | VL llama-server 输出 |
| `log/whisper-server-YYYY-MM-DD.log` | whisper-server 输出 |

JSON 写入采用临时文件加重命名。修改存储结构时应考虑已有数据读取，避免无提示破坏用户历史。

四类日志均同时输出到控制台，按本地日期每日拆分并永久保留。文件创建或写入失败时仅降级到控制台，不影响服务运行。日志包含完整文本，可能含有敏感信息。

## 扩展时的检查点

新增接口时同时检查：路由、请求校验、前后端类型、全局忙碌状态、停止传播、持久化时机和测试。新增页面或资源时同时检查：完全离线、全局模型状态、资源监控、全部主题以及 CSP。

新增需求不应悄悄改变“不排队”“不自动裁剪上下文”“不保留不完整结果”等核心约束。需求未决事项记录在 `REQUIREMENTS.md` 第 19 节，实现前需要确认，而不是自行假定。
