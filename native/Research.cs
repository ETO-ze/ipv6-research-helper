using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.IO;
using System.Linq;
using System.Net.Http;
using System.Text;
using System.Threading.Tasks;
using System.Windows;
using System.Windows.Controls;

namespace LocalIPv6 {
 public class ResearchSource {public string id,name,category,sample,kind,sha256,note;public string[] hosts; public string Label{get{return name;}}}
 public class DNSAttempt {public string provider,host,code;}
 public class HostHealth {public string host,error,code,resolveHost;public string[] ipv6;public List<DNSAttempt> dns;}
 public class SourceHealth {public string id,status,detail,code,advice,sha256,finalHost;public string @checked;public int bytes,http;public List<HostHealth> hosts;}
 public class ResearchJob {public string id,source,name,host,state,detail,path,sha256,expected,created;public long done,total,resumedFrom;public int http;}
 public class ResearchState {public List<ResearchSource> sources;public List<SourceHealth> health;public List<ResearchJob> jobs;public bool checking;public string downloadRoot,proxy;}
 public class SourceRow {public string ID{get;set;}public string Name{get;set;}public string Category{get;set;}public string Status{get;set;}public string Detail{get;set;}}
 public class JobRow {public string ID{get;set;}public string Name{get;set;}public string Source{get;set;}public string Status{get;set;}public string Progress{get;set;}public string Detail{get;set;}}
 public partial class DesktopApp {
  ResearchState researchState=new ResearchState();bool researchBusy;string sampleSignature="",jobsSignature="",sourcesSignature="";
  void BuildResearch(){
   N<Button>("ResearchNav").Click+=(s,e)=>ShowPage("research");
   N<Button>("DiagnosticsNav").Click+=(s,e)=>ShowPage("diagnostics");
   N<Button>("NewDownloadButton").Click+=async(s,e)=>await ResearchAction(async()=>{await ResearchPost("download",new {url=N<TextBox>("DownloadURL").Text,sha256=N<TextBox>("ExpectedSHA").Text});Log("已建立科研下载任务。");});
   N<Button>("PauseJob").Click+=async(s,e)=>await JobAction("pause");
   N<ListView>("JobsList").SelectionChanged+=(s,e)=>UpdateJobControls();
   N<Button>("ResumeJob").Click+=async(s,e)=>await JobAction("resume");
   N<Button>("OpenDownloads").Click+=(s,e)=>{if(!String.IsNullOrEmpty(researchState.downloadRoot)){Directory.CreateDirectory(researchState.downloadRoot);Process.Start(new ProcessStartInfo(researchState.downloadRoot){UseShellExecute=true});}};
   N<Button>("JobDetails").Click+=(s,e)=>{var row=N<ListView>("JobsList").SelectedItem as JobRow;if(row==null)return;var job=researchState.jobs.FirstOrDefault(x=>x.id==row.ID);if(job!=null)ShowMessage(job.name,job.detail+"\n\n保存位置："+job.path+"\n\nSHA256："+(String.IsNullOrEmpty(job.sha256)?"尚未计算":job.sha256)+"\n预期 SHA256："+(String.IsNullOrEmpty(job.expected)?"未提供，仅计算本地哈希":job.expected));};
   N<Button>("LoadSample").Click+=(s,e)=>{var source=N<ComboBox>("SampleSources").SelectedItem as ResearchSource;if(source!=null){N<TextBox>("DownloadURL").Text=source.sample;N<TextBox>("ExpectedSHA").Text=source.sha256??"";}};
   N<Button>("CheckAllSources").Click+=async(s,e)=>await ResearchAction(async()=>{await ResearchPost("check",new {id=""});Log("正在分批检测全部来源，可继续使用软件。");});
   N<Button>("CheckSource").Click+=async(s,e)=>{var row=N<ListView>("SourcesList").SelectedItem as SourceRow;if(row!=null)await ResearchAction(async()=>{await ResearchPost("check",new {id=row.ID});});};
   N<ListView>("SourcesList").SelectionChanged+=(s,e)=>ShowSourceDetail();
   N<Button>("ValidateSourceFile").Click+=(s,e)=>{var row=N<ListView>("SourcesList").SelectedItem as SourceRow;if(row==null)return;var source=researchState.sources.FirstOrDefault(x=>x.id==row.ID);ShowPage("research");N<TextBox>("DownloadURL").Text=source.sample??"";N<TextBox>("ExpectedSHA").Text=source.sha256??"";Log(String.IsNullOrEmpty(source.sample)?"请填写 "+source.name+" 的有效 HTTPS 文件链接后开始验证。":"已载入 "+source.name+" 样本，可下载并验证完整文件。");};
   N<TextBox>("SourceSearch").TextChanged+=(s,e)=>RenderSources();
   N<Button>("ProxyHelp").Click+=(s,e)=>ShowMessage("科研客户端接入","科研文件可直接粘贴到本软件下载，无需启用 Epic 转发。\n\n支持 HTTP 代理的客户端可使用：http://127.0.0.1:17891\n\n例如单次 pip 操作：\npython -m pip download six --proxy http://127.0.0.1:17891\n\n该代理只允许来源检测中登记的域名，外网上游和 DNS 强制 IPv6。未自动修改 pip / Conda / 浏览器设置。HTTPS 重定向也必须经过该代理才受约束；全系统网络不在保证范围内。\n\n需要登录的来源，请使用已有权限的有效文件链接；此版不导入浏览器登录状态。");
   BuildRouting();ShowPage("routing");
  }
  void ShowPage(string page){currentPage=page;N<ScrollViewer>("RoutingScroll").Visibility=page=="routing"?Visibility.Visible:Visibility.Collapsed;N<Button>("RoutingNav").SetResourceReference(Button.BorderBrushProperty,page=="routing"?"Accent":"Line");N<ScrollViewer>("MainScroll").Visibility=page=="epic"?Visibility.Visible:Visibility.Collapsed;N<ScrollViewer>("ResearchScroll").Visibility=page=="research"?Visibility.Visible:Visibility.Collapsed;N<ScrollViewer>("DiagnosticsScroll").Visibility=page=="diagnostics"?Visibility.Visible:Visibility.Collapsed;N<Button>("ResearchNav").SetResourceReference(Button.BorderBrushProperty,page=="research"?"Accent":"Line");N<Button>("DiagnosticsNav").SetResourceReference(Button.BorderBrushProperty,page=="diagnostics"?"Accent":"Line");if(routePlan!=null)UpdateRoutingView();}
  async Task<string> ResearchPost(string action,object body){var request=new HttpRequestMessage(HttpMethod.Post,"http://127.0.0.1:17890/api/research/"+action);request.Headers.Add("X-Token",token);request.Content=new StringContent(json.Serialize(body),Encoding.UTF8,"application/json");using(var response=await http.SendAsync(request)){var text=await response.Content.ReadAsStringAsync();if(!response.IsSuccessStatusCode)throw new Exception(text.Trim());return text;}}
  async Task ResearchAction(Func<Task> action){if(!ready||researchBusy)return;researchBusy=true;try{await action();await PollResearch();}catch(Exception e){ShowMessage("操作未完成",e.Message);}finally{researchBusy=false;}}
  async Task JobAction(string action){var row=N<ListView>("JobsList").SelectedItem as JobRow;if(row!=null)await ResearchAction(async()=>{await ResearchPost(action,new{id=row.ID});});}
  async Task PollResearch(){
   researchState=json.Deserialize<ResearchState>(await http.GetStringAsync("http://127.0.0.1:17890/api/research"));
   var jobs=researchState.jobs??new List<ResearchJob>();var selectedRow=N<ListView>("JobsList").SelectedItem as JobRow;string id=selectedRow==null?"":selectedRow.ID;
   var rows=jobs.OrderByDescending(j=>j.created).ThenBy(j=>j.id).Select(j=>new JobRow{ID=j.id,Name=j.name,Source=j.source,Status=j.state,Progress=Bytes(j.done)+(j.total>0?" / "+Bytes(j.total):" / 未知大小"),Detail=j.detail}).ToList();string jobSignature=String.Join("|",rows.Select(r=>r.ID+r.Status+r.Progress+r.Detail));if(jobSignature!=jobsSignature){jobsSignature=jobSignature;N<ListView>("JobsList").ItemsSource=rows;N<ListView>("JobsList").SelectedItem=rows.FirstOrDefault(r=>r.ID==id);}
   int passed=(researchState.health??new List<SourceHealth>()).Count(h=>h.status=="样本通过");
   N<TextBlock>("ResearchSummary").Text=passed+" 个来源样本通过 · "+jobs.Count(j=>j.state=="下载中"||j.state=="排队中")+" 个进行中 · "+jobs.Count(j=>j.state=="已完成")+" 个完成";
   N<TextBlock>("DownloadLocation").Text="保存到："+researchState.downloadRoot;
   var sampleIDs=(researchState.health??new List<SourceHealth>()).Where(h=>h.status=="样本通过").Select(h=>h.id).ToArray();var samples=(researchState.sources??new List<ResearchSource>()).Where(s=>sampleIDs.Contains(s.id)&&!String.IsNullOrEmpty(s.sample)).ToList();string signature=String.Join(",",samples.Select(s=>s.id));
   if(signature!=sampleSignature){sampleSignature=signature;N<ComboBox>("SampleSources").ItemsSource=samples;N<ComboBox>("SampleSources").SelectedIndex=samples.FindIndex(s=>s.id=="pypi");if(N<ComboBox>("SampleSources").SelectedIndex<0&&samples.Count>0)N<ComboBox>("SampleSources").SelectedIndex=0;}
   N<Button>("LoadSample").IsEnabled=samples.Count>0;N<Button>("CheckAllSources").IsEnabled=!researchState.checking;N<Button>("CheckSource").IsEnabled=!researchState.checking && N<ListView>("SourcesList").SelectedItem!=null;N<Button>("NewDownloadButton").IsEnabled=ready;
   N<TextBlock>("DiagnosticSummary").Text=(researchState.checking?"检测进行中，结果会自动更新":"检测结果来自本机实际网络")+" · "+passed+" 个样本通过 / "+(researchState.sources??new List<ResearchSource>()).Count+" 个来源登记";
   RenderSources();UpdateJobControls();
  }
  void UpdateJobControls(){var row=N<ListView>("JobsList").SelectedItem as JobRow;N<Button>("PauseJob").IsEnabled=row!=null&&(row.Status=="下载中"||row.Status=="排队中"||row.Status=="校验中");N<Button>("ResumeJob").IsEnabled=row!=null&&(row.Status=="已暂停"||row.Status=="失败");N<Button>("JobDetails").IsEnabled=row!=null;}
  void RenderSources(){if(researchState.sources==null)return;var old=N<ListView>("SourcesList").SelectedItem as SourceRow;string id=old==null?"":old.ID;string query=N<TextBox>("SourceSearch").Text.Trim();var rows=new List<SourceRow>();foreach(var s in researchState.sources){var h=(researchState.health??new List<SourceHealth>()).FirstOrDefault(x=>x.id==s.id);var row=new SourceRow{ID=s.id,Name=s.name,Category=s.category,Status=h==null?"未检测":h.status,Detail=h==null?s.note:h.detail};if(query.Length==0||(row.Name+row.Category+row.Status).IndexOf(query,StringComparison.OrdinalIgnoreCase)>=0)rows.Add(row);}string signature=String.Join("|",rows.Select(r=>r.ID+r.Status+r.Detail));if(signature!=sourcesSignature){sourcesSignature=signature;N<ListView>("SourcesList").ItemsSource=rows;N<ListView>("SourcesList").SelectedItem=rows.FirstOrDefault(x=>x.ID==id);}}
  string DNSLabel(string code){switch(code){case "dns_cdn_ipv6":return "已实测 CDN IPv6 入口";case "dns_ipv6":return "查到 IPv6";case "dns_cname":return "跟随官方 DNS 别名";case "dns_no_aaaa":return "无 AAAA 记录";case "dns_nxdomain":return "域名不存在";case "dns_timeout":return "查询超时";case "dns_error":return "查询失败";case "dns_cname_loop":return "别名链异常";default:return code;}}
  void ShowSourceDetail(){var row=N<ListView>("SourcesList").SelectedItem as SourceRow;N<Button>("CheckSource").IsEnabled=!researchState.checking&&row!=null;N<Button>("ValidateSourceFile").IsEnabled=row!=null;if(row==null){N<TextBlock>("SourceDetail").Text="选择一项，查看检测原因、处理建议和逐域名结果。";return;}var h=(researchState.health??new List<SourceHealth>()).FirstOrDefault(x=>x.id==row.ID);var source=researchState.sources.FirstOrDefault(s=>s.id==row.ID);var text=new StringBuilder(row.Detail);if(h!=null){text.Append("\n\n处理建议："+(h.advice??"旧版检测结果，请点击“重新检测”更新诊断。"));if(!String.IsNullOrEmpty(h.@checked))text.Append("\n检测时间："+h.@checked);if(!String.IsNullOrEmpty(h.finalHost))text.Append("\n实际文件域名："+h.finalHost);foreach(var host in h.hosts??new List<HostHealth>()){text.Append("\n\n"+host.host+" · "+(String.IsNullOrEmpty(host.code)?host.error:DNSLabel(host.code)));if(!String.IsNullOrEmpty(host.resolveHost)&&host.resolveHost!=host.host)text.Append("\n解析目标："+host.resolveHost+"（保持原请求域名与证书校验）");if(host.ipv6!=null&&host.ipv6.Length>0)text.Append("\n"+String.Join("  /  ",host.ipv6));foreach(var dns in host.dns??new List<DNSAttempt>())text.Append("\n  "+dns.provider+" → "+DNSLabel(dns.code)+(dns.host==host.host?"":"（"+dns.host+"）"));}}else{text.Append("\n域名："+String.Join(" / ",source.hosts));}N<TextBlock>("SourceDetail").Text=text.ToString();}
 }
}

