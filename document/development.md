# 开发与构建

本文面向 EasyWork 的开发者。用户安装与启动方式见 [README](../README.md)。

## 工具链

- Windows 11 x64 是目标运行平台。
- 后端使用 Go，模块声明为 Go 1.24，工具链版本为 Go 1.24.2。
- 前端使用 React 19、TypeScript 和 Vite 6，依赖版本以 `frontend/package-lock.json` 为准。
- 不需要 Python、数据库或其他服务端组件。
- 真实模型验证还需要本地 llama.cpp b11898 和匹配的 GGUF 文件。

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

脚本会保留 `build/gguf/` 和 `build/llamacpp/`，但不会将它们放入发布压缩包。首次运行前需手动复制这两组资源。

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

## 版本库边界

`build/`、`gguf/`、`llamacpp/`、`history/`、`log/`、前端依赖和生成词典均不应提交。模型、原始图片、对话和日志可能很大或包含敏感信息，调试时也不要把它们附入补丁。
