# 模型 API 代理兼容性审查

日期：2026-10-29。范围：OpenAI Chat/Responses、Anthropic/Bedrock、Gemini、Qwen、GLM、Perplexity、LiteLLM，以及公共 HTTP 客户端和 deployment 路由。

修复前的审查结论：当时实现能覆盖多种文本请求，但不同入口的错误处理、工具调用和参数语义不一致，不能据此认为所有 agent 客户端都能互换使用所有模型。最应优先修复的是失败伪装成功、工具信息丢失、重复生成和实际模型身份变化。

## 修复状态（2026-10-29）

下方审查正文保留修复前的发现，代码位置也对应当时版本。原来的 13 个失败探针已转为仓库中的正式回归测试，并在修复后通过。

| 审查项目 | 实施结果 |
|---|---|
| 1. 流式 HTTP 失败伪装成 200 | 流前统一检查状态；保留错误正文、限流与 request ID 响应头；流中错误使用协议 error 事件 |
| 2. 原生 Responses 无 streaming | 实时转发原生 SSE，观察 response ID 和 usage；首帧不等待生成结束 |
| 3. Gemini 工具信息丢失 | 映射定义、调用、并行结果关联和 thought signature；stream/non-stream 均保留工具调用 |
| 4. Qwen 重复完整生成 | 移除能力探测；只在流前明确参数不支持时有限回退 |
| 5. Claude/GLM 流错误及大参数丢失 | 共用完整 SSE 帧解析，8 MiB 上限；读写/解析错误与断流明确失败；Responses 桥也使用同一读取器 |
| 6. 静默切换型号 | 移除 substring/family fallback；只匹配精确型号、显式别名与 vendor prefix；failover 固定最初解析的型号 |
| 7. 正常 Gone 被拉黑 | 只解析错误 envelope；generic endpoint 404 不使 deployment 失效 |
| 8. failed Responses 被转成成功 | failed/cancelled/invalid 对象转错误；映射 incomplete/refusal；未知 incomplete 原因按 length 结束；工具参数完成事件不一致时失败 |
| 9. 参数被改变 | 恢复 Claude temperature，显式 token 限制不会被推理预算抬高；GLM stop 转数组，保留转换后的工具定义/choice，拒绝无法映射的 reasoning/n/logprobs 等选项 |

附加修复：Responses 路由改为每 handler 的有界 TTL registry，previous_response_id 固定原部署，失败 DELETE 保留记录；Anthropic 非 Claude 非流式入口共享 provider 路由和 Responses 回退；LiteLLM 未实现的选项明确返回 400；多条 system/developer 指令保留；Claude 思考文本以 reasoning_content 输出；缓存增加地址、型号、资源组、端点/API 版本隔离和一小时 TTL；已知同型号实例全部退役时节流刷新 discovery；各流式路径在输出前支持同型号实例切换；设置请求与错误/流帧大小限制、响应头/读取空闲超时和写失败停止读取；修复 OAuth 缓存读锁和响应读取错误。

新增测试覆盖：多行/末尾 SSE 帧、大于 1 MiB 的 Responses 工具参数、客户端写失败停止读取、Gemini JSON array/SSE usage、超大 JSON 值拒绝、缓存隔离/到期、Responses affinity 到期及满容量更新、latest 别名 failover 固定版本、流前同型号切换，以及 stream/non-stream 限流响应头。

尚存的明确边界：

- Responses affinity 仍为内存状态：重启丢失，多实例需要 sticky routing；24 小时 TTL、最多 10,000 个 ID。没有引入共享存储。
- Anthropic 入口不支持非 Claude streaming/thinking；LiteLLM 不支持 streaming、legacy prompt 和未实现参数。通过明确 400 避免静默丢失语义。
- Gemini 非零 reasoning_effort、remote image、禁止并行工具需要原生 API；模型专属参数应优先使用原生协议。
- Chat 桥接不能完整往返 Claude 签名思考历史。adaptive thinking/output_config 与各模型的预算/采样规则仍需 SAP 实测。
- GPT-6.1-sol / GPT-6-astra 的实际可用性、SAP API 版本、strict/schema 限制和 provider headers 尚未通过真实 SAP 调用验证。OpenAI Docs 的获取限制仍适用。

最终验证结果在文末记录。本次仅修改工作区，没有部署或提交。

## 验证方法与边界

阅读了实际 handler、转换器、流式管道、deployment/client 实现和现有测试。使用 Go overlay 加载 `/private/tmp/proxy-api-audit` 下的临时探针，没有为本轮审查改动业务代码。

```sh
go test -overlay=/private/tmp/proxy-api-audit/overlay.json \
  ./internal/handler/openai ./internal/stream ./internal/sapclient ./test \
  -run '^TestAudit' -count=1
```

13 个目标行为断言失败，分别复现下列问题。这些失败是审查证据，并不是已修复的回归测试。探针使用本地模拟上游，没有调用真实 SAP 模型。

上一轮的 Responses 桥接修复及其回归测试仍保留。本报告包含桥接之外的剩余问题。上一轮完整 `go test ./...` 和 `go vet ./...` 通过，说明现有测试对这些行为的覆盖或预期不足。

尝试读取 OpenAI Docs 的官方 Responses 迁移文档，两处地址分别返回 Forbidden 和访问拦截页面，未取得文档正文。因此以下“确定问题”基于代码和探针；涉及新模型的 strict、推理签名等规则单独列为待真实协议验证的风险。

## 优先修复的确定问题

### 1. P1：多个流式入口把上游 HTTP 失败返回成 200

位置：`internal/handler/anthropic/messages.go:190`、`internal/handler/openai/glm.go:45`，以及 Qwen、Perplexity、Gemini 的 streaming 分支。

这些代码已取得 upstream.StatusCode，却直接调用 `c.Status(200)` 并进入流式转换。探针中 Anthropic 上游 429 被客户端观察为 200。部分转换器还会丢弃错误正文，最终只留下空流。

影响：SDK 无法依据 429/503 和 Retry-After 正常重试；agent 可能显示取消、空回复或正常结束。内部 metrics 记对状态不能修复客户端协议。

建议：写出 SSE 前统一检查上游状态，非 2xx 保留 HTTP 状态、Content-Type、错误正文和重试/请求 ID 响应头。写出 SSE 后发生的错误才使用对应客户端协议的流式 error 事件。主 OpenAI Chat 分支已经采用前一种检查方式，可以作为统一行为的参考。

现有 `test/qa_fix_regression_test.go` 多处刻意断言“客户端仍然收到 200”。修复需要同步调整这些历史行为测试及 README，不能把它们当作正确协议的依据。

### 2. P1：原生 Responses 入口没有真正支持 streaming

位置：`internal/handler/openai/responses.go:47`。

CreateResponse 不检查 stream，使用带 300 秒 timeout 的 Do，完整 ReadAll 后固定用 application/json 返回。探针中 `stream:true` 得到的 Content-Type 是 application/json。

影响：使用 Responses streaming 的客户端不能及时收到事件，可能等待到生成结束或超时；SSE 正文也不能作为 JSON 解码。流式 response ID 和 usage 不能按现有 JSON 逻辑提取。

建议：原生 Responses 增加独立的流式透传路径，按事件观察 response ID/usage；在上游结束前转发首帧。保留原生协议，无须绕回 Chat 转换器。

### 3. P1：Gemini 经 OpenAI Chat 入口会丢失工具调用

位置：`internal/handler/openai/gemini_bridge.go:14`、`:32`、`geminiToOpenAIResponse`，以及 `internal/stream/gemini_to_openai.go`。

请求结构只有 text/inlineData，没有 tools/functionDeclarations、functionCall/functionResponse；响应转换也只读取 text。探针确认工具定义消失，返回的 functionCall 被转换成 content 为空、finish_reason 为 stop 的 Chat completion。

影响：模型不能收到 agent 的工具列表；即使上游产生工具调用，agent 也无法执行并回传结果。工具角色还会被按普通 user 消息处理。

建议：完整实现“定义 → 调用 → 结果 → 后续调用”的双向映射和并行调用关联；在完成之前，对这条路由上的工具请求明确报不支持，避免接受请求后静默删字段。原生 Gemini 入口的正文透传不等同于这条桥接路径已经支持工具。

### 4. P1：Qwen 的能力探测会重复执行完整生成

位置：`internal/handler/openai/qwen.go:81`。

带 reasoning_effort 的 streaming 请求，先用真实完整 body 调用 Do 并 ReadAll，再调用 DoStreaming。body 仍带 stream:true，第一步实际可能就是完整消费一次生成流。成功时没有正向能力缓存，后续请求仍会重复。

探针确认一次客户端请求触发两次上游 inference POST。

影响：首帧时间多出一次完整生成；两次请求都可能计费，而第一份 usage 未计入代理统计。

建议：直接调用流式端点，只在尚未写出任何客户端内容且收到明确的参数不兼容 400 时做有限重试。不要用真实完整生成作为能力探针。

### 5. P1：Claude/GLM 转 Chat 的流错误与大工具参数被吞掉

位置：`internal/stream/anthropic_to_openai.go:64`、`:175`；同类 64 KiB 限制见 OpenAI、Anthropic、Gemini 转流管道。

该转换器没有向客户端映射 error 事件；Scanner 限制单行 64 KiB，且不检查 scanner.Err。没有 message_stop 时也写出正常 finish 和 DONE。

探针结果：上游 overloaded_error 消失，客户端收到 stop；70 KiB 的工具参数 delta 消失，客户端仍收到 tool_calls 完成信号。

影响：文件编辑、大段 JSON 等常见 agent 工具参数会被截断，客户端可能执行空参数或无法解析参数；上游失败被表现为成功。

建议：共用有明确大小上限、支持 SSE 帧解析的读取器；捕获读错误、解析错误和缺失终止事件，输出协议对应错误，绝不宣告工具调用成功。上限需要覆盖合理的大参数，超过上限时必须明确失败。上一轮修复的 Responses 转 Chat 管道不能替代其他管道的修复。

### 6. P1：找不到指定模型时可能静默切换型号

位置：`internal/sapclient/deployments.go:167`、`:182`、`extractFamily`。

gpt-6.1-sol 被提取成家族 gpt。探针仅提供 gpt-4o deployment 时，GetDeployment 返回了 gpt-4o，而不是找不到指定模型的错误。转换后的响应又经常标注客户端请求的 model。

影响：用户以为在用指定模型，实际推理能力、工具限制、上下文和费用都可能不同；后续适配还可能按原请求的 provider 选择错误协议。

建议：默认只允许精确型号和显式 alias；跨型号 fallback 必须通过明确配置限定候选顺序。deployment failover 优先切换同一型号的实例。记录 requested_model 和 resolved_model，不能用请求标签冒充实际模型。

### 7. P1：正常回复 Gone 会触发拉黑和重试

位置：`internal/sapclient/deployments.go:238`。

isRetryableStatus 对整个正文搜索字符串 `"Gone"`。探针返回 HTTP 200、message.content 为 Gone 的合法 completion，结果仍被判为 deployment 失效。

影响：已成功生成的回复被丢弃；deployment 被拉黑一小时；可能再次生成、切换型号或最终返回 502。

建议：解析明确的错误 envelope，只对 404/410 或约定的上游 error 字段判断失效；正常 choices/output 内容不得触发状态控制逻辑。端点不支持的 404 也应与整个 deployment 下线区分。

### 8. P1：非流式 Responses 失败对象被转换为正常空回复

位置：`internal/handler/openai/responses_bridge.go:266`、`chatViaResponses`。

转换器读取 status，但没有处理 failed/error。探针把 status:failed、带 server_error 的对象转换成 finish_reason:stop 的空 completion。

建议：先验证 Responses 终态和错误对象，再进行成功响应转换。failed 必须变成可识别的上游错误；invalid JSON 和缺少合法结果的对象也不能按成功处理。refusal 内容目前也没有映射，需补充协议测试。

### 9. P2：请求参数会静默消失或被改变

位置：`internal/transform/anthropic.go:14`、`internal/handler/openai/chat.go:508`、`internal/handler/openai/glm.go:121`。

探针确认：Claude temperature:0.2 被丢弃；max_completion_tokens:64 加 high effort 被改为 max_tokens:11024；GLM 接收 stop:"END" 后发出 stop_sequences:"END"，没有转成数组。

影响：采样设置失效，调用方的生成上限被绕过，或合法客户端请求变成错误的上游参数。

建议：按模型能力映射 temperature 和 stop；budget 与显式 token 上限冲突时返回明确参数错误，或采用事先定义的可见策略，不能自动大幅提高上限。不要仅因模型不支持就静默删除影响语义的参数。

## 从代码确定的剩余缺口

- **Responses 状态管理不完整**：response 路由表是全局内存 map，没有 TTL，重启/多实例不共享；CreateResponse 不根据 previous_response_id 固定到原 deployment；DELETE 无论上游是否成功都删除本地映射。失败删除后，后续 GET/DELETE 可能无法再访问仍存在的上游资源。
- **不同客户端入口没有共享同一执行逻辑**：Anthropic 入口的非 Claude 分支直接请求 Chat，不经过 GPT 的 Responses 能力回退；streaming 明确拒绝。Gemini/Qwen/GLM 从该入口使用时也没有进入相应原生 adapter。因此“有四种入口”不等于“任一模型在任一入口都兼容”。
- **LiteLLM 请求语义缩水**：Chat 与旧 Completions 走相同处理，但只读取 messages，不转换 prompt；Orchestration 固定 stream.enabled:false，且未传递调用方的多数生成参数。需要明确支持范围，或转换/拒绝，而不是接受 stream:true 后返回一次性 JSON。
- **指令与推理转换不一致**：Claude system 提升只识别 system 和字符串内容；Gemini 多个 system 消息只保留最后一个，developer 被归为普通 user；Claude 转 Chat 对 thinking 只输出标签，忽略 thinking_delta/signature_delta，而非流式又直接丢弃 thinking。应制定保持角色优先级和推理元数据的协议策略。
- **错误和正文读取缺乏公共约束**：多处忽略 io.ReadAll 的错误；绝大多数推理请求没有 body 上限；流式写失败后没有统一停止上游读取。DoStreaming 没有响应头/空闲超时，完整 body buffering 会增加内存占用。应区分连接/响应头/空闲超时与总生成时长，并传播取消。
- **缓存粒度过粗**：reasoning/thinking/Responses 能力缓存是全局永久布尔值，无法表达不同 endpoint、effort 值、tool 类型和 API 版本的差异；deployment 下线时路由缓存 12 小时，流式路径又通常没有同型号 failover。刷新路由与失效能力缓存需要关联。

## 需要真实上游核验的协议风险

1. Responses 与 Chat 对未显式指定 strict 的工具 schema 默认行为是否一致。桥接当前只转发已有 strict，不声明兼容策略；可选字段、additionalProperties 和 schema 限制需要真实 SAP 验证。
2. chatToResponsesBody 丢弃 n、stop、logprobs 等字段；responsesTools 会把非 function 工具也按 function 组装。图片 detail、refusal、音频/文件等内容未完整映射。需建立支持清单和拒绝规则。
3. 新 Claude 模型的 adaptive thinking、output_config.effort、工具 choice 限制，以及 Gemini 的 thoughtSignature/推理上下文回传规则。当前统一预算表和字段白名单不足以代表所有版本能力。
4. SAP 各 deployment 的 API 版本、原生 Gemini 的 SSE/JSON-array 返回形态和必要 provider headers。当前两条 Gemini 管道采用不同形态假设，应按实际响应 Content-Type 和文档验证。

## 建议的模块边界与实施顺序

先修复外部行为，再抽公共模块，避免一次性重写所有转换器。

1. **第一批：成功/失败与模型身份**。统一流前 HTTP 状态处理，修复 Gone 判断和严格型号路由，移除 Qwen 完整生成探测，处理原生 Responses streaming 和非流式 failed。
2. **第二批：agent 工具闭环**。修复 Claude/GLM 的 error/截断处理；补齐 Gemini 工具往返；校验 schema、并行调用 ID、截断参数和指令角色。尚未支持的组合明确拒绝。
3. **第三批：共享执行层**。客户端 surface 负责输入/输出协议；provider adapter 负责上游协议和能力；共享 executor 负责同型号 deployment 选择、HTTP、有限回退、取消和错误。缓存按 deployment+endpoint+API 版本+能力项组织，带 TTL；原生协议尽可能透传。

核心原则：保持请求语义、真实模型身份、工具调用关联和失败信号。不能为了取得 200 就静默删掉工具、关闭推理或替换型号。

## 应补充的行为测试

在“客户端 surface × 上游 provider × stream/non-stream”矩阵中覆盖实际支持的组合，并明确不支持的组合。至少包含：

- 文本请求、developer/system 多条指令、图片及结构化输出；
- 多轮/并行工具调用，空参数，大参数，tool result 与 call ID 关联；
- 400 参数错误、401 刷新、429+Retry-After、500、端点 404 与 deployment 410；
- 首帧在上游完成前到达、错误事件、64 KiB 以上帧、断流与客户端取消；
- 明确型号不存在、同型号实例切换、正常内容包含 Gone；
- Responses stream、failed/incomplete/refusal、previous_response_id 和失败 DELETE；
- 无隐藏重复生成，usage 与客户端 include_usage 独立，stream/non-stream 的参数语义一致。

## 修复后的最终验证

- `go test ./...`：通过，包括原 13 个审查探针及新增协议回归。
- `go test -race ./internal/sapclient ./internal/handler/openai ./internal/stream ./test`：通过；覆盖认证缓存、能力缓存、Responses 路由及各流式 handler。
- `go vet ./...`、`go build ./...`、`git diff --check`：通过。
- golangci-lint：本机 2.10.1（Go 1.26 构建）在 Go 1.27 标准库上崩溃；从本机缓存的 2.15.0 源码使用当前 Go 编译到临时目录后，完整 `run ./...` 通过，0 issues。未修改项目依赖或全局工具安装。

以上均使用模拟上游，未调用或部署真实 SAP 服务。
