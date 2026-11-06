# 工作日志 - 2026-11-07

session-id: 20261107-0207

## [02:07] - 文档生成: 核对 codebuddy2api 并整理前后端适配方案

- **文件**: docs/codebuddy2api-reference-plan.zh-CN.md, docs/control-plane-implementation.md
- **决策**: 参考固定提交 58fc829；保留 Go/net/http 与嵌入式静态 JavaScript；区分已发布基线、现有未提交 key 改动、规划能力。配额/跨进程计数/任务恢复属于扩展设计，不能声称参考项目已完整实现。保留完整 P0-P4 实施范围与未完成状态。
- **验证**: 远端 HEAD 与参考克隆一致；已运行的 npm run test:web 返回 1 项通过；go test ./...、go vet ./...、git diff --check 均通过。没有宣称截图已人工审阅或全部规划功能已完成。

## [02:39] - 文档生成: 补充实际进度、跨语言适配和前后端验收

- **文件**: docs/codebuddy2api-reference-plan.zh-CN.md, docs/control-plane-implementation.md
- **决策**: 逐项核对参考项目 key、策略、冷却、作业及存储前端源码；更新 SQLite/异步审计现状，单列迁移原子性、owner 恢复、跨实例查询一致性及脱敏缺口。加入 Go/静态 JS 对应机制、前后端共同完成标准及账号导入/构建身份等可选借鉴项。完整开发交付目标仍未完成，不将方案文档或局部测试视为 P0-P4 已交付。
- **验证**: 接续全量 go test ./... 返回成功；再次 go vet ./...、npm run test:web、git diff --check 通过。人工查看本次生成的 1440/768/390/320 截图，确认 key 列表操作可见、页面无横向溢出；尚未补充的浏览器用例保留待办。

## [03:49] - 功能实现: 收口审计基础并接入模型策略和模型冷却

- **文件**: internal/controlstore, internal/usage, internal/apikey/control_test.go, internal/modelpolicy, internal/modelcooldown, internal/server/models*, internal/openai/model*, internal/upstream/model_cooldown*, internal/admin/model_policies.go, internal/admin/control_session_test.go, internal/server/web/model-policies.js, tests/web*
- **决策**: 历史 usage 聚合与明细单事务导入；审计写入采用 BEGIN IMMEDIATE，实例心跳区分活跃请求；Close 幂等，队列健康包含重试批次，管理查询读取共享快照。模型策略使用 source ID 授权、CAS revision、严格名称空间与循环校验，结构化重建 JSON/multipart 并映射 SSE/JSON 响应名称。429 冷却按账号/模型独立存储，保留本机失败保护。参数白名单和结构化错误码避免上游正文/任意 metadata 入审计，新增逐尝试记录。修复流缓冲数据跳过及输出后错误导致 wrote 状态丢失。
- **验证**: 先复现第二实例错误中断活跃请求后修复；新增迁移故障注入/回滚、跨实例 RPM、会话重启/退出、模型策略冲突/能力/别名、JSON/multipart/SSE 映射、冷却重启/隔离、流断开不切换账号、审计敏感信息测试。go test ./... 与 go vet ./... 通过；浏览器 2 项通过，覆盖四尺寸模型页面、编辑/冲突/停用/重置/模型冷却显示。完整发布矩阵正在验证，P3/P4 剩余内容仍未完成。

## [05:00] - 功能实现: 接入共享准入及请求配额基础

- **文件**: internal/admission, internal/apikey/store.go, internal/server/handler.go, internal/upstream/timeouts.go, internal/admin/api.go, internal/server/web/admission.js, internal/server/web/keys.js, tests/webfixture/main.go
- **决策**: 读体前保守预留缓冲预算；全局/key/model并发使用持久租约，RPM与日/月请求数在单事务检查后提交。按配置时区划分账期；缺少可信计量时拒绝token/积分硬限额调用。响应首字节/空闲/整体超时分别处理，限制账号尝试次数；单账号透传已缓冲的上游429。
- **验证**: 双Store并发20次最多准入5次、幂等准入/释放、key并发及未知计量拒绝测试通过；相关apikey/admin/server/upstream回归通过。前端准入/配额表单已接入，浏览器和进一步失败路径验证待执行；整体P0-P4仍未完成。

## [06:00] - 功能实现: 持久作业、刷新合并、账号迁移和备份基础

- **文件**: internal/jobs, internal/admin/jobs.go, internal/admin/schedules.go, internal/admin/refresh.go, internal/admin/store.go, internal/admin/account_sync.go, internal/controlstore/backup.go, internal/backups, internal/server/web/jobs.js, internal/server/web/storage.js, tests/web/keys.test.cjs
- **决策**: 两个受控worker、32个活动任务上限、1000条历史、按类型/目标/参数去重，定时任务加入日期去重。owner租约过期标记中断并阻止迟到写；取消兑换不假定未扣费，禁止自动重试。缓存同资源强制刷新合并、等待方取消不取消共享刷新。账号JSON导入SQLite且原件保留，管理写入带revision，运行状态事务更新不覆盖credentials，实例按revision同步账号池。schema2增加独立审计幂等标记，明细淘汰不再导致重复累计；过旧事件拒绝重放。
- **验证**: 双Store并发去重/双worker只执行一次、取消与过期owner fencing、账号旧快照冲突和密码保留、HTTP取消释放租约、首字节/空闲超时、缓存刷新合并测试通过。浏览器3项通过，覆盖配额设置、日请求限制、任务页面重载。在线WAL快照/清单哈希/白名单和临时目录恢复重新打开SQLite验证通过；备份前端创建/下载/上传校验正在补验证。恢复执行停机重启链路、配置仓储、token/积分结算等仍未完成，没有发行/提交/推送。

## [07:00] - 功能实现: 配置仓储、来源锁定与生效边界

- **文件**: internal/config/settings_store*, internal/config/config.go, internal/admin/settings.go, internal/admin/api.go, internal/admin/update.go, internal/admin/account_sync.go, internal/upstream/upstream.go, internal/upstream/admin.go, internal/server/web/app.js, tests/web*
- **决策**: 管理覆盖保存在SQLite，配置读取带revision/fields；CLI > CLODESEN2API_SECTION_KEY环境变量 > 管理覆盖 > 文件 > 默认值。锁定字段拒绝覆盖，连接/鉴权配置重启生效，调度/并发/更新源即时生效；运行中的连接配置保持不可变。兑换码API只显示已配置，空值保留。账号状态更新复用凭据未变的session，避免同步时丢失cookie。修复默认配置文件没有-c时可能不读取的问题。
- **验证**: 配置冲突、CLI/env/file来源、锁定、无效时区、热更新/待重启、跨Store刷新测试通过；go test ./...通过；浏览器3项通过，包括配置修改/409/待重启标记和备份创建下载校验。完整P0-P4目标仍有待办，尚未版本更新/提交/推送。

## [08:00] - 功能实现: 控制数据迁移、恢复重启和可靠性收口

- **文件**: internal/admin/control_data*, internal/backups, internal/processguard, internal/admission, internal/modelpolicy, internal/upstream, internal/server, cmd/clodesen2api/main.go, tests/restore_test.go
- **决策**: 积分账本、兑换记录及缓存迁入SQLite；保留失败账号积分缓存；schema升级前生成WAL一致性快照。恢复采用共享实例锁及独占转换闸门，HTTP/worker/审计排空后关闭数据库并exec重启，支持替换失败回滚。多级模型映射检查整条链能力并解析最终上游。Key当前鉴权与准入在同一事务核对，租约有owner/续期及失效取消，上游重试共享总次数，响应缓冲有界。
- **验证**: 全量Go及浏览器3项通过；新增真实双进程恢复排他/exec重启验证，轮换后准入、租约失效、schema1升级前WAL快照、审计幂等保留边界测试通过。一次重复配置锁导致浏览器超时，移除后重跑通过。恢复回滚和计量端到端回归正在执行，尚未发行/提交/推送。

## [08:20] - 功能实现: 可验证计量的预留、结算和对账

- **文件**: internal/admission/metering*, internal/modelpolicy/store.go, internal/upstream/metering.go, internal/server/metering_test.go, internal/admin/api.go, internal/server/web/{admission,keys,model-policies}.js
- **决策**: 默认上游计量未验证，Token/积分硬配额明确拒绝；管理员可为最终上游模型声明已核实的usage契约及单请求上界，受限请求只尝试一次。按request ID预留，JSON/SSE可信usage结算整数用量，未知项不释放，重启保留并通过带version对账；上界违约阻止该key继续计量调用。界面显示用量/预留与待核对记录，不能把价格估算或余额差当成可信结算。
- **验证**: 部分结算、拒绝不计数、未知预留重启及对账幂等回归通过；JSON/SSE端到端与恢复语义/回滚验证继续执行。

## [09:20] - 验证与修复: 审计panic、部分计量越界和恢复边界

- **文件**: internal/server/{handler.go,audit_safety_test.go}, internal/admission, internal/openai/request_test.go, internal/upstream/timeouts_test.go, internal/config/backup_test.go, internal/admin/backup_validation_test.go, tests/web/keys.test.cjs
- **决策**: 响应写出panic先复现为错误成功记录，再修为interrupted；部分已知用量越界立即封锁后续受限调用。准入事务失败还原本地reservation标记。恢复校验检查未裁剪状态，拒绝坏lease/reservation/balance。
- **验证**: 针对性Go回归通过；5项浏览器通过，覆盖四宽度待对账表单与Key编辑；继续补实际Token输入及保存按钮滚动可达截图。恢复语义/配置路径拒绝、总重试预算、408与有界响应测试通过。

## [09:25] - 文档与发行准备: v0.3.0及准确能力边界

- **文件**: VERSION, README.md, docs, .github/workflows/release.yml, THIRD_PARTY_NOTICES.md, Makefile
- **决策**: VERSION统一版本；main只验证打包，tag发布且不clobber既有发行。说明契约需人工验证、未知用量、单机共享、逐仓储迁移、替换回滚范围和Windows未运行验证。保留可选借鉴项为未来工作。
- **验证**: 完整测试、最终多平台构建及远端CI待完成，不提前声明发行完成。
