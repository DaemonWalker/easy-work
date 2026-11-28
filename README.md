# EasyWork

EasyWork 是一个完全离线运行的 Windows 本地翻译、图像识别与连续语音翻译工具。它通过 Go 服务端统一管理 llama.cpp、whisper.cpp 和本机 WASAPI 音频采集，并在浏览器中提供中日互译、图片 OCR、自由对话、连续会议字幕和日文罗马音转换等功能。

## 功能

- **中译日**：在统一入口中通过下拉框选择同事交流、设计书／式样书、Test Case、Git Commit 或代码注释场景，各场景保留独立历史和模型选择；使用 Index-Translate 时可自动或手动二次校验译文。
- **日译中**：将日文翻译为自然、准确的中文。
- **图像识别翻译**：上传图片提取文字，可选择继续翻译成中文；保留原图和历史结果。
- **连续语音识别翻译**：持续采集电脑内声音、麦克风或两者混音，按语音段先显示日文原文，再流式补入中文；自动保存完整会议录音，支持下载、删除、大 Whisper 会后转录和另选模型翻译，实时结果与高精度结果分版本保留。
- **自由 Chat**：支持自定义 Prompt、多轮对话、消息编辑和重新生成。
- **罗马音**：使用本地 Kuroshiro 和词典转换，不调用模型，也不消耗 Token。
- **模型与资源管理**：按需加载或卸载 Chat／VL 模型，显示 Whisper 实际状态、CPU、内存、Token、生成速度和耗时。
- **本地持久化**：对话、图片任务、完整音频会议结果和设置统一保存在运行目录的 `history/` 中。

## 运行环境

- Windows 11 x64。
- 建议至少 4 核 CPU、16 GB 内存；无需 GPU。
- Windows 桌面版 Edge 或 Chrome。
- 正常运行不需要安装 Go、Node.js、Python、FFmpeg、C/C++ 编译器或其他服务端运行时。
- 音频由纯 Go 调用 Windows WASAPI，不使用浏览器共享屏幕或浏览器麦克风采集。
- 程序不依赖 CDN、在线字体、在线词典或其他外部服务。

## 快速开始

发布包只包含程序、页面和本地词典，**不包含模型、llama.cpp 或 whisper.cpp**。首次运行前，需要自行准备这些文件。

### 1. 准备运行目录

将发布内容以及运行资源整理为以下结构：

```text
EasyWork/
├── EasyWork.exe
├── html/                         # 页面、脚本和本地 Kuroshiro 词典
├── history/                      # 自动创建；保存对话、图片、音频会议和设置
├── log/                          # 自动创建；保存后端和模型服务器每日日志
├── gguf/
│   ├── chat/
│   │   ├── Hy-MT2-7B-Q4_K_M.gguf
│   │   ├── HY-MT2-7B-Q6_K.gguf
│   │   ├── Index-Translate-9B.Q4_K_M.gguf  # 可选，中译日推荐
│   │   ├── Index-Translate-9B.Q5_K_M.gguf  # 可选，更大、更慢
│   │   ├── Index-Translate-2B.Q4_K_M.gguf  # 可选，连续语音翻译首选候选
│   │   └── Index-Translate-2B.Q3_K_M.gguf  # 可选，真实验证后决定
│   ├── vl/
│   │   ├── PaddleOCR-VL-1.6-GGUF.gguf
│   │   ├── PaddleOCR-VL-1.6-GGUF-mmproj.gguf
│   │   └── Index-Translate-9B.mmproj-Q8_0.gguf  # 可选，配合 chat 中的 Index Q4/Q5
│   └── whisper/
│       ├── ggml-small.bin        # 多语言 Whisper small，实时首选候选
│       └── ggml-large-v3-turbo.bin  # 可选，会后高精度转录；也可使用其他 .bin/.gguf
├── llamacpp/
│   ├── llama-server.exe
│   └── ...                       # llama-server 所需 DLL 和其他文件
└── whispercpp/
    ├── whisper-server.exe
    └── ...                       # whisper-server 所需 DLL 和其他文件
```

VL 模型与对应的 mmproj 文件必须同时存在。Index VL 直接复用 `gguf/chat/` 中的 Q4/Q5 主模型，无需复制到 `gguf/vl/`。当前实现以 llama.cpp **build 11982** 为基准，已验证生成和 `/apply-template` 的 `chat_template_kwargs`；若改用 b11898，需先验证关闭 thinking 的接口。

Index-Translate 可用于五个中译日场景、日译中、图像识别和图像翻译，固定使用对应方向与场景的 Prompt、贪心解码并关闭 thinking。中译日页面的“自动校验”开关会在译文完成后再次调用 Index 检查并修订；关闭开关时，可点击译文下方的对勾图标手动校验。校验结果独立显示，不覆盖原译文。图像页分别记住识别模型与翻译模型，识别模型默认仍为 PaddleOCR-VL。接入 Index 不会修改已有模型选择，对话在首条消息后固定模型，换模型需新建对话。

连续语音翻译还可选择 Index-Translate-2B Q4_K_M 或 Q3_K_M；Q4 是否过慢、Q3 是否满足质量要求必须以真实会议验证为准。2B 的音频候选不会扩大普通翻译页面的模型范围。Whisper 固定从 `whispercpp/whisper-server.exe` 按需启动，仅监听本机 `18183`；浏览器只访问 EasyWork 的同源 API。

### 2. 启动

双击 `EasyWork.exe`，程序会启动本地服务并自动打开默认浏览器。也可以手动访问：

```text
http://127.0.0.1:18765
```

模型仅在发送翻译、对话、图像任务、开始连续语音处理或启动会后转录／翻译时按需加载，首次请求可能需要较长时间。关闭服务端时，程序会停止音频会话和会后任务，并清理由它启动的 llama-server 和 whisper-server 进程。

### 3. 连续语音会议

1. 打开“语音会议”，选择电脑内声音、麦克风或“电脑内声音＋麦克风”，再选择 Whisper 和日译中模型。
2. 点击“开始连续录音”。系统声音是默认系统混音，不是 Zoom 专属采集；局域网客户端的声音不会被本机 WASAPI 采集。
3. 会议按语音段连续处理。日文原文完整识别后立即显示；中文处于“待翻译”或“翻译中”时不表示已有完整译文，只有标记“完整”的内容才已保存为最终译文。
4. 翻译落后时优先识别积压原文，再按顺序补译。页面分别显示待识别、待翻译数量和实际延迟。
5. “停止录音，继续处理”只结束采集，已采集内容仍继续识别和翻译；“停止整个会话”会取消当前处理并保留已完成结果。
6. 对停止或失败的未完成会议，可点击“会后补全”。当前补全支持同一后端进程中的活动会议；服务端重启后完整历史仍可查看，但不会恢复未完成的实时采集队列。
7. 采集的全量音频（包括静音）会流式保存为完整录音，与实时临时语音段分开保留。页面显示录音时长和大小，可下载或删除；删除前会二次确认，已生成文本不受影响。
8. 历史会议可单独选择更大的 Whisper 并点击“开始高精度转录”。系统从完整录音重新分段，逐段保存新原文，不覆盖实时结果；服务端重启后仍可对已有录音执行此操作。
9. 在“结果版本”中选择高精度版本，可另选日译中模型并点击“翻译此版本”。高精度转录和翻译分别执行，停止或失败后只保留完整段落；未完成翻译可继续，已完成译文不会重复生成。

### 4. 可选启动参数

在 PowerShell 中进入程序目录后运行：

```powershell
# 不自动打开浏览器
.\EasyWork.exe -no-browser

# 允许局域网内其他设备访问
.\EasyWork.exe -lan
```

`-lan` 模式没有登录或访问令牌，仅应在可信网络中使用。外部服务端口固定为 `18765`，不支持自定义。

## 键盘操作

点击顶部的 `?` 按钮，或按 `F1`／`Ctrl+/`，可查看完整快捷键帮助。所有控件均可通过 `Tab` 和 `Shift+Tab` 到达，使用 `Enter`／`Space` 激活；下拉框使用方向键选择。

| 快捷键 | 功能 |
| --- | --- |
| `Alt+1`～`Alt+6` | 切换六个功能页面，语音会议为第 6 页 |
| `Alt+7` | 打开设置／返回之前页面 |
| `Alt+I` | 聚焦当前输入框或语音页音源选择 |
| `Alt+M` | 打开／关闭实际模型状态 |
| `Ctrl+Enter` | 发送消息／保存编辑并重新发送 |
| `Ctrl+.` | 停止当前模型任务或整个语音会话 |
| `Alt+N` | 新建对话／上传图片／新建语音会议 |
| `Alt+B` | 收起／展开历史侧栏 |
| `Alt+E` | 编辑最后一条用户消息 |
| `Alt+R` | 重新生成最后回复；语音页会后补全 |
| `Alt+S` | 语音页开始录音／停止录音并继续处理 |
| `Alt+X` | 停止整个语音会话 |
| `Alt+U` | 上传图片并识别 |
| `Alt+O` | 重试当前图片的 OCR |
| `Alt+T` | 翻译／重新翻译当前 OCR 结果 |
| `Esc` | 关闭帮助或模型详情／取消消息编辑 |

快捷键只执行当前页面中可用的操作，不会绕过任务忙碌或模型锁定状态。页面顶部还提供“跳到主要内容”链接，图片区域可直接用 `Enter`／`Space` 打开文件选择。

## 图片限制

- 支持 PNG、JPEG、WebP，可通过文件选择、拖放或剪贴板粘贴上传。
- 每个任务仅包含一张图片。
- 原始文件最大 4 MB。
- 横图最大 `3840×2160`，竖图最大 `2160×3840`。
- 图片不会压缩、缩放或转换格式。

## 数据与备份

业务运行数据位于 `history/`：

- `settings.json`：主题、生成参数和各页面偏好，包括音源与语音模型选择。
- `conversations.json`：普通对话历史。
- `images.json`、`images/`：图片任务和原始图片。
- `audio.json`：会议、完整录音元数据、实时及高精度版本的完整日文原文、完整中文译文、时间码和完整统计。
- `audio/<会议>/recording.wav`：包含静音的完整会议录音，独立保留直至主动删除；16 kHz、单声道、16 位 PCM 约为 115 MB/小时。
- `audio/<会议>/<语音段>.wav`：实时识别积压、失败、停止或待翻译时的临时 WAV；完整翻译后删除，不完整流式结果不会写入音频历史。

日志位于 `log/`，同时输出到控制台：

- `easywork-YYYY-MM-DD.log`：后端关键操作、错误、完整用户输入和模型输出。
- `chat-server-YYYY-MM-DD.log`、`vl-server-YYYY-MM-DD.log`：对应 llama-server 的 stdout/stderr。
- `whisper-server-YYYY-MM-DD.log`：whisper-server 的 stdout/stderr。

日志按本地日期每日拆分并永久保留，不自动清理。日志无法写入文件时，程序会报告警告并继续使用控制台输出。

备份或迁移时复制整个 `history/`；需要保留日志时同时复制 `log/`。这些目录可能包含私人对话、图片、会议内容和模型输出，不应提交到版本库或公开分享。

## 从源码构建

需要 Go 1.24.2（或兼容版本）、Node.js 和 npm。在仓库根目录的 PowerShell 中执行：

```powershell
.\build.ps1
```

脚本会依次运行 Go 测试、安装前端锁定依赖、构建前端，并生成免安装目录 `local/build/`。脚本不会自动复制新模型或模型服务器；随后按“准备运行目录”的结构，将 `local/gguf/`、`local/llamacpp/` 和 `local/whispercpp/` 复制到 `local/build/`，再运行：

```powershell
.\local\build\EasyWork.exe
```

构建会保留 `local/build/gguf/`、`local/build/llamacpp/` 和 `local/build/whispercpp/`，但会清理 `local/build/` 中的其他内容，**包括 `history/` 运行数据和 `log/` 日志**。重新构建前请备份需要保留的内容；保留 whispercpp 不改变运行数据清理规则。

如需同时生成发布压缩包，请安装 7-Zip 并执行：

```powershell
.\build.ps1 -Package
```

构建成功后会在 `local/EasyWork.7z` 生成发布压缩包，使用最高压缩级别，仅包含 `EasyWork.exe`、`html/` 和空的 `history/`。`gguf/`、`llamacpp/` 和 `whispercpp/` 不包含在发布包中。

更多开发命令见 [开发指南](document/development.md)。

## 项目结构

```text
backend/        Go HTTP 服务、音频与模型进程管理、持久化与测试
frontend/       React + TypeScript + Vite 页面
local/          本地构建产物、运行资源和分析资料（不纳入版本库）
document/       面向开发与维护的详细文档
REQUIREMENTS.md 完整需求基线
```

## 文档

- [开发与构建](document/development.md)
- [架构与关键约束](document/architecture.md)
- [AI 编码代理指南](AGENTS.md)
- [完整需求](REQUIREMENTS.md)
