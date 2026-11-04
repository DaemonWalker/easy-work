# 开发与构建

本文面向 EasyWork 的开发者。用户安装与启动方式见 [README](../README.md)。

## 工具链

- Windows 11 x64 是目标运行平台。
- 后端使用 Go，模块声明为 Go 1.24，工具链版本为 Go 1.24.2。
- 前端使用 React 19、TypeScript 和 Vite 6，依赖版本以 `frontend/package-lock.json` 为准。
- 不需要 Python、数据库或其他服务端组件。
- 真实模型验证还需要本地 llama.cpp build 11982 和匹配的 GGUF 文件。当前已验证该版本支持生成请求与 `/apply-template` 的 `chat_template_kwargs`；若改用 b11898，必须先重新验证关闭 thinking 的接口。

## 常用命令

以下命令在仓库根目录执行。

```powershell
# 后端单元与集成测试（使用 mock llama-server，不需要真实模型）
go -C backend test ./...

# 标准构建：测试、前端构建、后端编译、组装 build/
.\build.ps1

# 构建后以 7-Zip 最高压缩级别生成 EasyWork.7z
.\build.ps1 -Package

# 启动构建结果
.\build\EasyWork.exe -no-browser
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
build/
├── EasyWork.exe
├── html/
├── history/
└── log/
```

脚本会保留 `build/gguf/` 和 `build/llamacpp/`，但不会将它们放入发布压缩包，也不会把新模型从开发目录复制到 `build/gguf/chat/`。首次运行前需手动复制这两组资源；Index 端到端验证前，确认两个 `Index-Translate-9B.*.gguf` 已放入 `build/gguf/chat/`；Index 图像识别还需将 `Index-Translate-9B.mmproj-Q8_0.gguf` 放入 `build/gguf/vl/`。

**构建会清理 `build/` 中除 `gguf/` 和 `llamacpp/` 外的内容，包括运行数据和日志。** 重新构建前请备份需要保留的内容。

使用 `.\build.ps1 -Package` 会在构建成功后通过 7-Zip 的最高压缩级别（`-mx=9`）在仓库根目录生成 `EasyWork.7z`，其中仅包含 `EasyWork.exe`、`html/` 和空的 `history/`。脚本优先使用 `PATH` 中的 `7z`，否则尝试默认安装路径 `%ProgramFiles%\7-Zip\7z.exe`。

## 本地运行根目录

服务端默认以 `EasyWork.exe` 所在目录为运行根目录。开发时可通过 `EASYWORK_ROOT` 指定包含 `html/`、`gguf/`、`llamacpp/`、`history/` 和 `log/` 的目录，避免依赖可执行文件位置。

PowerShell 示例：

```powershell
$env:EASYWORK_ROOT = (Resolve-Path .\build)
Set-Location backend
go run . -no-browser
```

该方式仍要求 `build/html/` 已存在，因此先完成一次标准构建。

## 修改代码时

1. 先阅读 [需求基线](../REQUIREMENTS.md) 和 [架构说明](architecture.md)，不要只按页面表象推断约束。
2. 后端接口与数据结构变更时，同步修改 `backend/types.go` 和 `frontend/src/types.ts`。
3. 修改 SSE 事件时，同步检查后端发送端、`frontend/src/api.ts` 的解析以及 `frontend/src/App.tsx` 的事件处理。
4. 修改构建产物或目录结构时，同步更新 README、本文和架构文档。
5. 不引入在线资源；新增前端依赖必须能随构建产物完全离线运行。
6. 沿用现有代码风格。当前不少源码采用紧凑格式，不要仅为格式化制造大规模无关变更。

## 验证

每次交付至少执行：

```powershell
go -C backend test ./...
Set-Location frontend
npm ci
npm run build
```

完整构建改动应直接运行 `.\build.ps1`。涉及真实交互时，还应在 Windows 上启动 `build/EasyWork.exe`，验证对应页面、API、模型加载与停止流程。

后端测试通过 `httptest` 和 mock llama-server 覆盖核心行为，不要求下载模型。但 mock 测试不能替代以下真实环境验证：

- 大模型加载、内存预留和 Chat／VL 切换。
- llama-server 崩溃恢复与退出清理。
- 实际 GGUF 元数据和 Chat Template。
- 图片 OCR、流式中断与性能统计。
- 离线环境中的页面和本地词典加载。

如果无法执行真实模型验证，应明确说明未验证范围。

### Index 本地评测

当前 instTrans「通用硬约束 + 场景约束」Prompt 已固定在 tracked 文件 `backend/prompts_index.go`，产品运行时不读取分析目录。16/12/47 条评测 CSV 含工作内容，只保留在已忽略的 `chatgpt-analysis/`，不得复制到 `backend/testdata/` 或提交到 Git。`backend/index_eval_test.go` 直接读取该本地目录；常规 Go 测试在数据存在时校验用例和 Prompt 包装，不存在时跳过。

需要真实模型评测时，先在一个终端启动已加载 Index Q4 或 Q5 的 llama-server，再在仓库根目录执行：

```powershell
$env:EASYWORK_INDEX_EVAL_SERVER = 'http://127.0.0.1:18080'
go -C backend test -run '^TestIndexTranslateEval$' -v
```

未设置 `EASYWORK_INDEX_EVAL_SERVER` 时真实请求测试跳过。该测试使用生产 Prompt、贪心参数及 `enable_thinking=false`，依次检查 16 条硬规则、12 条留出用例和 47 条回归用例；47 条中的自动断言门槛为 28/33，人工项仍需复核，mock 通过不能替代此验证。

## 版本库边界

`build/`、`gguf/`、`llamacpp/`、`history/`、`log/`、`chatgpt-analysis/`、前端依赖和生成词典均不应提交。模型、原始图片、对话、日志和评测用例可能很大或包含敏感信息，调试时也不要把它们附入补丁。
