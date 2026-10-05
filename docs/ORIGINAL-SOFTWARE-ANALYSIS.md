# UsbEAm 游戏下载助手的只读分析

本项目参考用户提供的 **UsbEAm 游戏下载助手**，研究其平台选择和下载连接接管方式，再独立实现所需功能。原软件作者为 **羽翼城 / Dogfight360**；本项目由 **ETO-ze** 开发，不表示获得原作者或游戏平台的官方关联。

分析日期：**2026-10-05，Asia/Shanghai**。依据是用户提供的 EXE、其可读构建信息和嵌入资源、原作者公开说明，以及分析时只读获取的远程规则快照。原 EXE、完整前端代码、图标编码及完整第三方规则没有随本项目发布。

## 分析了什么，未恢复什么

| 资料 | 已确认内容 | 证据边界 |
| --- | --- | --- |
| 用户提供的 Windows EXE | Windows AMD64 GUI；Go 1.25.0，CGO 关闭；Wails `v3.0.0-alpha.71`、WebView2 封装依赖 | 来自 PE 和 Go buildinfo，不表示恢复原始工程 |
| 可读嵌入前端 | Vue 界面、平台/网络/CDN/上游选择、启动/停止和节点控制调用 | 可确认界面调用及参数，不能直接证明每个后端分支的实际运行结果 |
| Go 运行时信息 | 仍可恢复函数名、类型和方法名称，包括 hosts、HTTP、DNS、隧道、节点策略及优选入口 | 原始函数体、变量含义和完整机器码控制流没有还原 |
| 作者远程 INI | 当前平台范围、监听器、HostSet、TargetGroup、forward/redirect 路由及策略字段 | 配置是快照，会更新；存在条目不等于该服务已通过本机下载验收 |
| 作者公开说明 | hosts 接管、本地端口转发、平台/网络选择、节点操作和部分 CDN 优选用法 | 属于原作者说明；本分析没有运行原软件加速或验收全部平台 |

原 EXE 长度 **11,848,704 字节**，SHA256：

```text
2EF10F74EC52011D2F263919D9595516A5FD26090886A8EDD6F0A91481EB1B0F
```

它的 PE CLR 目录为空，结合 Go buildinfo 和 Wails 依赖可确定，此样本使用 Go 后端和 WebView2 桌面界面。本项目采用 Go 引擎与 C# WPF 界面，两者的界面技术不同。

静态解析保留的 Go 运行时函数信息得到 12,073 个名称，其中 611 个属于应用命名空间或生成包装。这里的“读了 EXE”是构建信息、资源、配置和程序接口层面的分析，不能称为完整源码反编译。

## EXE 版本与远程规则版本

用户提供的 EXE 中可见版本文字为 **20260412**；分析时远程规则 `[Rules].Version` 为 **20260713**。原作者当前页面也列出 20260713 和较早版本的下载说明。程序版本、远程规则版本和当前网页标题应分别记录。[原作者说明与版本记录](https://www.dogfight360.com/blog/19189/)

EXE 中能看到配置地址，以及远程加载、解析和建立运行配置的函数入口。分析时的规则快照长度为 **326,361 字节**，SHA256：

```text
C69CFBF25F8E61D79BBE7E9C6267C46CB5B94FA8DA73FB28F0E545FC7D9EEDDC
```

远程规则地址：[作者的 INI 配置](https://www.dogfight360.com/Usbeam/dl2v6_v2.ini)。这个链接的未来内容可能变化，文中的计数和路由仅指 2026-10-05 获取的快照。

## 确认的功能范围

该规则快照包含 **17 个真实平台节**：14 个 PC 平台和 3 个主机平台。

- PC：Steam、Epic、Battle.net、Riot、Ubisoft、Xbox PC、EA Desktop、GOG、HIKARIFIELD、Rockstar、Wargaming、Paradox、Legacy Games、Amazon Games。
- 主机：Xbox、Switch、PlayStation。

原作者说明的主机方式包括代理和 DNS 配置，不能把 PC Steam 的 hosts 方式直接套用于主机。作者还提供 Windows、macOS 和 Linux 版本说明；本次分析对象是一个 Windows AMD64 EXE，其他系统及主机未实机验收。[原作者平台与使用说明](https://www.dogfight360.com/blog/19189/)

| 能力 | 交叉确认的静态依据 | 未据此声称的结果 |
| --- | --- | --- |
| IPv4 / IPv6 选择 | 前端网络选项、`tcp` / `tcp4` / `tcp6` 配置、按地址族筛选的函数入口 | 没有运行原软件验证全部连接都遵守所选地址族 |
| 平台、CDN 与上游选择 | 可读界面参数和配置中的平台/CDN/TargetGroup | 没有验收每个平台的所有 CDN |
| hosts 管理 | 预览、生成、应用、移除托管记录的接口 | 未执行原软件的 hosts 写入和恢复 |
| 本地 HTTP、显式代理、TCP 隧道和 DNS | 对应配置模式与 HTTP/CONNECT、SNI、DNS TCP/UDP 处理函数名 | 某模式存在不代表所有平台使用它 |
| 节点断开、拉黑与解除拉黑 | 前端操作、`DisconnectIPs` / `BlacklistIPs` 等接口及统计字段 | 未对原软件实际连接做断开/拉黑试验 |
| 上游分配策略 | `ordered`、`round_robin`、`random`、`sticky_failover`、`smart` 字段与相关入口 | 未复原所有评分和失效切换细节 |
| CDN 优选 | 延迟/速率测量、区间复测、应用结果的前端及后端接口 | 未复现整个测速算法，也没有实际测量所有候选池 |

原作者的说明也描述了节点选择、实时连接操作及部分 CDN 优选流程；这些与可读前端和配置对应。[原作者功能说明](https://www.dogfight360.com/blog/19189/)

## 原 Steam 配置如何接管

**当前快照的 `steam_hosts` 实际有 19 个唯一域名。** 这是对 HostSet 单独解析去重后的计数。它不是“全 Steam 域名列表”，也不能据此推断未来规则数量。

原 Steam 平台节只选择 **`http_main`** 监听器：

```text
Steam 内容请求
    ↓ HostSet 中 19 个精确下载/CDN 域名
系统 hosts → 127.0.0.19
    ↓ http_main，端口 80
按原始 HTTP Host 匹配 forward / redirect
    ↓ TargetGroup 的解析别名与所选 tcp / tcp6 / tcp4
CDN 节点，或带原路径的 HTTP 301 重定向
```

直接转发目标由 CDN 解析别名提供，`ip_mode = inherit` 随所选网络方式变化，目标组默认使用 `round_robin`。这不是让客户端填写 HTTP CONNECT 显式代理地址的 Steam 模式。

全局配置虽然还定义 443 TCP 隧道，但此 Steam 平台节没有引用它。原作者对主程序 80/443 转发能力的总体说明，不能替代 Steam 平台节的实际监听器配置，因此本分析没有把原 Steam 模式写成已覆盖 HTTPS。

以下是理解路由所需的少量配置事实，未复制完整原规则：

| 原请求域名示例 | 原规则解析目标或行为 |
| --- | --- |
| `dl.steam.clngaa.com` | 解析 `dl.steam.clngaa.com.z.ngaagslb.net`，端口 80 |
| `files.steam.nsclouds.cn` | 解析 `bjxlv4.y.ngaagslb.net`，端口 80 |
| `akk.gdtstream.com` | 解析 `akk.gdtstream.com.t.ngaagslb.net`，端口 80 |
| `xz.pphimalayanrt.com` | 解析 `xz.pphimalayanrt.com.m.alikunlun.com`，端口 80 |
| `st.dl.eccdnx.com` 等 | 解析 `zqctgdl.v.trpcdn.net`，端口 80 |
| 蒸汽中国的部分内容入口 | HTTP 301 到 `xz.pphimalayanrt.com` 或 `dl.steam.clngaa.com`，保留原请求路径 |

配置还提供 `xz.sycontroller.com` 的 forward route，但它没有出现在这 19 个 HostSet 条目中。静态配置有这一不一致，不能仅凭 route 存在就认定客户端流量会命中它。原 Steam 规则提示国区登录限制和多次重定向对速率统计的影响，覆盖范围也应按具体规则理解。

这些 CDN 别名的来源是原作者配置，而非 Valve 官方地址认证。只读实测发现，部分候选目前没有 AAAA、返回 403、出现证书/TLS 错误或跳转到没有 IPv6 的目标；具体结果见 [Steam 接入与实测](STEAM.md)。新实现以本机日志发现并独立验证的官方 SteamCache 精确主机为首批服务，不将原工具的整个候选池直接标为可用。

## 对本项目实现的影响

本项目保留“点击平台图标、选择服务、启动后在原客户端下载”的操作方式，独立编写 hosts、双栈回环监听、IPv6 上游、节点策略和验收逻辑。0.8.0 的 Steam 新路径增加原样 HTTPS 转发，以支持本机已经观察到的官方 SteamCache HTTPS HTTP/2 下载。

原软件的其他 16 个平台、主机连接方式、全部上游策略和完整 CDN 优选功能，不会因为读出了接口就自动成为本项目已实现的功能。当前产品范围仍以项目首页和版本验收记录为准。

旁边的 UsbEAm Hosts Editor 是另一个样本，可见 .NET/Avalonia 相关资源，与本次 Go/Wails 游戏下载助手的结构不同；本分析未完整解开其包或恢复节点数据库。本机保留的旧 `#UHE_` hosts 条目也未被当作新助手接管成果。

回到 [项目首页](../README.md) · [Steam 使用说明](STEAM.md)
