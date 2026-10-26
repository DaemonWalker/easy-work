# EasyWork

EasyWork 是一个完全离线运行的 Windows 本地翻译与图像识别工具。它通过 Go 服务端统一管理 llama.cpp 模型进程，并在浏览器中提供中日互译、图片 OCR、自由对话和日文罗马音转换等功能。

## 功能

- **中译日**：分别提供同事交流、设计书／式样书、Test Case 三种表达风格。
- **日译中**：将日文翻译为自然、准确的中文。
- **图像识别翻译**：上传图片提取文字，可选择继续翻译成中文；保留原图和历史结果。
- **自由 Chat**：支持自定义 Prompt、多轮对话、消息编辑和重新生成。
- **罗马音**：使用本地 Kuroshiro 和词典转换，不调用模型，也不消耗 Token。
- **模型与资源管理**：按需加载或卸载 Chat／VL 模型，显示 CPU、内存、Token、生成速度和耗时。
- **本地持久化**：对话、图片任务和设置统一保存在运行目录的 `history/` 中。

## 运行环境

- Windows 11 x64。
- 建议至少 4 核 CPU、16 GB 内存；无需 GPU。
- Windows 桌面版 Edge 或 Chrome。
- 正常运行不需要安装 Go、Node.js、Python 或其他服务端运行时。
- 程序不依赖 CDN、在线字体、在线词典或其他外部服务。

## 快速开始

发布包只包含程序、页面和本地词典，**不包含模型与 llama.cpp**。首次运行前，需要自行准备这些文件。

### 1. 准备运行目录

将发布内容以及运行资源整理为以下结构：

```text
EasyWork/
├── EasyWork.exe
├── html/                         # 页面、脚本和本地 Kuroshiro 词典
├── history/                      # 自动创建；保存对话、图片和设置
├── gguf/
│   ├── chat/
│   │   ├── Hy-MT2-7B-Q4_K_M.gguf
│   │   └── HY-MT2-7B-Q6_K.gguf
│   └── vl/
│       ├── PaddleOCR-VL-1.6-GGUF.gguf
│       └── PaddleOCR-VL-1.6-GGUF-mmproj.gguf
└── llamacpp/
    ├── llama-server.exe
    └── ...                       # llama-server 所需 DLL 和其他文件
```

VL 模型与对应的 mmproj 文件必须同时存在。当前实现以 llama.cpp **b11898** 为基准。

### 2. 启动

双击 `EasyWork.exe`，程序会启动本地服务并自动打开默认浏览器。也可以手动访问：

```text
http://127.0.0.1:18765
```

模型仅在发送翻译、对话或图像任务时按需加载，首次请求可能需要较长时间。关闭服务端时，程序会清理由它启动的 llama-server 进程。

### 3. 可选启动参数

在 PowerShell 中进入程序目录后运行：

```powershell
# 不自动打开浏览器
.\EasyWork.exe -no-browser

# 允许局域网内其他设备访问
.\EasyWork.exe -lan
```

`-lan` 模式没有登录或访问令牌，仅应在可信网络中使用。外部服务端口固定为 `18765`，不支持自定义。

## 图片限制

- 支持 PNG、JPEG、WebP，可通过文件选择、拖放或剪贴板粘贴上传。
- 每个任务仅包含一张图片。
- 原始文件最大 4 MB。
- 横图最大 `3840×2160`，竖图最大 `2160×3840`。
- 图片不会压缩、缩放或转换格式。

## 数据与备份

所有运行数据均位于 `history/`：

- `settings.json`：主题、生成参数和各页面偏好。
- `conversations.json`：普通对话历史。
- `images.json`、`images/`：图片任务和原始图片。
- `chat-server.log`、`vl-server.log`：模型进程日志。

备份或迁移时复制整个 `history/`。该目录可能包含私人对话与图片，不应提交到版本库或公开分享。

## 从源码构建

需要 Go 1.24.2（或兼容版本）、Node.js 和 npm。在仓库根目录的 PowerShell 中执行：

```powershell
.\build.ps1
```

脚本会依次运行 Go 测试、安装前端锁定依赖、构建前端，并生成免安装目录 `build/`。随后按“准备运行目录”的结构，将 `gguf/` 和 `llamacpp/` 复制到 `build/`，再运行：

```powershell
.\build\EasyWork.exe
```

> **注意：构建脚本会删除并重建整个 `build/` 目录。** 如果你已把模型、llama.cpp 或运行数据放入其中，请先备份，避免被清理。

更多开发命令见 [开发指南](document/development.md)。

## 项目结构

```text
backend/       Go HTTP 服务、模型进程管理、持久化与测试
frontend/      React + TypeScript + Vite 页面
build/         构建生成的免安装运行目录（不纳入版本库）
document/      面向开发与维护的详细文档
REQUIREMENTS.md 完整需求基线
```

## 文档

- [开发与构建](document/development.md)
- [架构与关键约束](document/architecture.md)
- [AI 编码代理指南](AGENTS.md)
- [完整需求](REQUIREMENTS.md)
