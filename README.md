# magpie-search-adapter

把 **Magpie 的联网搜索**（`web_search` / `web_fetch`）包装成普通 REST 搜索 API，供 **new-api** 或直接支持 Tavily / Serper / Brave 的客户端使用。对外不暴露 MCP。

```
客户端 / new-api
   │  URL + Key
   ▼
Tavily / Serper / Exa / SearXNG / Firecrawl / Google CSE
You.com / Bocha / Zhipu / Jina / Brave / DeepSeek
   │
   ▼
magpie-search-adapter
   │ REST → MCP（默认）
   │ 或 → /v1/alpha/search
   ▼
Magpie
/mcp/magpie/web-search
/v1/alpha/search
```

## 支持的接口

| 厂商 / 形状 | 规范路径 | 原生别名 | 鉴权 |
|---|---|---|---|
| Tavily | `POST /search`（body `query`） | — | Bearer / body `api_key` |
| Serper | `POST /search`（body `q` 或 `X-API-KEY`） | — | `X-API-KEY` / Bearer |
| Brave | `GET /res/v1/web/search` | — | `X-Subscription-Token` / Bearer |
| DeepSeek | `POST /v1/tools/search`、`POST /v1/tools/fetch` | — | Bearer |
| Exa | `POST /exa/search` | `POST /search`（body 含 `numResults` / `contents` / `additionalQueries`） | Bearer / `X-API-Key` |
| Firecrawl v1 | `POST /firecrawl/v1/search` | `POST /v1/search`（无 `X-API-Key` 时） | Bearer |
| Firecrawl v2 | `POST /firecrawl/v2/search` | `POST /v2/search` | Bearer |
| You.com | `POST/GET /you/v1/search` | `POST/GET /v1/search`（带 `X-API-Key` 时） | `X-API-Key` / Bearer |
| SearXNG | `GET/POST /searxng/search` | `GET /search`（`format=json`） | 免 key 网段 / `?key=` / Bearer |
| Google Programmable Search | `GET /google/customsearch/v1` | `GET /customsearch/v1` | `?key=` / Bearer |
| 博查 Bocha | `POST /bocha/v1/web-search` | `POST /v1/web-search` | Bearer |
| 智谱 Web Search | `POST /zhipu/v4/web_search` | `POST /api/paas/v4/web_search` | Bearer |
| Jina Search | `GET/POST /jina`、`/jina/...` | — | 免 key 网段 / `?key=` / Bearer |
| Tavily Extract | `POST /extract` | — | Bearer |
| OpenAI 模型列表 | `GET /v1/models` | — | Bearer |
| 健康检查 | `GET /healthz`、`GET /readyz` | — | 无 |

`POST /search` 同时承担 Tavily、Serper、Exa（按 body 键区分）和 SearXNG 表单（按 `Content-Type: application/x-www-form-urlencoded`）；`GET /search` 固定给 SearXNG。

### 客户端 base URL 怎么填

| 客户端 | base URL |
|---|---|
| Exa SDK | `http://<适配器>/exa` |
| Firecrawl SDK | `http://<适配器>`（原生 `/v1/search`、`/v2/search`），或 `http://<适配器>/firecrawl` |
| You.com SDK | `http://<适配器>/you`（原生 `/v1/search` 需要 `X-API-Key`） |
| SearXNG 客户端（LangChain/Dify 等） | `http://<适配器>`（`GET /search?format=json`）或 `/searxng` |
| Google CSE SDK | `http://<适配器>/google`，或直接替换 host 为适配器 |
| Bocha / 智谱 | 原生路径直接可用（`/v1/web-search`、`/api/paas/v4/web_search`） |
| Jina | `http://<适配器>/jina/` |

所有接口都接受 `ADAPTER_API_KEYS` 中的任意一个 key。鉴权失败返回各厂商自己的错误形状。免 key 访问默认关闭，见“稳定性与性能”的 `ALLOW_KEYLESS_CIDRS`。

### 接入方式怎么选（new-api vs 直连）

两种方式都能用，区别在治理能力和接口覆盖：

| 场景 | 推荐 | 理由 |
|---|---|---|
| 多用户 / 团队，需要独立 token、计费、限额 | **接入 new-api** | 适配器只有共享 key，无法按用户治理；new-api 负责鉴权、配额、计费、日志 |
| 自己用或少量内部服务 | **直连** | 少一层转发，延迟最低，调试简单 |
| 客户端要用 Tavily / Serper / Exa / Firecrawl / SearXNG / Google / You.com / Bocha / 智谱 / Jina 的原生形状 | **直连** | new-api 只暴露 DeepSeek 形状（`/v1/tools/search`、`/v1/tools/fetch`） |
| 客户端只会调 new-api 的 `/v1/tools/search` | **接入 new-api** | 适配器本身不实现 new-api 的 token/计费层 |

经 new-api 调用时，客户端只会用到 DeepSeek 形状这一个接口（`/v1/tools/search`、`/v1/tools/fetch`），参数只有 `query`、`count`、`need_content`、`ori_question`、`localization`。Tavily / Serper / Brave / Exa / Firecrawl / SearXNG / Google / You.com / Bocha / 智谱 / Jina 的形状、Tavily `/extract`、`include_domains` / `exclude_domains`、分页都只在直连时可用。注意这跟**上游搜索源**是两回事：Magpie 侧配置的 search API（或模型代搜）在两种接入方式下都是同一条链路，接入 new-api 不会减少上游搜索服务的种类。

本项目默认按**直连 + 1Panel 反向代理**部署；只有需要 new-api 的 token、配额或计费时才启用 new-api。

推荐拓扑：

```text
公网客户端 → new-api（token/配额/计费） → compose 内网 → magpie-search-adapter → Magpie
内网服务 / 厂商 SDK ──────────────────────→ magpie-search-adapter（仅内网或 HTTPS 反代）
```

安全底线：**不要把适配器的明文端口直接暴露公网**。适配器只有静态共享 key，没有按 key 的配额、计费或用量统计，服务本身也是明文 HTTP。直连场景把 `LISTEN_ADDR` 设为 `127.0.0.1:8787` 或内网 IP；必须公网直连时，套 HTTPS 反向代理并在反代做限流和访问控制，定期轮换 `ADAPTER_API_KEYS`。对外多用户优先走 new-api。

## 快速开始

要求 Go 1.26+。

```powershell
cd D:\cc_prj\prj2\ts10

# 1. 配置（至少这四项）
$env:ADAPTER_API_KEYS = "change-me-to-a-long-random-key"
$env:MAGPIE_API_KEY = "sk-magpie-key-..."   # 必须是有效的 gateway key
$env:MAGPIE_BASE_URL = "https://magpie.127001001.xyz"
$env:MAGPIE_SEARCH_MODE = "mcp"             # mcp 或 alpha

# 2. 自检（会真的搜索一次，消耗 Magpie 额度）
go run . -selfcheck

# 3. 启动
go run .
```

也可以把变量写进 `.env`（程序启动时自动读取，已存在的环境变量优先）：

```powershell
Copy-Item .env.example .env
# 编辑 .env
go run .
```

监听地址默认 `0.0.0.0:8787`。如果 new-api 和适配器在同一台机器，建议改成 `127.0.0.1:8787` 并只在本机访问。

## 配置

| 变量 | 默认 | 说明 |
|---|---|---|
| `LISTEN_ADDR` | `0.0.0.0:8787` | 监听地址 |
| `ADAPTER_API_KEYS` | 必填 | 逗号分隔的静态 key；new-api 渠道 key 从这里选一个 |
| `MAGPIE_BASE_URL` | `https://magpie.127001001.xyz` | Magpie 网关地址，不要带 query/fragment |
| `MAGPIE_API_KEY` | 必填 | 有效的 Magpie gateway key |
| `MAGPIE_SEARCH_MODE` | `mcp` | `mcp`：官方 MCP，支持 count 1..20 与 fetch；`alpha`：纯 HTTP `/v1/alpha/search`，完全不碰 MCP，但条数受 Magpie 全局设置限制、没有 fetch |
| `MAGPIE_MCP_PATH` | `/mcp/magpie/web-search` | MCP 路径 |
| `MAGPIE_ALPHA_MODEL` | `magpie-search-adapter` | alpha 模式请求里的 model；应避免命中 ChatGPT/Codex 账号模型 |
| `MAGPIE_FETCH_MODE` | `mcp` | `mcp` 或 `off`；alpha 模式下如果连 fetch 也不想用 MCP，设为 `off` |
| `INIT_TIMEOUT` | `10s` | MCP initialize 超时 |
| `REQUEST_TIMEOUT` | `60s` | 单次搜索超时 |
| `FETCH_TIMEOUT` | `90s` | 单次 fetch 超时 |
| `DEFAULT_COUNT` | `6` | 未指定条数时的通用兜底 |
| `MAX_COUNT` | `20` | 上限（Magpie 最多 20） |
| `FETCH_MAX_CHARS` | `100000` | Magpie `web_fetch` 上限 |
| `MAX_BODY_BYTES` | `1048576` | 请求体上限 |
| `MAX_CONCURRENCY` | `32` | 同时处理的请求上限 |
| `UPSTREAM_RETRIES` | `2` | 搜索失败后的重试次数（仅 5xx / 429 / 网络错误 / 未耗尽的超时） |
| `RETRY_BASE_DELAY` | `200ms` | 指数退避基数（full jitter） |
| `RETRY_MAX_DELAY` | `2s` | 退避上限；上游 `Retry-After` 更长时以它为准 |
| `SEARCH_BUDGET` | `REQUEST_TIMEOUT` | 一次搜索（含重试）的总预算 |
| `SEARCH_CACHE_TTL` | `30s` | 搜索结果缓存时间；`0` 关闭 |
| `SEARCH_CACHE_SIZE` | `1024` | 缓存条目上限（LRU） |
| `ALLOW_KEYLESS_CIDRS` | 空 | 允许免 key 的网段（SearXNG/Jina）；逗号分隔 CIDR |
| `LOG_LEVEL` | `info` | `debug` / `info` / `warn` / `error` |

## 稳定性与性能

- **重试**：只对可恢复错误重试（上游 5xx、429、网络错误、未耗尽的超时），指数退避 + full jitter；上游返回 `Retry-After` 时以它为准。客户端错误（400/401/403/无搜索源）不重试。
- **时间预算**：一次搜索（含重试）不超过 `SEARCH_BUDGET`（默认等于 `REQUEST_TIMEOUT`），退避等待也受它约束。
- **结果缓存**：只缓存搜索、不缓存 `fetch`；只缓存成功结果。key 包含 query、count、include domains。`SEARCH_CACHE_TTL=0` 关闭，`SEARCH_CACHE_SIZE` 限制 LRU 条目。
- **singleflight**：相同查询的并发请求只打一次 Magpie，其余等待并共享结果；某个等待者取消不会取消共享调用。
- **并发上限**：`MAX_CONCURRENCY` 满时立即 503。重试中的请求会一直占用槽位，所以 Magpie 故障时 503 会更早出现。
- **免 key 访问**：`ALLOW_KEYLESS_CIDRS` 默认空。填了之后，来源 IP 命中这些 CIDR 的请求可以不带 key（SearXNG/Jina 客户端无法加 header）。只信任 TCP 来源地址，不信任 `X-Forwarded-For`；适配器在反向代理后面时不要用它，改用 `?key=` 或让反代注入 `Authorization`。启动日志会打印警告。

## Magpie 前置条件

1. 开启 **Settings → Network and sharing → Share on local network**（远程调用需要）。
2. 在 **Gateway → Gateway keys → Add gateway key** 新建一个启用的 key，或：
   ```bash
   magpie gateway-key add "new-api-search"
   ```
3. 打开 **Settings → Plugins → Web search**。
4. 搜索源保持默认即可。Magpie 用“会搜索的模型”（模型代搜）时，适配器会解析模型写的要点与 `*Page:*` / `*Pages:*` 链接；这是当前支持并实测过的模式。
   - 模型代搜的 URL 是 Google grounding 跳转链接（`vertexaisearch.cloud.google.com/...`，实测 302 到真实页面），`site_name` 是跳转域名，`content` 是要点摘要而不是页面正文，返回条数由模型决定。
   - 如果想要真实 URL + 页面正文，可按下面“Magpie 搜索源：模型代搜与 search API”的可选步骤切换，不改也能用。
5. 使用 MCP 模式时，给这个 key 授权 `magpie-web-search`：
   ```bash
   magpie gateway-key mcp <id> magpie-web-search
   ```
6. 如果 Magpie 配置了 `MAGPIE_ALLOW_IPS`，把适配器所在机器的出口 IP 加进去。

## Magpie 搜索源：模型代搜与 search API

### 当前：模型代搜（无需改动）

Magpie 默认优先用“会搜索的模型”。它的 `web_search` 返回模型写的要点，例如：

```
* **$11 Billion Funding Round:** ...
  *Page:* [OpenAI raises $11bn | MEO](https://vertexaisearch...)
```

适配器会按要点拆成多条结果，用 `*Page:*` / `*Pages:*` 后面的链接作为 URL，用要点摘要作为 `content`。行为与限制见“契约与限制”。

### 可选：换成 search API

如果以后想让结果带真实 URL 和页面正文，在 Magpie 容器里执行（宿主机二进制则直接 `magpie ...`）：

```bash
docker exec <magpie容器> magpie settings get searcher
docker exec <magpie容器> magpie settings set searcher off      # 只用 search API
# 或者保留模型兜底：
docker exec <magpie容器> magpie settings set searchFirst api   # API 优先，失败再用模型

docker exec <magpie容器> magpie search add tavily <key>        # brave / exa / firecrawl / you / searxng
docker exec <magpie容器> magpie search count 20                # 1..20
docker exec <magpie容器> magpie search                         # 查看已配置的源与顺序
```

- `you` 不需要 key：`magpie search add you`。
- `searxng` 只需要 URL：`magpie search add searxng url=https://searx.example.com`。
- 也可以走 UI：**Settings › 模型 › 联网搜索**，添加搜索 API；新版本有“优先使用”（模型代搜 / 搜索 API）。
- 切换后适配器不用改：它会优先解析 `1. 标题 — URL` 的结构化页面列表。

| 模式 | URL | 正文 | 条数 |
|---|---|---|---|
| 模型代搜（当前） | grounding 跳转链接，302 到真实页面 | 模型要点摘要 | 由模型决定，适配器只能截断 |
| search API | 真实页面 URL | 每页约 1500 bytes | 受请求 `count` 控制（≤20） |

### 搜索模型与思考强度（searcher / searcherEffort）

Magpie 的“模型代搜”用哪个模型、想多久，由 `settings.json` 的三个键控制。CLI 键名区分大小写，就是 JSON 名；容器内用 `magpie settings set <key> <value>`：

| 键 | 取值 | 说明 |
|---|---|---|
| `searcher` | 空（自动）/ 模型 id / `off` | 空 = Magpie 按 provider 顺序自动选；`off` = 只用已配置的 search API |
| `searcherEffort` | 空 / `none` / `minimal` / `low` / `medium` / `high` / `xhigh` / `max` / `ultra` | 空 = 不发送 effort、用模型自己的默认；`none` 关闭思考；其余越高思考越久 |
| `searchFirst` | `model` / `api` | `api` = 先问 search API，失败再回到模型代搜 |

**适配器不需要因这些设置改动**：它只解析 Magpie 返回的文本。但设置会直接影响：

| 影响面 | 说明 |
|---|---|
| 延迟 / 超时 | effort 越高越慢。适配器单次搜索受 `REQUEST_TIMEOUT`（默认 60s）和 `SEARCH_BUDGET`（默认等于它）约束，超出返回 504。`high` 及以上建议把两者一起调到 90–120s（`SEARCH_BUDGET` 不能大于 `REQUEST_TIMEOUT`） |
| 条数 | 模型代搜由模型决定引用几条，适配器只能截断；要可控条数用 search API |
| URL / 正文 | 模型代搜给 grounding 跳转链接 + 摘要；search API 给真实 URL + 约 1500 bytes 正文 |
| 解析命中率 | 换模型可能改变输出格式；适配器认编号列表、`*Page:*` / `*Pages:*`、markdown 链接和裸 URL，认不出时整段文本作为 1 条结果 |

推荐：

- **保持默认**：当前“模型代搜”已实测可用，不需要改。
- 想更快：`magpie settings set searcherEffort low`（或 `none` 完全关闭思考）。
- 想真实 URL / 正文 / 可控条数：`magpie settings set searchFirst api`（或 `searcher off`），并按上一节添加 search API。
- 改回默认：`magpie settings reset searcherEffort`（`searcher`、`searchFirst` 同理）。
- alpha 模式下 `MAGPIE_ALPHA_MODEL` 是适配器发给 `/v1/alpha/search` 的 model，与这三个键独立；仍要避免命中 ChatGPT/Codex 账号模型。

## 接入 new-api（可选）

直连对外部署可以跳过本节。只有需要 new-api 的统一 token、分组、配额、计费或日志时，再按本节把适配器挂成一个 DeepSeek 渠道。

new-api 需要包含 `POST /v1/tools/search` 的版本：该路由由 2026-10-17 的 commit 引入，随 **v1.0.0-rc.45** 发布。

在 new-api 后台 → 渠道 → 添加：

- 类型：**DeepSeek**
- 名称：`Magpie Search`
- Base URL（**不要带 `/v1` 或尾部斜杠**；new-api 会拼成 `${BaseURL}/v1/tools/search`）
  - 适配器与 new-api 在同一个 compose 网络：`http://magpie-search-adapter:8787`
  - 不同网络：`http://<服务器内网IP>:8787`（先把适配器的 `ports` 发布出来）
  - 不要填 `127.0.0.1`：在 new-api 容器里它指向容器自己
- 密钥：`ADAPTER_API_KEYS` 中的任意一个
- 模型：`deepseek-search`、`deepseek-fetch`
  - **只在“Magpie Search”这个渠道勾选这两个模型**，其他 DeepSeek 渠道不要勾，否则搜索可能被路由到官方 DeepSeek
- 分组：new-api token 能访问的组
- 模型价格：内置价格是 `$0.75 / 1000 次`，想免费就在“模型价格”里覆盖为 0

注意：

- new-api 的渠道“测试”按钮默认走 `/v1/chat/completions`，本适配器不实现 chat，测试会失败，属正常。**如果开启了全局“自动测试渠道/自动禁用”，该渠道可能被自动禁用**；建议关闭全局自动测试/自动禁用，或用 curl 自行监控。
- 如果 new-api token 限制了模型，把 `deepseek-search`、`deepseek-fetch` 加入允许列表。
- 空结果会返回 `{"results":[]}`，new-api 会走退款逻辑。

客户端调用（只暴露 new-api）：

```bash
curl -s http://<new-api>/v1/tools/search \
  -H "Authorization: Bearer sk-<new-api-token>" \
  -H "Content-Type: application/json" \
  -d '{"query":"OpenAI latest news","count":5,"need_content":true}'
```

```bash
curl -s http://<new-api>/v1/tools/fetch \
  -H "Authorization: Bearer sk-<new-api-token>" \
  -H "Content-Type: application/json" \
  -d '{"url":"https://example.com/article"}'
```

### 为什么不用 new-api 的“任务插件”？

new-api 确实有插件系统（任务插件 / Task Plugin v1），但它面向异步生成任务（视频 / 图片 / 音乐），不能替代本适配器：

- 插件是单文件同步 JS，宿主负责网络、渠道调度、任务持久化和计费；必需钩子是 `buildSubmitRequest` / `parseSubmitResponse` / `parseTaskResult`，按“提交任务 → 轮询 → 取产物”建模。见 [`docs/plugin-api/v1.md`](https://github.com/QuantumNous/new-api/blob/main/docs/plugin-api/v1.md)。
- 插件只能声明 `meta.routes`（厂商风格原生路由）或 `meta.protocols`（`openai_responses` / `openai_video` / `openai_image`），没有 search 协议，也不能接管宿主固定路由 `/v1/tools/search`。
- new-api 的 `/v1/tools/search` 处理器只接受 `ChannelTypeDeepSeek` / `ChannelTypeNewAPI` / `ChannelTypeSub2API` / `ChannelTypeAdvancedCustom`，其他渠道直接返回 `channel does not support ...`；插件渠道是 type 60/61。见 [`relay/deepseek_tools_handler.go`](https://github.com/QuantumNous/new-api/blob/main/relay/deepseek_tools_handler.go)。
- 官方插件市场 [`QuantumNous/new-api-plugins`](https://github.com/QuantumNous/new-api-plugins) 现有 13 个插件全部是视频 / 图片 / 音乐 / 结构化决策任务，没有搜索插件。
- 插件自身不能发 POST（`utils.fetch` 仅 GET/HEAD，且只在 driver hooks 可用）；Magpie MCP 需要 initialize + session + tools/call 多轮交互，无法在一次 submit 里完成。

结论：要让客户端只调 new-api 的 `/v1/tools/search`，同时对外提供 Tavily / Serper / Brave / DeepSeek 四种形状，独立适配器是目前唯一完整方案。

## 直接使用适配器

```bash
KEY='change-me-to-a-long-random-key'

# Tavily
curl -s http://127.0.0.1:8787/search -H "Authorization: Bearer $KEY" \
  -H "Content-Type: application/json" \
  -d '{"query":"golang","max_results":3,"include_raw_content":true}'

# Serper
curl -s http://127.0.0.1:8787/search -H "X-API-KEY: $KEY" \
  -H "Content-Type: application/json" \
  -d '{"q":"golang","num":3}'

# Brave
curl -s "http://127.0.0.1:8787/res/v1/web/search?q=golang&count=3" \
  -H "X-Subscription-Token: $KEY"

# DeepSeek
curl -s http://127.0.0.1:8787/v1/tools/search -H "Authorization: Bearer $KEY" \
  -H "Content-Type: application/json" \
  -d '{"query":"golang","count":3,"need_content":true}'
```

## 契约与限制

- **条数**：Magpie 最多返回 20 条。Tavily `max_results` 默认 5，Serper `num` 默认 10，Brave `count` 默认 20，DeepSeek `count` 默认 10（合法 1..50，但上游最多 20）。各厂商自己的上限也会生效（Google `num` ≤10、Jina ≤20、Bocha/智谱 ≤50、Exa/Firecrawl/You.com ≤100），最终都受 `MAX_COUNT` 约束。模型代搜模式下实际条数由模型输出决定，适配器只能截断到请求值。
- **正文长度**：search API 模式下 Magpie 每页只带约 1500 bytes 正文；模型代搜模式下 `content` 是模型写的要点摘要。需要全文请调用 `/v1/tools/fetch` 或 Tavily `/extract`。
- **字段**：DeepSeek 的 `site_icon`、`date` 返回 `null`，`authority` 返回 `-1`；`site_name` 用 URL 主机名。Brave 的 `page_age` 返回 `null` 或 `""`。
- **分数**：Tavily `score` 与 DeepSeek `rerank_score` 是适配器按顺序生成的，不是真实相关性分数。
- **alpha 模式**：不能按请求指定条数，使用 Magpie Settings 的全局 count（默认 6，可 `magpie search count 20`）；没有 fetch；如果 `MAGPIE_ALPHA_MODEL` 命中 ChatGPT/Codex 账号模型，可能返回加密输出并报 502。
- **域名过滤**：`include_domains` / `exclude_domains` / Google `siteSearch` 只写主机名（例如 `example.com`），不要带端口、协议或路径；适配器会去掉 `*.`、`.` 和 `www.` 前缀，其余非法值返回 400。URL 形式的条目（`https://example.com:443/x`）会先取主机名，端口会被丢掉。
- **字段诚实性**：拿不到的字段一律 `null` 或省略，不编造。例如 Firecrawl 的 `markdown` / `html` / `rawHtml`，Exa 的 `publishedDate` / `author` / `image` / `favicon` / `subpages`，Google 的 `pagemap` / `image`，You.com 的 `thumbnail_url` / `page_age`；`content` / `text` / `summary` 在模型代搜模式下是 Magpie 给的要点摘要。
- **分页**：SearXNG `pageno>1` 返回空页；Google `start` 在我们缓存的 ≤20 条内切片，越界返回空 `items`；You.com `offset` 按页索引切片，超出同样返回空数组。
- **模型代搜（当前模式）**：Magpie 用“会搜索的模型”回答时，适配器会按要点（`* **…:**` + `*Page:*` / `*Pages:*` 链接）拆成多条结果，内容为要点摘要。URL 是 grounding 跳转链接（302 到真实页面），`site_name` 是跳转域名，没有每页正文，条数由模型决定。想要真实 URL + 正文可按“Magpie 搜索源”切换 search API。
- **鉴权**：适配器 key 失效返回 401；Magpie key 失效返回 502（不会把上游 401 当成客户端 key 错误）；未配置搜索源返回 503；超时返回 504。
- **SSRF**：`fetch` 只接受 http(s) 公网 URL；私网/回环字面量在适配器侧就被拒绝，Magpie 的 `web_fetch` 还会再做一次公网地址检查。

## 已验证（2026-11-08）

用有效 gateway key 在本机实测：

- `/readyz` → `200 {"ready":true}`；MCP `tools/list` 含 `web_search` / `web_fetch`。
- Tavily / Serper / Brave / DeepSeek 四种形状均返回非空结果；`/v1/tools/fetch` 抓取 `https://example.com` 返回 `Example Domain`；`/extract` 并发抓取两个 URL 正常。
- Exa / Firecrawl v1+v2 / SearXNG / Google CSE / You.com / Bocha / 智谱 / Jina 的规范路径均返回正确形状；原生别名 `/v1/search`、`/v2/search`、`/customsearch/v1`、`/v1/web-search`、`/api/paas/v4/web_search` 正常。
- Google CSE 的 `?key=` 鉴权有效；无 key 的 SearXNG 请求返回 401 和 `{"error":...}`；相同查询第二次命中缓存（Exa 实测 0.8ms）。
- 无 key → 401；私网 URL → 400；空 query → 400；Magpie key 失效 → 502 且不泄露 key。

当前 Magpie 实例使用“会搜索的模型”，返回 Google grounding 的 `vertexaisearch.cloud.google.com/...` 跳转链接（实测 302 到真实页面）。适配器会把模型写的要点解析成独立的 `results`；`site_name` 是跳转域名，`content` 是要点摘要，没有每页正文。这是当前部署的预期行为。若以后需要真实 URL + 页面正文，可按“Magpie 搜索源”的可选步骤切换 search API，适配器无需改动。

## 部署到服务器（1Panel 编排 + 反向代理，直连对外）

适配器只监听宿主机的 `127.0.0.1:8787`，由 1Panel 的 OpenResty 提供 HTTPS 和反向代理；公网只开 80/443。

### 1. 准备代码和配置

在服务器上（1Panel 的编排目录，或任意目录）：

```bash
mkdir -p /opt/1panel/docker/compose/magpie-search-adapter
git clone https://github.com/Sakiko15/magpie-search-adapter.git /opt/1panel/docker/compose/magpie-search-adapter
cd /opt/1panel/docker/compose/magpie-search-adapter
cp docker-compose.example.yml docker-compose.yml
cp .env.example .env
openssl rand -hex 32   # 输出填到 .env 的 ADAPTER_API_KEYS
```

编辑 `.env`，至少要改：

```dotenv
ADAPTER_API_KEYS=<上一步的输出；多个客户端用逗号分隔，各给一个 key>
MAGPIE_API_KEY=<有效的 Magpie gateway key>
MAGPIE_BASE_URL=https://magpie.127001001.xyz
```

`ADAPTER_API_KEYS` 是客户端 key；`MAGPIE_API_KEY` 只给适配器用，两者不要混。`ALLOW_KEYLESS_CIDRS` 保持空——反代后适配器只看到 OpenResty 的来源地址。

### 2. 用 1Panel 编排启动

1Panel → 容器 → 编排 → 创建编排 → 路径选择 `/opt/1panel/docker/compose/magpie-search-adapter/docker-compose.yml`，构建并启动。也可以在终端执行：

```bash
docker compose up -d --build
```

compose 里已经写了 `ports: ["127.0.0.1:8787:8787"]`，端口不会直接对公网开放。若 1Panel 拉取 `golang:1.26-alpine` 失败，先在 1Panel 的容器 → 配置 → 镜像加速里配置 Docker Hub 镜像。

验证容器：

```bash
curl -s http://127.0.0.1:8787/healthz
curl -s http://127.0.0.1:8787/readyz
```

### 3. 1Panel 反向代理 + HTTPS

1. 1Panel → 网站 → 创建网站 → 反向代理，填写域名（例如 `search.example.com`）。
2. 代理地址填 `http://127.0.0.1:8787`。
3. 创建后在网站 → HTTPS 里申请证书并开启强制 HTTPS。
4. 打开该网站的反向代理配置（1Panel 网站 → 反向代理 → 配置文件/配置修改），把超时调到大于适配器的 90s fetch：

```nginx
client_max_body_size 2m;
proxy_connect_timeout 30s;
proxy_send_timeout 300s;
proxy_read_timeout 300s;
```

1Panel 的 OpenResty 默认 `proxy_read_timeout` 是 60s，不改会在 `/v1/tools/fetch` 或较慢的搜索上先于适配器返回 504。

如果 OpenResty 无法访问宿主机的 `127.0.0.1`，把代理地址改成宿主机内网 IP（如 `http://192.168.1.10:8787`）或 Docker 网桥地址 `http://172.17.0.1:8787`。

### 4. 防火墙

1Panel → 主机 → 防火墙（以及云厂商安全组）只放行 22/80/443；不要放行 8787。端口已经绑定在 `127.0.0.1`，即使误开防火墙也不会直接暴露。

### 5. 验证

```bash
curl -s https://search.example.com/healthz
curl -s https://search.example.com/readyz
curl -s https://search.example.com/search \
  -H "Authorization: Bearer <ADAPTER_API_KEYS 里的 key>" \
  -H "Content-Type: application/json" \
  -d '{"query":"golang","max_results":3}'
```

期望 `/readyz` 返回 `{"ready":true}`，搜索返回非空 `results`。

### 6. 客户端怎么填

base URL 是 `https://search.example.com`，按“客户端 base URL 怎么填”的表加厂商前缀：

| 客户端 | base URL |
|---|---|
| Tavily | `https://search.example.com`（`POST /search`） |
| Exa SDK | `https://search.example.com/exa` |
| Firecrawl SDK | `https://search.example.com`（`POST /v1/search`、`/v2/search`） |
| SearXNG 客户端 | `https://search.example.com`（`GET /search?format=json&key=<key>`） |
| Google CSE | `https://search.example.com`（`GET /customsearch/v1?...&key=<key>`） |

SearXNG/Jina 这类不能加 header 的客户端用 `?key=`；不要用 `ALLOW_KEYLESS_CIDRS`（反代后来源地址都是 OpenResty）。

### 7. Key 管理与限流

- 每个客户端一个 key，写在 `ADAPTER_API_KEYS`（逗号分隔）；删除某个 key 后重启容器即可吊销。
- 适配器只有全局并发上限 `MAX_CONCURRENCY`，没有按 key 的配额。公网部署建议在 1Panel 的 OpenResty 里加限流，或按客户端固定 IP 做网站 ACL/防火墙白名单：

  ```nginx
  # http 段
  limit_req_zone $binary_remote_addr zone=magpie_search:10m rate=5r/s;
  # location 段
  limit_req zone=magpie_search burst=10 nodelay;
  ```

- `ADAPTER_API_KEYS` 用长随机串；泄露后从 `.env` 删除并重启容器，同时给客户端换新 key。

### 可选：接入 new-api

需要 new-api 的 token/配额/计费时，用 `docker-compose.new-api.example.yml` 把适配器加入 new-api 的 compose 网络：

```bash
docker compose -f docker-compose.new-api.example.yml up -d
# 项目名不同时：
NEW_API_NETWORK=<实际网络名> docker compose -f docker-compose.new-api.example.yml up -d
```

在 new-api 的网络里验证：

```bash
docker run --rm --network <实际网络名> curlimages/curl -s \
  http://magpie-search-adapter:8787/readyz
```

然后在 new-api 后台 → 渠道 → 添加：类型 **DeepSeek**，Base URL `http://magpie-search-adapter:8787`，密钥用 `.env` 里的 `ADAPTER_API_KEYS`，模型只勾 `deepseek-search` / `deepseek-fetch`。其余步骤见上面“接入 new-api（可选）”。

Magpie 也在 Docker 里时，`MAGPIE_BASE_URL=https://magpie.127001001.xyz` 可用；如果服务器无法访问自己的公网域名（hairpin NAT），改成 Magpie 的服务名/端口，或 `http://host.docker.internal:<端口>` 并打开 `extra_hosts` 的注释。

只想单容器试跑：

```bash
docker build -t magpie-search-adapter .
docker run --rm -p 127.0.0.1:8787:8787 \
  -e ADAPTER_API_KEYS=change-me \
  -e MAGPIE_API_KEY=sk-magpie-key-... \
  -e MAGPIE_BASE_URL=https://magpie.127001001.xyz \
  magpie-search-adapter
```