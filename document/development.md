# 开发与构建

本文面向 EasyWork 的开发者。用户安装与启动方式见 [README](../README.md)。

## 工具链

- Windows 11 x64 是目标运行平台。
- 后端使用 Go，模块声明为 Go 1.24，工具链版本为 Go 1.24.2。
- 前端使用 React 19、TypeScript 和 Vite 6，依赖版本以 `frontend/package-lock.json` 为准。
- 不需要 Python、数据库或其他服务端组件。WASAPI 采集由纯 Go 调用 Windows COM 接口完成，不使用 CGo、浏览器采集或 FFmpeg。
- 真实 Chat/VL 验证需要本地 llama.cpp build 11982 和匹配的 GGUF 文件。当前已验证该版本支持生成请求与 `/apply-template` 的 `chat_template_kwargs`；若改用 b11898，必须先重新验证关闭 thinking 的接口。
- 真实连续语音验证还需要 `whispercpp/whisper-server.exe`、其运行依赖和 `gguf/whisper/` 下的多语言 Whisper 模型。浏览器不直接访问 whisper-server。

## 常用命令

以下命令在仓库根目录执行。

```powershell
# 后端单元与集成测试（使用 mock llama-server、mock Whisper 和合成 PCM，不需要真实模型）
go -C backend test ./...

# 后端静态检查
go -C backend vet ./...

# 标准构建：测试、前端构建、后端编译、组装 local/build/
.\build.ps1

# 构建后以 7-Zip 最高压缩级别生成 EasyWork.7z
.\build.ps1 -Package

# 启动构建结果
.\local\build\EasyWork.exe -no-browser
```

前端单独检查或构建：

```powershell
Set-Location frontend
npm ci
npm run build
```

`npm run dev` 只启动 Vite 页面服务；当前 Vite 配置没有后端 API 代理，因此它不是完整的联调启动方式。完整验证应通过 Go 服务端访问构建后的页面。

## 构建产物

`build.ps1` 会生成：

```text
local/build/
├── EasyWork.exe
├── html/
├── history/
└── log/
```

脚本会保留 `local/build/gguf/`、`local/build/llamacpp/` 和 `local/build/whispercpp/`，但不会将它们放入发布压缩包，也不会自动复制模型或模型服务器。首次运行前需手动准备这些资源；Index 端到端验证前，确认所需 Index 文件位于 `local/build/gguf/chat/`；Index 图像识别还需将 `Index-Translate-9B.mmproj-Q8_0.gguf` 放入 `local/build/gguf/vl/`。

连续语音还需手动准备：

```text
local/build/
├── gguf/
│   └── whisper/
│       └── ggml-small.bin       # 或其他多语言 .bin/.gguf 模型
└── whispercpp/
    ├── whisper-server.exe
    └── ...                      # 所需 DLL 和其他 whisper.cpp 文件
```

**构建会清理 `local/build/` 中除 `gguf/`、`llamacpp/` 和 `whispercpp/` 外的内容，包括 `history/` 中的运行数据和 `log/` 中的日志。** 重新构建前请备份需要保留的内容；新增 whispercpp 保留规则不表示会保留运行数据。

使用 `.\build.ps1 -Package` 会在构建成功后通过 7-Zip 的最高压缩级别（`-mx=9`）生成 `local/EasyWork.7z`，其中仅包含 `EasyWork.exe`、`html/` 和空的 `history/`。`llamacpp/`、`whispercpp/` 和 `gguf/` 均由用户按目录结构另行复制。脚本优先使用 `PATH` 中的 `7z`，否则尝试默认安装路径 `%ProgramFiles%\7-Zip\7z.exe`。

## 本地运行根目录

服务端默认以 `EasyWork.exe` 所在目录为运行根目录。开发时可通过 `EASYWORK_ROOT` 指定包含 `html/`、`gguf/`、`llamacpp/`、`whispercpp/`、`history/` 和 `log/` 的目录，避免依赖可执行文件位置。没有真实 Whisper 资源时仍可运行其他页面和 mock 测试，但不能进行真实连续语音验收。

PowerShell 示例：

```powershell
$env:EASYWORK_ROOT = (Resolve-Path .\local\build)
Set-Location backend
go run . -no-browser
```

该方式仍要求 `local/build/html/` 已存在，因此先完成一次标准构建，或在测试运行目录中手动复制 `frontend/dist/`。

## 修改代码时

1. 先阅读 [需求基线](../REQUIREMENTS.md) 和 [架构说明](architecture.md)，不要只按页面表象推断约束。
2. 后端接口与数据结构变更时，同步修改 `backend/types.go` 和 `frontend/src/types.ts`。
3. 修改 SSE 事件时，同步检查后端发送端、`frontend/src/api.ts` 的解析以及 `frontend/src/App.tsx` 或对应视图的事件处理。音频事件由 `AudioView.tsx` 消费，不完整草稿不能显示成完整译文。
4. 修改构建产物或目录结构时，同步更新 README、本文和架构文档。
5. 不引入在线资源；新增前端依赖必须能随构建产物完全离线运行。
6. 沿用现有代码风格。当前不少源码采用紧凑格式，不要仅为格式化制造大规模无关变更。
7. 连续音频改动必须同时检查录音与处理状态、识别优先队列、停止传播、临时 WAV、完整结果持久化和退出时 SSE/采集关闭。

## 验证

每次交付至少执行：

```powershell
go -C backend test ./...
Set-Location frontend
npm ci
npm run build
```

完整构建改动应直接运行 `.\build.ps1`，但它会按上述规则清理运行数据和日志，执行前必须备份。涉及真实交互时，还应在 Windows 上启动 `local/build/EasyWork.exe`，验证对应页面、API、模型加载与停止流程。

后端测试通过 `httptest`、mock llama-server、mock Whisper、辅助进程和合成 PCM 覆盖核心行为，不要求下载模型。辅助进程可以验证受控终止语义，但不能替代真实 whisper-server。mock 测试不能替代以下真实环境验证：

- 大模型加载、内存预留和 Chat／VL 切换。
- llama-server 与 whisper-server 崩溃恢复和退出清理。
- 实际 GGUF 元数据和 Chat Template。
- 图片 OCR、流式中断与性能统计。
- 麦克风、系统 loopback、双源混合、不同声卡格式和长时间会议时钟漂移。
- Whisper small 的识别质量、实际延迟、Index-Translate-2B Q4/Q3 的翻译质量与速度。
- 连续会议中原文优先、待翻译补全、停止录音与停止整个会话的实际体验。
- 离线环境中的页面、本地词典、SSE 视觉状态和键盘交互。

如果无法执行真实模型、Windows 音频或浏览器验证，应明确说明未验证范围，不能把 TypeScript 构建或 mock 结果描述为端到端通过。

### Index 本地评测

当前 instTrans「通用硬约束 + 场景约束」Prompt 已固定在 tracked 文件 `backend/prompts_index.go`，产品运行时不读取分析目录。16/12/47 条评测 CSV 含工作内容，只保留在已忽略的 `local/chatgpt-analysis/`，不得复制到 `backend/testdata/` 或提交到 Git。`backend/index_eval_test.go` 直接读取该本地目录；常规 Go 测试在数据存在时校验用例和 Prompt 包装，不存在时跳过。

需要真实模型评测时，先在一个终端启动已加载 Index Q4 或 Q5 的 llama-server，再在仓库根目录执行：

```powershell
$env:EASYWORK_INDEX_EVAL_SERVER = 'http://127.0.0.1:18080'
go -C backend test -run '^TestIndexTranslateEval$' -v
```

未设置 `EASYWORK_INDEX_EVAL_SERVER` 时真实请求测试跳过。该测试使用生产 Prompt、贪心参数及 `enable_thinking=false`，依次检查 16 条硬规则、12 条留出用例和 47 条回归用例；47 条中的自动断言门槛为 28/33，人工项另行复核，mock 通过不能替代此验证。

## 版本库边界

`local/`、前端依赖和生成词典均不应提交。模型、原始图片、音频会议、临时 WAV、对话、日志和评测用例可能很大或包含敏感信息，调试时也不要把它们附入补丁。
