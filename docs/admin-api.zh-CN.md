# 管理 API

本文件对应当前工作区实现。管理接口统一位于 `/admin/api`；客户端推理位于 `/v1`，两者凭证分开。

## 公共约定

- 除会话创建及会话状态外，管理 API 需要 `clodesen_admin` Cookie。会话有效期 12 小时，Cookie 为 HttpOnly、SameSite=Strict，路径 `/admin`。
- 修改请求校验同源 Origin。常规 JSON 请求体最多 16 KiB，单个 JSON 值；结构体输入拒绝未知字段。账号局部更新使用对象输入，只读取支持的字段。
- JSON 响应使用 `application/json; charset=utf-8`；管理错误为 `{"error":"说明"}`。客户端不应解析中文错误文案来判断状态。
- 400 参数无效，401 未登录，403 来源不允许，404 资源不存在，409 版本冲突，429 登录退避，503 存储或服务不可用。部分旧动作路由对不支持的方法返回 404，带 Allow 的资源路由返回 405。
- `revision` 是资源配置版本，密钥使用 `version`。409 后重新读取，保留用户输入供核对，禁止静默覆盖。
- GET 统计通常为服务本地读取；`refresh=1` 对缓存资源发起共享上游刷新。过期或部分失败可能保留旧快照，响应有 `X-Admin-Cache` 状态。
- 密码、管理令牌、完整密钥不会由读取接口返回。创建或轮换密钥的 `secret` 仅返回一次。

## 接口索引

路径均省略 `/admin/api` 前缀。

| 路径 | 方法 | 请求与结果 |
| --- | --- | --- |
| `/session` | GET | 是否已认证、是否需首次设置 |
| `/session` | POST | `{token,confirm?}`，设置或验证管理令牌并建立会话 |
| `/session` | DELETE | 注销当前会话 |
| `/login-events` | GET | 最近登录事件，不含令牌 |
| `/system-status` | GET | ready、draining、数据库、准入、审计、账号池及任务状态 |
| `/accounts` | GET | 账号快照、策略、余额汇总，支持 refresh |
| `/accounts` | POST | `{identifier,password}`，201 `{id}` |
| `/accounts/{id}` | PATCH | identifier、password、enabled 的局部更新，204；空密码保留旧密码 |
| `/accounts/{id}` | DELETE | 移除账号，204 |
| `/accounts/{id}/checkin` | POST | 单账号签到结果 |
| `/accounts/{id}/refresh` | POST | 单账号上游状态 |
| `/accounts/batch` | POST | `{accountIds,enabled}`，原子批量启停 |
| `/checkin` | POST | 同步全部签到，面板批量流程优先使用 jobs |
| `/settings` | GET/PUT | 当前路由策略；PUT `{strategy}` |
| `/config` | GET/PUT | 完整公开设置、字段来源/锁定/生效方式；PUT 提供 revision |
| `/models` | GET | 模型目录和价格，支持 refresh |
| `/model-policies` | GET | revision 及公开模型策略 |
| `/model-policies/{sourceId}` | PUT/DELETE | 写入 `{revision,rule}` 或按 revision 重置策略 |
| `/model-cooldowns` | GET | 账号与模型冷却、原因及恢复时间 |
| `/keys` | GET/POST | 密钥列表；创建 `{name,enabled?,expiresAt?,rpm?,models?,ipAllow?,limits?}` |
| `/keys/{id}` | PUT/DELETE | 按 version 更新/删除，DELETE 也支持 `?version=` |
| `/keys/{id}/rotate` | POST | `{version}`，返回 key 和一次性 secret |
| `/keys/{id}/usage` | GET | 近 24 小时已保留事件汇总 |
| `/admission` | GET/PUT | 准入状态和设置，PUT `{revision,settings}` |
| `/admission/reconcile` | POST | `{id,version,actual}`，对未知用量做显式对账 |
| `/usage` | GET | 独立的累计请求统计 |
| `/request-records` | GET | 筛选、分页、同一快照汇总及 storage 状态 |
| `/credit-usage` | GET | 积分明细、筛选与汇总，支持 refresh |
| `/reports` | GET | 时间范围和维度报表，见下文 |
| `/jobs` | GET/POST | 列表；提交 `{kind,accountIds,code?}`，202 `{job,duplicate}` |
| `/jobs/{id}` | GET | 单任务及逐账号状态 |
| `/jobs/{id}/cancel` | POST | 请求取消，204；不保证已经开始的上游操作没有副作用 |
| `/jobs/{id}/retry` | POST | 仅重试安全类型；兑换拒绝自动重试，409 |
| `/redeem` | POST | 同步兑换 `{code,accountIds?}`，面板使用持久化 jobs |
| `/redemption-history` | GET | 兑换历史 |
| `/cache-status` | GET | 各资源更新时间、过期时间、TTL 和刷新状态 |
| `/backups` | GET/POST | 备份列表 / 创建手动备份，创建返回 201 |
| `/backups/{id}` | GET/DELETE | 下载 ZIP / 删除，删除返回 204 |
| `/backups/schedule` | GET/PUT | 计划和运行状态；PUT `{revision,settings:{enabled,at,timezone,retain}}` |
| `/backups/validate` | POST | 原始 ZIP 请求体，最多 512 MiB；返回临时恢复计划 |
| `/backups/restore` | POST | 提交已校验计划，请求排空及重启恢复 |
| `/backups/restore-status` | GET | 恢复状态 |
| `/alerts` | GET/PUT | 告警渠道定义、状态和队列；按资源 revision 保存渠道配置 |
| `/alerts/test` | POST | 用当前草稿配置测试一个渠道，不持久化草稿 |
| `/update` | GET/PUT/PATCH/POST | 检查版本 / 保存下载源 / 测速 / 安装更新 |

## 筛选与报表

`/request-records` 参数为 `model,account,type,status,key,page,pageSize`。model/account 为不区分大小写的子串；key 精确匹配 ID。pageSize 为 1 到 100；越界页截断到最后一页。状态为 normal、upstream_error、abnormal、running、interrupted。筛选汇总与列表读取同一事务，账号选项来自保留明细。

`/reports?range=30d&dimension=model`：range 为 today、7d、30d；dimension 为 day、model、account、key。日期使用 Asia/Shanghai，今天从零点开始，7/30 天包含今天，结束时间为请求时刻且不包含边界。仅汇总已结束请求，P95 使用最近秩算法，失败和中断请求也计入耗时。

响应包含 since、until、rows、retentionDays、recordLimit、retainedSince、limited、coverage 和 storage。SQLite schema 3 为报表独立保留 31 天的紧凑指标，不保存提示词、IP 或凭证，也不受明细 5 万条上限影响。升级不把旧明细冒充连续指标；`retainedSince` 标记指标开始采集的时间，跨过此起点的范围 `limited=true`。审计写入降级/丢弃仍可能造成缺失。

`credits` 是近 7 天本地请求价格估算，零价格请求另计 unknown；不是已核实账单，不替代配额计量。旧数据没有请求与密钥的积分对应关系，key 维度返回 `unavailable_no_key_attribution`，不按比例分摊。`format=csv` 导出相同指标、覆盖范围和估算口径，防止分组名称被表格软件当作公式执行。

## 路由、告警及备份

strategy 仅支持 fixed、round_robin、random。新请求传入已移除的策略或账号 weight 字段返回 400。历史配置与备份中的 weighted、least_inflight、latency 在读取时转换为 fixed，旧 weight 字段忽略，后续保存时清除；策略不扩大计量重试预算。

告警默认关闭，每 30 秒检查本地状态。余额仅使用仍有效且所有启用账号余额均已知的缓存；未知状态不视为恢复。任务异常按各任务类型最近一个已结束任务判断，后续成功任务可以恢复。支持自定义 Webhook、Bark、Server 酱、PushPlus、Telegram、企业微信机器人、钉钉机器人、飞书机器人、SMTP、ntfy、WxPusher、Gotify 和 PushDeer。GET `/alerts` 返回 `definitions`（每渠道字段类型、必填和选项）、`settings.channels`、`configured`、当前条件及投递队列。凭据字段不会回传明文；`configured[channel][field]` 表示凭据已保存。PUT 请求示例：`{revision,settings:{lowBalance,channels:{webhook:{enabled:true,config:{url:"https://example.test/hook"}},ntfy:{enabled:false,config:{url:"https://ntfy.example.test",topic:"ops"}}}},clearSecrets:[]}`。`settings.channels` 中提交的渠道会更新，未提交的渠道保持不变；空凭据保留原值，`clearSecrets` 用 `渠道ID.字段名` 显式清除凭据，如 `["webhook.url"]`。清除必填凭据前须同时关闭对应渠道。旧版单 Webhook 配置会自动迁移；`settings.enabled` 汇总各渠道启用状态。

POST `/alerts/test` 使用相同 revision 校验，格式为 `{revision,channel,config,clearSecrets?}`，按已保存凭据与草稿合并后发送一条测试消息，但不保存草稿或变更 revision。验证失败返回 400，revision 冲突返回 409，渠道发送失败返回 502。持久化告警队列跨实例认领，通知请求超时 5 秒且不跟随重定向；稳定的 `Idempotency-Key` 供接收方去重。进程崩溃窗口仍可能重复投递，接收端应按事件 ID 去重。失败退避 1/2/4/8/16 分钟，最多 10 次；队列最多 100 条，溢出与重试耗尽在面板显示。

自动备份默认关闭，保留数 1 到 60，仅清理自动备份，手动备份不受影响。成功创建与旧备份清理分别反馈：cleanup_failed 表示新备份已保存，计划不重复创建。schema 2 备份仍可校验及恢复；下次启动升级至 schema 3，迁移前创建权限为 0600 的 SQLite 快照。

`GET/HEAD /readyz` 不需登录，检查本地数据库、准入和账号可用性，失败返回 503，不访问上游且不公开内部错误。`/healthz` 仅表示 HTTP 进程仍响应。
