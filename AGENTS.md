# AGENTS.md

本文件是 AI 编码代理的入口，只保留常用规则；详细内容按需阅读链接文档。

## 项目速览

EasyWork 是 Windows 11 x64 上的完全离线中日翻译与图像识别工具：Go 后端提供页面和 API、管理 llama.cpp 进程并持久化数据；React + TypeScript + Vite 构建前端。

## 按需阅读

- 修改功能或交互前：阅读 [完整需求](REQUIREMENTS.md)，它是需求基线。
- 修改架构、API、模型进程或持久化前：阅读 [架构与关键约束](document/architecture.md)。
- 安装、构建、调试或验证前：阅读 [开发指南](document/development.md)。
- 编写用户说明或运行步骤时：参考 [README](README.md)。

## 核心规则

- 保持完全离线，不引入 CDN、在线词典、在线字体或远程 API。
- 所有模型任务全局互斥，不排队、不并行推理；停止必须实际取消推理。
- 仅在任务执行时加载模型，不因选择模型或切换页面而加载。
- 业务运行数据只放在 `history/`，日志放在 `log/`；不得持久化不完整回复、OCR、翻译或其统计。
- 不擅自扩大需求范围；需求未决项先询问，不自行假定。
- 同步维护前后端类型与 SSE 事件处理，避免只修改一端。
- 不提交 `build/`、`gguf/`、`llamacpp/`、`history/`、`log/`、依赖目录或生成词典。
- 修改目录、命令或已记录行为时，同步更新相关文档。

## 常用验证

在仓库根目录执行：

```powershell
go -C backend test ./...
.\build.ps1
```

仅需单独构建前端时，在 `frontend/` 执行 `npm ci` 和 `npm run build`。注意 `build.ps1` 会保留 `build/gguf/` 和 `build/llamacpp/`，但清理 `build/` 中的其他内容，执行前提醒用户备份需要保留的运行数据与日志；使用 `-Package` 可在构建后生成 `EasyWork.7z`。

涉及真实模型、Windows 进程或浏览器交互的改动，应进行端到端验证；无法验证时明确说明，不把 mock 测试当作真实模型验证。
