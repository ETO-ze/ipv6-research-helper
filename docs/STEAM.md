# Steam IPv6 下载接管

**0.8.0 新增 Steam 官方内容缓存接管。** 平台列表现在包含 **Epic + Steam + 37 类科研来源，共 39 项**。Steam 使用本机 hosts 和本地连接转发，启动后在 Steam 客户端照常下载，无需为 Steam 配置 Clash。

本文的节点和文件实测日期为 **2026-10-05，Asia/Shanghai**。节点可用性会随网络、下载地区和 CDN 调度变化；请以软件当前检测结果为准。

## 开始使用

1. 正常退出已有助手，右键本软件 EXE，选择“以管理员身份运行”。管理员权限用于修改系统 hosts；权限不足时，软件会提示原因。
2. 在 Steam 中按原方式开始一次你需要的游戏下载或更新，让 Steam 生成下载服务器日志。
3. 在助手平台列表选择 **Steam**，点击“**刷新下载服务器**”。助手从本机 `logs/content_log.txt` 读取符合格式的官方 SteamCache 主机名称，展示可选服务和上游 IPv6。
4. 选择已发现服务器组，或选择其中一个精确域名，点击“**启动 IPv6 接管**”。如果域名没有可用 IPv6、全部节点被拉黑、hosts 已有冲突规则或本地端口被占用，按提示解决后重试。
5. 回到 Steam，执行“**暂停 → 继续**”，让现有连接重新建立。已经建立的旧连接不会因为 hosts 更新而自动改道。
6. 点击“**验证 Steam 下载链路**”，查看本次接管状态、真实内容块和上游连接检查。助手样本验收与 Steam 客户端实际下载连接应分别核对。
7. 用完点击停止/恢复配置，或正常退出助手。本软件移除自己管理的 Steam hosts 块，保留其他 hosts 条目。

同时只启用一组服务。切换到 Epic 或科研来源时，按照对应接管方式操作；这些服务的 Clash 路径与 Steam 的本机 hosts 路径不同。

### 下载过程中出现新服务器

Steam 会根据地区和调度选择新的缓存。当前版本不会把整个 `steamcontent.com` 后缀都指向同一个节点，也不会在后台默默接管新域名。

如果 Steam 换了下载服务器，请点击“刷新下载服务器”，重新选择并应用服务，然后在 Steam 中再次“暂停 → 继续”。发现列表与当前已启用域名可能不同，以界面中的“接管域名”为准。未列入本次接管的域名仍按原网络方式连接，不能据此宣称整个 Steam 都只使用 IPv6。

## 连接方式

```text
Steam HTTP / HTTPS 下载
    ↓ 精确下载域名的系统 hosts 记录
本机回环 127.0.0.23 / ::1，端口 80 / 443
    ↓ HTTP Host 或 TLS SNI 确认目标域名
助手按该域名的官方 DNS AAAA 选择上游
    ↓ TCP6，失败不回退 IPv4
官方 cacheN-region.steamcontent.com 的 IPv6 节点
```

HTTPS 采用原样字节转发，保留 Steam 的原始 SNI、站点证书校验和 HTTP/2；助手不解密 Steam HTTPS，也不安装根证书。`127.0.0.23` 是同一台电脑上的进程间连接，不能将它算作外网 IPv4 回退。助手 DNS 查询也使用 IPv6 加密连接。

本功能不修改 Steam 下载地区、账号、凭据或 depot 权限。Steam 登录、商店、社区、游戏联机、UDP、远程串流，以及未登记的新下载主机不在该接管保证范围内。对路由器隧道内部或 CDN 到源站的链路协议，也不能由本机 TCP6 结果作出保证。

Steam 官方说明内容下载涉及 HTTP 80、HTTPS 443，登录和其他功能还使用额外 TCP/UDP 端口；通用代理允许域名的说明不等于 Windows Steam 内容下载一定采用系统 HTTP 代理。[Steam 官方端口说明](https://help.steampowered.com/en/faqs/view/2EA8-4D75-DA21-31EB)

## 上游 IP 的固定与拉黑

选中 Steam 服务后，在上游 IP 列表查看该精确域名的 IPv6 候选和实际连接结果：

- **固定所选 IP**：对该域名持续使用选定地址。地址必须仍在 DNS 候选中；失效时停止连接并提示。
- **拉黑 / 解除拉黑**：按域名排除或恢复指定节点。全部拉黑后停止连接，不转用 IPv4。
- **恢复自动**：恢复自动选择候选地址，已有黑名单继续保留。

固定和拉黑会断开受影响的旧连接，让新策略能够生效。DNS 列表里的地址只是候选，不能代替实际文件验证。

如果本机保留了 UsbEAm Hosts Editor 的旧 `#UHE_` 条目，本软件不主动删除它们。当前实测机器的旧 Steam 条目位于 CDN 解析别名，并非这批官方 `cacheN-region.steamcontent.com` 域名。遇到同一个接管域名已存在 hosts 记录时，应先在原工具中停止相关服务，再重新启用。

## 2026-10-05 独立节点实测

本机 Steam 原生日志已记录 `cache7-12-hkg1.steamcontent.com` 六个官方香港缓存通过 IPv6 建立 HTTPS HTTP/2 连接。随后独立使用 `curl --ipv6 --noproxy '*'`，连接各自主机经 IPv6 DoH 查询得到的地址，保留正常 TLS 证书校验，取得同一个真实 Steam 内容块：

| 官方下载域名 | 本次 AAAA / 实测远端 |
| --- | --- |
| `cache7-hkg1.steamcontent.com` | `2404:3fc0:2:100::671c:368a` |
| `cache8-hkg1.steamcontent.com` | `2404:3fc0:2:101::671c:3696` |
| `cache9-hkg1.steamcontent.com` | `2404:3fc0:2:100::671c:368c` |
| `cache10-hkg1.steamcontent.com` | `2404:3fc0:2:101::671c:3697` |
| `cache11-hkg1.steamcontent.com` | `2404:3fc0:2:100::671c:3686` |
| `cache12-hkg1.steamcontent.com` | `2404:3fc0:2:101::671c:369a` |

匿名样本路径：`/depot/730/chunk/ce09629928fe234c3785e60ce5e782a2b33c3c9f`。

六个节点均返回 HTTP 200、`application/x-steam-chunk`，完整 **745,040 字节**，下载载荷 SHA256 全部相同：

```text
C0C3800733DF222E5264B1CEE42EDA79C7EDBAB04D77658AE3AA5325F27C6A1D
```

响应的 `x-content-sha` 为块 ID `ce09629928fe234c3785e60ce5e782a2b33c3c9f`，`x-content-crc` 为 `3163229067`。下载载荷 SHA1 为 `E30A91DB985DB5EB0856B913FE343C47C56CA8F7`，与块 ID 不同。样本是 Steam 压缩/加密内容块，本次没有读取私有 depot 密钥或解密验证最终游戏文件。

上述 SHA256 是独立 IPv6 下载建立的**实测传输参照**，并非 Valve 发布的校验值或数字签名。长度、类型及参照 SHA256 可以用于核对助手是否完整转发同一块内容，不能替代 Steam 自己的最终文件完整性验证。

这批较早的原生 Steam 日志产生于接管前，仅作为发现官方节点的依据。之后已经完成下面的管理员客户端验收；独立样本与实际 Steam 连接证据分别记录。

## 管理员实际客户端下载验收

2026-10-05，最终修复构建 `9DDD27ED…` 以管理员运行，对本机日志发现的 **25 个精确官方缓存域名**启用双栈 hosts，并在 Steam 实际下载中核对连接：

| 证据 | 本次结果 |
| --- | --- |
| 25.452 秒、22 次采样 | Steam 到 `::1:443` 观测 120 次，助手反向四元组匹配 120 次 |
| 助手公网连接采样 | IPv6 观测 120 次，IPv4 0 次；多次快照计数不等于唯一连接数 |
| 官方缓存真实流量 | 六个 `cache7-12-hkg1` 内容流累计增加 250,120,379 字节，助手总下载计数增加 250,124,764 字节；两种口径不相加 |
| Steam 自身日志 | 22:33:42 六个缓存均记录 HTTP/2 经 `[::1]:443` |
| 下载链路验收 | 最终构建 22:30 的 8 项通过，包含已应用 hosts、本地 TLS 和真实内容块 |
| 立即停止恢复 | 22:34:25 耗时 0.0379637 秒，活动转发连接 6 → 0，hosts 与原文件哈希一致 |
| 固定/拉黑 | 最终构建 22:40 复验：固定 IP 生效、745,040 字节真实块哈希一致；拉黑后 CONNECT 502、curl exit 7，目标上游新建连接为 0；原空固定项和黑名单已恢复 |

采样、日志和真实内容流一起确认了**本次 Steam → 回环助手 → 官方缓存 IPv6**路径。脱敏数据见 [最终验收 JSON](acceptance/steam-final-2026-10-05.json)。短连接可能未被采样，未接管域名仍按原网络方式连接，不能扩大为所有 Steam 流量都只用 IPv6。

此前 `D545EFB2…` 候选首次恢复遇到 Windows hosts 文件替换占用，连接空闲关闭后重试成功，原文件哈希一致。[候选历史报告](acceptance/steam-client-2026-10-05.json) 保留首次失败。最终版已修复为先关闭受影响连接、Windows 原子替换最多重试 **3 秒**，并通过上述立即停止复验；仍不能替换时保留恢复记录并提示。

最终 EXE 为 **9,227,776 字节**，SHA256 `9DDD27EDE94DCAE4F8A060CED80D2395C156199D13CC6CFC47DDC50B20C3F439`。洁净源码 **67 个顶层、115 项含子测试通过，Steam 21 个顶层，go vet 通过**；2 项显式跳过分别为公网 Raw 未开启、UE 样本未随公开源码分发。验收后重新启用 Steam 接管供继续使用，原下载队列保持暂停。完整状态见 [验收记录](acceptance/2026-10-05.md)。

## 原软件国区候选的实测状态

为理解原工具的方式，另对其当前规则中的部分国区 CDN 做了匿名只读检查。它们不是当前默认官方缓存服务；以下失败会如实保留，不能因为有 AAAA 就写成下载通过。

| 候选 | 本次结果与含义 |
| --- | --- |
| `dl.steam.clngaa.com` | CNAME 到 `dl.steam.clngaa.com.z.ngaagslb.net`，再到 `akk.gdtstream.com.z1.ngaagslb.net`。两个 IPv6 节点 HTTP 都返回 302，跳到 `akk.gdtstream.com` 同一内容路径；HTTPS 都因不可信证书链失败。入口 IPv6 可达，最终内容仍未通过。 |
| `akk.gdtstream.com` | 当前 CNAME 目标 `akk.gdtstream.com.t.ngaagslb.net` 无 AAAA。研究性借用前述 `dl` 入口的 IPv6，原 Host 返回 HTTP 403，服务端明确指出 Host 未配置；不可盲目共享 IP。 |
| `xz.pphimalayanrt.com` | `xz.pphimalayanrt.com.m.alikunlun.com` 有 IPv6；测试的节点 HTTP 返回 403，HTTPS TLS 握手失败。没有关闭证书校验或尝试绕过授权。 |
| `st.dl.eccdnx.com` | 经 `zqctgdl.v.trpcdn.net` 到 `ztgdl.v.trpcdn.net`，最终目标未查到 AAAA。 |
| `files.steam.nsclouds.cn` | CNAME 链最终到 `bjxlv4.y.ngaagslb.net`，未查到 AAAA。 |
| `steamcdn-a.akamaihd.net` | 经 `edgesuite.net` 到 `a1843.g1.akamai.net`，本次未查到 AAAA；没有套用其他 Akamai 地址。 |

当前可确认的是这次匿名请求的结果。403 可能与 CDN 授权或访问条件相关，无 AAAA 也可能随网络和调度改变；这些结果不证明对应平台永远无法使用 IPv6。更详细的原软件结构说明见 [只读分析](ORIGINAL-SOFTWARE-ANALYSIS.md)。

## 常见问题

| 现象 | 处理 |
| --- | --- |
| 提示需要管理员权限 | 停止/退出助手，右键 EXE 以管理员身份运行，再启用 Steam。 |
| 提示 80/443 已被占用 | 根据提示确认占用程序，处理后重新启用；助手不会擅自结束其他服务。 |
| 刷新后没有发现下载服务器 | 确认本机 Steam 已有一次内容下载记录，检查 Steam 安装路径和日志是否可读取。 |
| 已启用但 Steam 仍用旧连接 | 在 Steam 暂停后继续；仍不匹配时刷新服务器并重新应用。 |
| IPv6 连接超时或所有 IP 均不可用 | 检查本机 IPv6、解除误拉黑或恢复自动；软件不会用 IPv4 装作通过。 |
| 停止/恢复提示 hosts 块被改动 | 核对软件保留的恢复记录和 hosts 备份。助手会保留其他程序或用户的新改动，不覆盖整份文件。 |

回到 [使用手册](USER-GUIDE.md) · [构建说明](DEVELOPMENT.md) · [项目首页](../README.md)
