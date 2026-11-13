# Codex Candy Eval 定制分支维护手册

更新日期：2026-11-13。适用于长期分支 `custom`。目前分支从 Sub2API `v0.2.21`（`10ba8f9b4dc2e6023d4a9879ab77b21841babd10`）迁入 Candy Eval commit `bae76a3533536688afce550458ec7ec2ab307342`。自有仓库为 `gjwhhhh/sub2api-update`；`origin` 指向自有仓库，`upstream` 指向 `Wei-Shaw/sub2api`。自有仓库默认分支为 `custom`，本地同名分支跟踪 `origin/custom`。

## 远程仓库与流水线配置

- `origin`：`git@github.com:gjwhhhh/sub2api-update.git`，用于定制代码备份、CI 和发布。
- `upstream`：`https://github.com/Wei-Shaw/sub2api.git`，用于获取官方 tag 和合并升级。
- Actions 和 Release workflow 已启用。已核实 `server_tencent` 使用 Docker Compose 和 Linux amd64 镜像；仓库变量 `SIMPLE_RELEASE=true`，后续 tag 发布默认只构建 Linux amd64 GHCR 镜像，不发布 macOS、Windows、arm64 或二进制下载包。
- workflow 的 release job 单独声明 `contents: write`、`packages: write`，使用 GitHub 自动提供的 `GITHUB_TOKEN`；无需额外保存个人 Token，也无需扩大所有 workflow 的默认权限。
- GHCR 镜像路径为 `ghcr.io/gjwhhhh/sub2api`，由发布 helper 固定使用镜像名 `sub2api`，与仓库名 `sub2api-update` 不同。Docker Hub 和 Telegram 凭证未配置，因此对应发布/通知步骤会跳过。

先推送分支，再手动执行不发布的服务器镜像打包验证：

```sh
git push origin custom
gh workflow run release.yml --repo gjwhhhh/sub2api-update --ref custom \
  -f tag=custom -f dry_run=true
gh run list --repo gjwhhhh/sub2api-update --workflow release.yml
```

确认选定提交和 CI 后，推送对应的定制 tag 会真正发布 GitHub Release 与 GHCR 镜像。安全修复版本 `custom-v0.2.21.2`（服务版本 `0.2.21+custom.2`，安装包/镜像版本 `0.2.21-custom.2`）已发布：

```sh
git push origin custom-v0.2.21.2
```

revision 1 的 dry-run 只验证构建，不发布。安全修复 revision 2 已通过 CI 和安全扫描后推送 tag，并完成正式发布。后续修复使用新提交和新 revision；不移动已有 tag。dry-run 验证打包和镜像构建，不执行镜像推送或 Release 创建。

2026-11-13 首次完整 dry-run 已通过：[运行 43376509485](https://github.com/gjwhhhh/sub2api-update/actions/runs/43376509485)，来源提交为 `03f87b3676ed173ad9dbff0e1850e71d1edd9bb8`。前端构建、Linux/macOS 的 amd64/arm64、Windows amd64 共五个安装包、制品来源和校验和检查，以及两个 Linux 架构的 Docker 镜像构建均成功。安装包可从该运行的 `release-binary-*` Artifacts 下载；当前保留期为 1 天，dry-run 报告保留 3 天。长期下载需正式发布 Release。

首次远程安全扫描（运行编号 `43376518624`）报告 Go 标准库漏洞 `GO-2026-6859`，`go1.27.3` 的 `net/http` 受影响，修复版本为 `go1.27.4`。定制 revision 2 已同步升级 `backend/go.mod`、三个 Docker 构建入口和 CI 版本断言至 Go `1.27.4`。本地 `govulncheck ./...` 已通过，未发现可达漏洞；另有 6 项依赖模块中的漏洞未被当前代码调用，仍保留扫描结果。没有屏蔽安全扫描或降低检测标准。

同次发布预检修复了在途计费内存测试的 fixture：原 fixture 缺少生产构造函数会初始化的可复用费率解析器，每次估算会临时创建 go-cache 及清理 goroutine，堆快照显示增长主要来自这些 goroutine。测试现在复用解析器，并使用 `runtime.KeepAlive` 保持被测缓存存活到堆测量结束；原 8 MiB 阈值、20,000 次未定价请求、不查数据库和快照读取次数断言均保留。

revision 2 的 GitHub Actions 发布已通过：[Release 运行 43385349452](https://github.com/gjwhhhh/sub2api-update/actions/runs/43385349452)。已创建 GitHub Release [custom-v0.2.21.2](https://github.com/gjwhhhh/sub2api-update/releases/tag/custom-v0.2.21.2)，上传五个平台安装包和 SHA256 文件，并发布多架构 GHCR 镜像。

后续精简发布复用已有 `SIMPLE_RELEASE` 模式，由自有仓库的 Actions 变量控制。自定义版本仍发布版本镜像及 `custom-latest` 别名，并创建 GitHub Release，但不附加二进制下载包。完整矩阵能力保留；确需恢复时先将仓库变量改为 `false`。手动输入 `simple_release=false` 不能覆盖值为 `true` 的仓库变量。已发布的 revision 2 及其下载包保持原样。

## server_tencent 部署记录

2026-11-13 已核实并完成单实例升级：

- 平台：OpenCloudOS 9.6，`x86_64`，容器平台为 `linux/amd64`。
- 部署文件：`/opt/sub2api/deploy/docker-compose.local.yml`，Compose 项目为 `deploy`，应用容器名为 `sub2api`。
- 升级前：`weishaw/sub2api:latest`，实际服务版本 `0.2.20`。
- 升级后：`ghcr.io/gjwhhhh/sub2api:0.2.21-custom.2`，实际服务版本 `0.2.21+custom.2`，来源提交 `6df10514ea15e3c5ae513a89598aea9b4b977132`。Compose 同时固定镜像 digest `sha256:d5c60308653cf8bf1596af3cdbeb8d4213acb1f9594d5b2a8d5b78621e93f0fc`。
- 保留原持久挂载 `/opt/sub2api/deploy/data:/app/data` 和 `127.0.0.1:8082:8080` 端口映射。仅重建应用容器，PostgreSQL、Redis 和其他服务未重建。
- 停止应用后生成数据库 custom-format dump、数据目录归档和部署配置备份，校验 dump 目录、归档可读性及 SHA256。备份目录为 `/opt/sub2api/backups/before-custom-v0.2.21.2-20261113-163150`，仅 root 可访问。
- 备份目录内的 `rollback-image.sh` 可恢复原 Compose 配置及保留的本地旧镜像。它不恢复数据库；需要恢复数据库时应先停止应用并评估快照之后的写入。

应用、数据库和 Redis 健康检查均通过，用户、账户及 API Key 数量与升级前一致。公网 `/health` 和 `/codex-eval` 均返回 200；评估及巡检状态接口在未登录时返回 401。自动巡检沿用默认关闭状态，未触发真实账户评估请求。管理员登录后的真实账户评估仍需按使用需求验收。

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
