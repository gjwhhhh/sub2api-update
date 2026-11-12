# Codex Candy Eval 定制分支维护手册

更新日期：2026-11-13。适用于长期分支 `custom`。目前分支从 Sub2API `v0.2.21`（`10ba8f9b4dc2e6023d4a9879ab77b21841babd10`）迁入 Candy Eval commit `bae76a3533536688afce550458ec7ec2ab307342`。本地 `origin` 仍指向公共上游；推送和发布前要先配置自有仓库。

## 分清两条更新线

Sub2API 更新与 Candy Eval 更新分开评估、分别记录，最后在 `custom` 汇合。两边都以普通源码形式维护；升级宿主时不重复导入 Candy Eval 的初始补丁，升级 Candy Eval 时也不覆盖已经适配和定制的整个模块。

### 合并新的 Sub2API tag

1. 拉取上游 tag，确认目标版本及对应 commit，并记录 `custom` 当前提交，确保工作区干净。
2. 从 `custom` 创建短期验证分支（例如 `upgrade/sub2api-v0.2.22`），把上游 tag 合并进来。不要 reset 或 rebase 长期分支到上游 tag。
3. 处理并审阅冲突。重点看 `internal/codexeval` 的调用契约、账户请求适配、账户受管字段、管理权限和二次验证、路由注册、启动/停止、前端入口、通用 HTTP 错误处理，以及版本和发布配置。无文本冲突也要验证行为。
4. 执行 Go 单元/集成测试、`go test -race ./internal/codexeval`、前端检查和构建、Candy Eval DOM 测试、release helper 测试及完整构建。检查自动巡检退出清理和账户写回行为。
5. 用单实例测试环境对选定账户做真实请求验证后，把验证分支合回 `custom`；在最终合并提交上重新运行必要检查。保留前一版镜像、二进制、数据和账户字段快照。

宿主的小版本更新通常是合并和回归成本；冲突集中在上述少量接入点。Sub2API 改动评估、账户模型或权限/路由变化时，需要更仔细审阅，不能只依据 Git 是否提示冲突判断安全。

### 跟进 Candy Eval 上游

1. 查看 `third_party/codex-candy-eval/source.json` 记录的仓库、commit 和版本；确认新 commit 的变更及许可证。
2. 在临时目录展开新旧来源并比较差异，区分上游修复、功能变化和我们不准备采用的行为。
3. 逐项移植到 `backend/internal/codexeval` 及其测试；需要宿主接口变化时同步更新适配器。不要把新版 patch 整套覆盖到模块目录。
4. 更新来源元数据和第三方声明，补充针对变更的回归，然后执行本手册中的相关测试。

宿主 tag 和来源 commit 都记录在发布说明与来源清单中，使每个定制版本可追溯。

## 自定义开发约定

- 新评估逻辑、巡检、历史和内嵌 UI 放在 `backend/internal/codexeval/`；与宿主交互尽量通过 `Sender`、`Resolver`、`HistoryStore` 等窄接口。
- 宿主账户、路由、启动停止和页面入口的改动保持薄，并在提交说明中解释必要性及测试覆盖。
- 将上游导入、缺陷修复/兼容层、模块化和新定制功能分开提交，减少升级时的冲突面。
- 不把核心改动写回原始 patch 文件；`third_party/codex-candy-eval/` 只用于来源、许可证和可追溯信息。
- 不把来源项目的安装器用于当前服务；始终通过本项目的完整构建和发布流程交付。

## 构建、测试与发布

本地验证至少包括：

```sh
cd backend && make test-unit
cd backend && make test-integration
cd backend && go test -race ./internal/codexeval
make test-frontend
pnpm --dir frontend run build
python -m unittest discover -s .github/release-tools -p 'test_release_matrix.py'
bash -n .github/release-tools/release-images.sh
```

完整 CI 还包含部署脚本检查和 golangci-lint。使用 `backend/go.mod` 指定的 Go 版本、CI 指定的 pnpm/Node 版本运行验证。发布前还要用 workflow dry-run 检查目标仓库的权限、制品和镜像构建。

定制版本使用 `custom-vX.Y.Z.N` tag。例如 `custom-v0.2.21.1` 会生成制品/镜像版本 `0.2.21-custom.1`，服务器版本为 `0.2.21+custom.1`。宿主升级至 `0.2.22` 后，首个定制 revision 可用 `custom-v0.2.22.1`。从 `custom` 的已验证提交打 tag；确认 workflow 运行在自有 fork，避免使用公共上游仓库或官方命名空间。

定制镜像使用 `custom-latest`，不移动官方 `latest`；定制服务器的内置官方更新与回退入口会拒绝操作。升级时发布并部署自有仓库构建的定制镜像/二进制，不能换成上游官方产物后期待模块仍存在。不要给定制提交打普通 `vX.Y.Z` tag，以免进入官方版本命名和镜像别名流程。

## 部署限制、数据保护与回退

目前只支持单实例。活动任务、取消句柄和自动巡检在内存中；历史写到配置的 `DataDir/codex-eval/history.json`。部署时持久挂载对应目录，禁止在多副本环境启用此功能，除非另行实现任务协调与存储一致性。

自动巡检可以更改账户优先级、模型映射和 Extra 字段。变更前先确认巡检状态，并导出受影响账户相关字段快照；遇到账户并发编辑或回退时，程序回滚不会自动撤销已经写入的账户数据。正式启用前用少量明确选定的账户验证。

升级前备份历史文件、巡检设置和账户快照，并保留上一版定制镜像/二进制。回退时先停止巡检和服务，部署上一版，再判断是否需要恢复历史与设置；仅恢复程序不能恢复账户字段。历史升级会兼容旧格式并创建旧版本备份，回退数据前先确认目标版本可读取该格式。

生产烟测应限定单实例和少量账户，记录实际请求、额度消耗及账户字段变化。未完成真实环境烟测的源码构建不等于已通过生产验收。
