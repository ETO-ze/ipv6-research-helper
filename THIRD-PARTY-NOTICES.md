# 第三方内容说明

项目作者为 ETO-ze。以下内容不属于作者原创，也不因本仓库公开而改变原权利归属。

## 软件依赖

- Go 标准库和运行时：Go Authors，BSD 风格许可，见 third-party/Go-LICENSE.txt。
- gopkg.in/yaml.v3 v3.0.1：Canonical Ltd / Kirill Simonov 等，按文件适用 Apache 2.0 或 MIT，见 third-party/yaml-v3-LICENSE.txt 和 third-party/Apache-2.0.txt。
- Microsoft .NET Framework / WPF：由系统环境提供，源码包不重新分发该运行时。

## 平台名称和图标

native/icons 中的网站图标和 Epic 客户端图标用于服务辨识，属于各平台权利人；出处见该目录 provenance.json 和 provenance-0.6.json。0.8.0 的 `native/icons/steam.img` 取自 [Steam 官方商店 favicon](https://store.steampowered.com/favicon.ico)，用于 Steam 服务识别，权利属于 Valve。图片扩展名 .img 用作内嵌资源，内容为原始图片数据。没有可靠图标的来源显示名称缩写。

这些资源不适用本项目统一许可，不表示平台认可、合作或支持。本项目不包含参考软件 UsbEAm 的可执行文件、完整第三方前端源码、规则素材或参考截图。原工具的行为研究采用独立分析文字，引用作者公开说明与规则链接，见 [原软件只读分析](docs/ORIGINAL-SOFTWARE-ANALYSIS.md)。

## 下载样本

论文、数据、包文件、UE 和 Steam 数据块用于网络与完整性测试，归各自权利人所有。仓库保存公共链接、验证逻辑和必要摘要，不分发下载的论文全文、UE/Steam 数据块或用户文件。出处见 sample-provenance.md、research-sources.json 和 [Steam 样本说明](docs/STEAM.md)。

Steam 样本 SHA256 是六个官方缓存独立 IPv6 下载建立的传输参照，并非 Valve 发布者校验值或数字签名；本次未读取私有 depot 密钥、未解密原始游戏文件。平台资源可读取或哈希一致，不表示这些资源属于本项目原创。

## 项目代码

Copyright (c) 2026 ETO-ze. 当前源码公开供查看，未指定统一开源许可证；第三方依赖仍适用各自许可。需额外使用授权时请联系作者。
