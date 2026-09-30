# 开发和验证

## 环境与构建

Windows x64；Go 1.24+；系统 .NET Framework C# 编译器和 WPF 程序集。界面使用系统编译器兼容语法，不依赖 Electron 或浏览器容器。发布版本实际使用 Go 1.27.1 和 Framework64 编译器。

```powershell
git clone https://github.com/ETO-ze/ipv6-research-helper.git
cd ipv6-research-helper
go test ./...
go vet ./...
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/build.ps1
```

build.ps1 先构建 Go 引擎，再将引擎、XAML 和图标嵌入 WPF EXE。输出目录不入 Git。本地兼容管理页保留在 dashboard.html；桌面入口用 `--no-open --manual` 启动引擎，不打开浏览器。

## 测试层次

1. 默认回归：域名限制、转发、IPv4 回退拒绝、DNS 证据、错误分类、格式、断点下载、节点策略和规则恢复，不依赖真实公网。
2. 可选 UE 测试：原始数据块属于 Epic，不随仓库分发。下列脚本从官方 CDN 获取并校验；缺失时仅跳过该项，不算真实链路通过。
3. 在线验收：软件内运行 Epic 验收或来源检测，需要本机 IPv6，结果依网络和时间变化。
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

GitHub Actions 在 Windows 运行默认测试、go vet 和桌面构建，提供 CI 构建附件；它不验证用户主机的 IPv6、Clash 或完整 Epic 安装，不自动发布 Release。

## 实现边界

本地管理服务监听 127.0.0.1:17890，HTTP 代理为 127.0.0.1:17891；还保留 Epic 本机映射监听入口。修改操作需要每次启动生成的本机 token。引擎执行精确域名允许列表，出站显式使用 tcp6，HTTPS 保留证书校验。

不要扩大为任意公网代理；不要自动接受未知重定向、任意上游、私网 IP 或 IPv4。新来源应加入官方样本与明确域名，验证真实文件后再描述可用范围。DoH 与文件下载的失败原因分别保留。

新增颜色使用主题资源，诊断不能硬编码成功标识。签名 URL 不得在公开日志/报告中泄露，发布证据只保留公共样本和必要字段。

## 发布检查

- 保留配置，验收切换后恢复原接管服务。
- 验证最终 EXE 的真实文件、原生 UI 和恢复功能，记录未覆盖范围。
- 源码只含实现、文档和明确依赖；运行目录、私人任务、下载内容、参考软件不入 Git。
- Release 包含 EXE、说明、第三方许可、校验和；源码快照对应同一 Git tag。
- 发布后检查默认分支、远端提交、资产状态/大小/摘要，并从公开地址回读校验。
