# Accio Work 接入第三方模型

此集成参考 [AI-Helper](https://github.com/TF49/AI-Helper) 的 Accio Work
启动变量和 ADK 协议，在 Magpie 内实现桥接。模型供应商和 API Key 由
Magpie 管理，不需要运行 AI-Helper，也不修改 Accio Work 的安装文件。

## 使用

1. 编译并启动此分支的 Magpie，让本地网关保持运行。
2. 在“供应商”页添加你的模型接口、API Key 和模型。
3. 在“智能体”页找到 **Accio Work**，选择一个 Magpie 模型。
4. Windows 和 macOS 桌面版：完全退出已运行的 Accio Work，然后点击
   智能体列表中 Accio Work 旁边的 **打开**。程序会带上接入设置启动，
   不需要打开终端。重复点击或旧进程未退出时会提示先退出，不会强制结束任务。

网页版仍提供复制启动命令，也可以在终端运行：

   ```powershell
   .\magpie.exe accio run
   ```

启动时注入的变量只作用于该进程。直接从原来的桌面快捷方式打开 Accio
不会自动经过 Magpie；已运行的 Accio 进程也不会重新读取启动变量。
在 Accio 的模型菜单中，可切换 Magpie 为它显示的其他模型。

Windows 也可以双击仓库根目录的 `Start-Accio-Magpie.cmd`。请先启动
同目录的 `magpie.exe`；脚本会检查模型列表及 Accio 是否已经退出。
脚本使用 `MAGPIE_ACCIO_BIN` 或程序自动检测的安装路径，也兼容
`D:\accio\Accio.exe`。从 GitHub 下载源码后，需要先编译
`go build -o magpie.exe`；源码仓库不包含编译好的程序或个人 API Key。

若 Accio 的旧会话或内部路由发送 `1Helix-…` 模型编号，桥接会先读取请求中
其他模型字段的明确选择；没有其他选择时，使用你在 Magpie 中为 Accio Work
选定的模型。未配置默认模型时仍报错。其他未知模型名称或写错的
`供应商/模型` 不会被静默替换。

常见安装位置会自动检测。若装在其他位置，可先在当前 PowerShell 中指定：

```powershell
$env:MAGPIE_ACCIO_BIN = 'D:\Apps\Accio\Accio.exe'
.\magpie.exe accio run
```

也可以在命令行添加供应商、选择模型和查看启动变量：

```powershell
.\magpie.exe provider add 'My API' id=my-api url=https://api.example.com/v1 key=YOUR_API_KEY models=YOUR_MODEL
.\magpie.exe accio magpie/my-api/YOUR_MODEL
.\magpie.exe accio env
.\magpie.exe accio run
```

将示例地址、密钥和模型替换为供应商提供的值。密钥只保存在 Magpie 的供应商配置中，
不会放进 Accio 的启动变量。默认模型保存在 Magpie 配置目录的 `accio.json` 中。
“断开”会移除 Magpie 的模型选择；退出使用旧变量启动的 Accio 后，按原方式打开即可。

## 当前范围

- 按 Accio 的模型目录格式列出 Magpie 允许该智能体使用的模型，兼容客户端
  实际使用的 POST 请求以及 GET 查询。
- 接受 ADK/Gemini `contents` 和 OpenAI `messages` 请求，包括 gzip 请求。
- 文本、图片输入、函数调用和函数返回复用 Magpie 的现有协议转换。
- 上游可以使用 Chat Completions、Anthropic Messages 或 Responses，具体取决于
  Magpie 中该供应商的配置和能力。
- 返回 ADK 的 SSE 完成事件；模型执行期间每 15 秒发送注释心跳。
  与参考桥接一致，正文和工具调用在完整回复结束后一起返回，不逐字输出。
- Embedding 请求复用 Magpie 的 `/v1/embeddings` 处理，需要请求明确提供
  Magpie 的 `供应商/模型` ID。本集成不会默认选择收费的 Embedding 模型。
- 账号、登录、团队、技能等允许的元数据请求只转发到固定的 Accio 官方域名；
  此入口仅接受来自本机的元数据请求，并拒绝重定向。
- `web_search` 和 `web_fetch` 使用的 `/api/web/search-proxy` 已接通：
  搜索及网页抓取继续使用 Accio 官方工具服务及已有 Accio 登录，
  与第三方模型接口分别处理。Accio 登录失效或官方工具限额仍会返回对应错误。
- MCP 官方工具代理 `/api/mcp/proxy` 已接通：保留 Accio 的登录、工具参数、
  JSON-RPC ID、JSON/SSE 回复与工具错误。SSE 逐块刷新，单次请求最多等待
  两分钟，桥接不会自动重试业务工具操作。具体工具仍依赖 Accio 账号的授权。
- 推理失败不会回退到 Accio 官方推理接口。图像生成、TPP/RLab 独立接口、
  未适配的 Accio 路径返回明确错误；这些功能尚未迁移。

如果模型可以回复，但搜索或抓取报 HTTP 501，请更新至包含上述搜索代理
适配的版本，并退出、重新打开 Magpie。若 MCP 工具仍报错，先检查日志中的
实际 HTTP 状态：旧版本的 `MCP proxy request failed: 501` 是桥接尚未适配，
不能据此认定为官方服务故障。MCP 通道已适配不等于所有发品接口均已支持；
TPP/RLab 独立接口仍未迁移。

## 验证

```text
go test -tags nogui ./internal/gateway ./internal/agent -run Accio -count=1
go test -tags nogui ./internal/gateway -run 'TestKeySlotEveryRoute|TestReadOnlyKeyEveryRoute|TestBrowserKeyEveryRoute'
go test -tags nogui ./internal/agent ./internal/agentenv -run 'TestFolderVarsAreListed|TestVarsAreRead|TestIsAgentByUserAgent'
```

测试使用临时用户目录及本地模拟上游，不调用真实模型服务。
真实 Accio Work 客户端与第三方 API Key 的联调仍需要实际安装和凭据，
模拟接口通过不能替代这一检查。

### 本地验证记录（2026-11-17 至 2026-11-18）

在 Windows 上使用 Go 1.27.4：

- Accio 专项测试、只读及浏览器密钥限制、并发入口分类、环境变量隔离、
  智能体注册检查通过。测试日志在 `dist/verification/focused-tests.log`。
- 仅在临时编译副本中移除 Accio 路由，`TestAccioWorkGateway` 会因模型列表
  返回 404 而失败；恢复实际源码后通过。记录在
  `dist/verification/without-route-tests.log`。
- Windows 程序的启动检查通过：使用临时配置及模拟客户端，确认启动变量实际传入。
- Windows 桌面版、Linux 无界面版编译和双平台 `go vet` 通过。
- 全项目测试未完成：已有的 `build/darwin/TestFreedHeapLeavesFootprint` 在
  Windows 上因 `/tmp/.../overlay.json` 路径不能被原生 Go 识别而失败；
  随后临时盘空间不足，已停止完整测试并清理本次临时编译文件。
  保留的部分日志在 `dist/verification/full-tests-incomplete.log`。
- `-race` 未能执行：当前环境未启用 cgo，且缺少所需的 C 编译环境。
- 初次构建未进行真实客户端联调；随后用户已确认聊天可以正常连接。
- 根据当前安装的 Accio 客户端，补齐模型目录的 POST 请求支持。
  新增测试在修复前复现 501，修复后 Accio 专项及网关访问限制测试通过；
  记录在 `dist/verification/accio-catalog-before.log` 和
  `dist/verification/accio-catalog-after.log`。
- 已启动修复后的 Windows 程序，实际 POST 模型目录返回 10 个模型；
  在已安装的 Accio Work 模型菜单中确认 DeepSeek 和 GPT 模型均可显示，
  不再出现“模型列表加载失败”。此检查未发送聊天消息。
- 内部“打开”入口：Windows/macOS 启动决策、已运行进程、无法读取进程、
  取消及失败路径、桌面与网页版入口、提示语翻译、界面脚本预算测试通过。
  Windows 模拟子进程实际收到四个接入变量，自定义安装目录的快捷方式检测通过，
  且未改写快捷方式。日志：`dist/verification/accio-open-tests.log`。
- Chromium 内核的界面测试通过：五种语言、900/400 像素窗口、失败后重试、
  网页版限制共 10 项；既有 agy/Cursor 启动控件回归 8 项通过。
  日志：`dist/verification/accio-open-ui.log`、
  `dist/verification/accio-launch-regression-ui.log`。本机旧版测试 Chromium
  在启动时崩溃，最终使用现有 Edge 完成验证；未执行 WebKit 和真实 Mac 桌面测试。
- 内部“打开”版本的 Windows 桌面程序编译通过。`-race` 仍因本机缺少
  cgo/C 编译环境而未能执行。
- 搜索代理回归：按安装客户端的真实接口格式，修复前复现 HTTP 501；
  修复后搜索、网页抓取、gzip、认证及区域信息、401/429、重定向拒绝、
  本机限制与未支持路径边界测试通过。原有 Accio 专项及网关限制测试通过。
  日志：`dist/verification/accio-web-before.log`、`dist/verification/accio-web-after.log`。
  此项使用模拟官方接口，未调用真实 Accio 搜索账号。
- Windows 本机日志确认 MCP 请求曾被桥接以 HTTP 501 拒绝，4097 正在监听。
  MCP SDK 请求的回归测试在修复前复现 501。修复后覆盖 JSON、SSE、工具错误、
  401/429、重定向拒绝、无效请求和流式刷新；不调用真实发品或修改店铺。
  日志：`dist/verification/accio-mcp-before.log`、`dist/verification/accio-mcp-after.log`。
