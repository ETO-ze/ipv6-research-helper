# 开发和验证

## 环境与构建

Windows x64；Go 1.24+；系统 .NET Framework C# 编译器和 WPF 程序集。界面使用系统编译器兼容语法，不依赖 Electron 或浏览器容器。当前版本为 0.8.1，平台登记为 Epic + Steam + 37 类科研来源，共 39 项；0.8.0 历史构建使用 Go 1.27.1 和 Framework64 编译器。

```powershell
git clone https://github.com/ETO-ze/ipv6-research-helper.git
cd ipv6-research-helper
go test ./...
go vet ./...
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/build.ps1
```

build.ps1 先构建 Go 引擎，再将引擎、XAML 和图标嵌入 WPF EXE，输出 `output/IPv6-Research-Helper-0.8.1-windows-x64.exe`。输出目录不入 Git。本地兼容管理页保留在 dashboard.html；桌面入口用 `--no-open --manual` 启动引擎，不打开浏览器。

## 测试层次

1. 默认回归：域名限制、转发、IPv4 回退拒绝、DNS 证据、错误分类、格式、断点下载、节点策略和规则恢复，以及 Steam 精确主机发现、hosts 管理和 TLS 转发边界，不依赖真实公网。
2. 可选 UE 测试：原始数据块属于 Epic，不随仓库分发。下列脚本从官方 CDN 获取并校验；缺失时仅跳过该项，不算真实链路通过。
3. 在线验收：软件内运行 Epic 验收、Steam 实际块验收或来源检测，需要本机 IPv6，结果依网络和时间变化。Steam 样本链路成功不等于 Steam 客户端进程已经使用该入口。
4. 原生交互：启动最终 EXE，实际检查图标、主题、按钮、下载和恢复；编译通过不能替代这些检查。

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/fetch-ue-fixture.ps1
go test -run TestRealUEChunkIntegrity -v
```

样本获取可经过开发机原有网络，只负责取得指定数据，不证明 IPv6。严格 IPv6 要使用软件的在线验收。实时 Raw 测试是显式开关：

```powershell
$env:IPV6_LIVE_TEST = '1'
go test -run TestLiveResearchRaw -v
Remove-Item Env:IPV6_LIVE_TEST
```

GitHub Actions 在 Windows 运行默认测试、go vet 和桌面构建，提供 CI 构建附件；它不验证用户主机的 IPv6、Clash、管理员 hosts 接管、实际 Steam 客户端或完整 Epic 安装，不自动发布 Release。0.7.0 的历史验收仍保留在 [2026-09-30 记录](acceptance/2026-09-30.md)，不能计为新增 Steam 模块的实测。

## 实现边界

本地管理服务监听 127.0.0.1:17890，HTTP 代理为 127.0.0.1:17891；还保留 Epic 本机映射监听入口。修改操作需要每次启动生成的本机 token。引擎执行精确域名允许列表，出站显式使用 tcp6，HTTPS 保留证书校验。

### 0.8.1 验收状态持久化

Steam/Epic 保存最近一次实际链路验收结果，启动时重新载入，通过与失败都必须保存，最近失败覆盖旧成功。接管启用、DNS 成功或旧报告文件存在都不能直接生成通过状态。接口记录包含 `status`、`passed`、`checked`、`scope`、`detail`、`version`；原生界面在平台列表和详情展示结果、时间、版本与范围。

验收记录与接管状态独立：停止接管不抹掉历史结果，历史通过也不证明当前或未来网络可用。科研来源沿用逐来源 health 和真实样本状态，不由 Steam/Epic 的验收结果覆盖。回归应分别覆盖无记录、成功保存和重载、失败覆盖成功、记录异常及科研 health 重载；最终 EXE 的界面显示还需单独检查。

### Steam 接管（自 0.8.0）

`steam.go` 从本机 Steam 内容日志提取符合严格格式的 `cacheN-region.steamcontent.com` 名称，按精确域名建立服务。用户手动刷新并重新应用后，管理员写入的管理块同时映射到 `127.0.0.23` 与 `::1`，本机 80/443 入口按 HTTP Host/TLS SNI 转发，外网上游为该主机官方 AAAA 的 TCP6。TLS 字节原样转发，不安装证书、不解密 Steam 流量，不依赖 Clash，也不将整个 Steam 域名后缀指向任意同一节点。

hosts 应预先检查权限、同域名冲突及端口占用，并保存恢复记录。停止只删除本程序管理块；遇到用户或其他工具修改时保留冲突与备份，不能覆盖整个 hosts 文件。原有 UsbEAm Hosts Editor `#UHE_` 记录不应被清理。日志仅提取主机名，不将账号、manifest 授权 URL 或整个客户端配置放入公开报告。

停止 Steam 接管时先阻止跨恢复期的新拨号并关闭所管理主机的现有转发连接。Windows 原子替换遇文件占用时最多重试 3 秒，每次重新核对原快照；持续失败或检测到外部修改时保留恢复记录和备份，不原地覆盖 hosts。

已发现主机不代表捕获了未来新 CDN；新域名需手动刷新、重新应用，再由 Steam 暂停/继续建立新连接。登录、商店、游戏 UDP、未知域名及下载地区/凭据不在本功能范围。原软件国区镜像仅作只读研究，未接入本版。

`steam_verification.go` 检查匿名真实块的 HTTP 状态、745,040 字节长度、内容类型、块 ID 响应头及 SHA256。参照哈希来自 2026-10-05 六个官方缓存的独立 IPv6 下载，非 Valve 发布者校验值；载荷没有解密到原始游戏文件。独立代理/本地 TLS 样本检查，即使未启用 hosts 也可能通过，所以报告必须说明接管状态，客户端实际使用回环入口仍需 Steam 日志和进程连接证据。

操作、节点和失败证据见 [Steam 说明](STEAM.md)；参考软件的静态证据及版本边界见 [只读分析](ORIGINAL-SOFTWARE-ANALYSIS.md)。

不要扩大为任意公网代理；不要自动接受未知重定向、任意上游、私网 IP 或 IPv4。新来源应加入官方样本与明确域名，验证真实文件后再描述可用范围。DoH 与文件下载的失败原因分别保留。

新增颜色使用主题资源，诊断不能硬编码成功标识。签名 URL 不得在公开日志/报告中泄露，发布证据只保留公共样本和必要字段。

## 发布检查

- 保留配置，验收切换后恢复原接管服务。
- 验证最终 EXE 的真实文件、原生 UI 和恢复功能，记录未覆盖范围。
- 源码只含实现、文档和明确依赖；运行目录、私人任务、下载内容、参考软件不入 Git。
- Release 包含 EXE、说明、第三方许可、校验和；源码快照对应同一 Git tag。
- 发布后检查默认分支、远端提交、资产状态/大小/摘要，并从公开地址回读校验。
