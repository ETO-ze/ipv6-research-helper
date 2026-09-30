# 样本与上游依据

- arXiv 原请求：https://arxiv.org/pdf/1706.03762
- Fastly 双栈说明：https://www.fastly.com/documentation/guides/full-site-delivery/domains-and-origins/enabling-dualstack-connections/
- Fastly IPv6 入口为 dualstack.s.sni.global.fastly.net；实测 arxiv.org、export.arxiv.org 的原域名 TLS 和 PDF 均成功。此为本地已验证的 CDN 映射，不宣称 arXiv 官方为这些域名发布了 AAAA。
- NOAA 官方公开桶及区域：https://registry.opendata.aws/noaa-goes/
- NOAA 官方指南：https://noaa-goes16.s3.amazonaws.com/Beginners_Guide_to_GOES-R_Series_Data.pdf
- S3 双栈说明：https://docs.aws.amazon.com/AmazonS3/latest/developerguide/dual-stack-endpoints.html
- NOAA NCEI 补充探测：https://www.ncei.noaa.gov/sites/default/files/2020-09/WOA%201994%20Data%20Download%20Instructions.pdf （HTTP 206；不是所有 NOAA 产品通过）
- bioRxiv PDF：https://www.biorxiv.org/content/10.1101/2024.01.18.576291v1.full.pdf
- medRxiv PDF：https://www.medrxiv.org/content/10.1101/2020.12.18.20248511v1.full.pdf （实测 403）
- Figshare 官方 API 教程及文件：https://info.figshare.com/user-guide/how-to-use-the-figshare-api/ ；https://figshare.com/ndownloader/files/9778696 （实测 403）
- ModelScope 下载 API 官方实现：https://github.com/modelscope/modelscope/blob/master/modelscope/hub/file_download.py （模型配置样本不能证明权重节点通过）
- IEEE 公开作者稿：https://ieeexplore.ieee.org/ielaam/6287639/8948470/9165719-aam.pdf
- Wiley 公开论文目录：https://onlinelibrary.wiley.com/journal/14679299/ ；样本：https://onlinelibrary.wiley.com/doi/pdf/10.1111/padm.12397 （实测 403）

不添加未知重定向域名，不更改签名 URL，不关闭证书验证，不使用 IPv4 回退。需要账户/授权的来源不会自动导入浏览器登录信息。
