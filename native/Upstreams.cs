using System;
using System.Collections.Generic;
using System.Linq;
using System.Net.Http;
using System.Text;
using System.Threading.Tasks;
using System.Windows;
using System.Windows.Controls;

namespace LocalIPv6 {
 public class NodePolicy {public string pinned;public string[] blocked;}
 public class NodeData {public string ip,last;public bool dns,blocked,pinned;public int active,connections;public long down;}
 public class NodeView {public string host,resolveHost,expires,error;public NodePolicy policy;public List<NodeData> rows;}
 public class NodeRow {public string IP{get;set;}public string State{get;set;}public string Origin{get;set;}public string Traffic{get;set;}public string Active{get;set;}public bool DNS,Blocked,Pinned;}
 public partial class DesktopApp {
  bool nodeBusy;string nodeSignature="",nodeError="";string lastNodeHost="";
  string NodeHost{get{return N<ComboBox>("NodeHost").SelectedItem as string??"";}}
  void BuildUpstreams(){
   N<ComboBox>("NodeHost").SelectionChanged+=async(s,e)=>{nodeSignature="";nodeError="";N<TextBlock>("NodeRouteNote").Text=new[]{"download.epicgames.com","download2.epicgames.com","download3.epicgames.com","download4.epicgames.com","epicgames-download1-1251447533.file.myqcloud.com"}.Contains(NodeHost)?"此地址的 HTTP 下载会转到 epicgames-download1.akamaized.net；请切换到该实际域名管理 HTTP 上游。当前列表管理本域名的直接连接。":"";await PollUpstreams();};
   N<ListView>("NodeList").SelectionChanged+=(s,e)=>UpdateNodeControls();
   N<Button>("RefreshNodes").Click+=async(s,e)=>await NodeAction("refresh");
   N<Button>("PinNode").Click+=async(s,e)=>await NodeAction("pin");
   N<Button>("AutoNodes").Click+=async(s,e)=>await NodeAction("auto");
   N<Button>("BlockNode").Click+=async(s,e)=>await NodeAction("block");
   N<Button>("UnblockNode").Click+=async(s,e)=>await NodeAction("unblock");
   N<Button>("CopyNode").Click+=(s,e)=>{var row=N<ListView>("NodeList").SelectedItem as NodeRow;if(row!=null){Clipboard.SetText(row.IP);Log("已复制上游 IPv6。");}};
  }
  void SetNodeHosts(string[] hosts){var combo=N<ComboBox>("NodeHost");var previous=NodeHost;var existing=combo.ItemsSource as string[];if(existing!=null&&existing.SequenceEqual(hosts))return;combo.ItemsSource=hosts;combo.SelectedItem=hosts.Contains(previous)?previous:hosts.FirstOrDefault();}
  async Task PollUpstreams(){if(nodeBusy||String.IsNullOrEmpty(NodeHost)||!ready)return;string host=NodeHost;try{var view=json.Deserialize<NodeView>(await http.GetStringAsync("http://127.0.0.1:17890/api/upstreams?host="+Uri.EscapeDataString(host)));if(host==NodeHost&&!nodeBusy)RenderNodes(view);}catch(Exception e){if(host==NodeHost)N<TextBlock>("NodeStatus").Text="读取失败："+e.Message;}}
  void RenderNodes(NodeView view){
   if(view.host!=NodeHost)return;
   if(lastNodeHost!=view.host){lastNodeHost=view.host;nodeSignature="";}
   var selected=N<ListView>("NodeList").SelectedItem as NodeRow;string ip=selected==null?"":selected.IP;
   var rows=(view.rows??new List<NodeData>()).Select(n=>new NodeRow{IP=n.ip,State=n.blocked?"已拉黑":n.pinned?(n.dns?"固定使用":"固定已失效"):"自动候选",Origin=n.dns?"当前 DNS":n.connections>0?"连接记录":"保存的策略",Traffic=Bytes(n.down),Active=n.active.ToString(),DNS=n.dns,Blocked=n.blocked,Pinned=n.pinned}).ToList();
   string signature=view.host+String.Join("|",rows.Select(n=>n.IP+n.State+n.Origin+n.Traffic+n.Active));if(signature!=nodeSignature){nodeSignature=signature;N<ListView>("NodeList").ItemsSource=rows;N<ListView>("NodeList").SelectedItem=rows.FirstOrDefault(n=>n.IP==ip);}
   string policy=view.policy!=null&&!String.IsNullOrEmpty(view.policy.pinned)?"固定 "+view.policy.pinned:"自动选择";
   N<TextBlock>("NodeStatus").Text=policy+" · "+rows.Count(n=>n.DNS&&!n.Blocked)+" 个未拉黑 DNS 候选 · "+rows.Count(n=>n.Blocked)+" 个拉黑"+(view.resolveHost!=view.host?"\n解析目标："+view.resolveHost:"")+(rows.Count==0?"\n尚无地址，请点击刷新 DNS。":"")+(String.IsNullOrEmpty(nodeError)?"":"\n解析失败："+nodeError);
   UpdateNodeControls();
  }
  void UpdateNodeControls(){var row=N<ListView>("NodeList").SelectedItem as NodeRow;bool enabled=ready&&!busy&&!nodeBusy&&!String.IsNullOrEmpty(NodeHost);N<ComboBox>("NodeHost").IsEnabled=!nodeBusy&&!busy;N<Button>("RefreshNodes").IsEnabled=enabled;N<Button>("AutoNodes").IsEnabled=enabled;N<Button>("PinNode").IsEnabled=enabled&&row!=null&&row.DNS&&!row.Blocked&&!row.Pinned;N<Button>("BlockNode").IsEnabled=enabled&&row!=null&&!row.Blocked;N<Button>("UnblockNode").IsEnabled=enabled&&row!=null&&row.Blocked;N<Button>("CopyNode").IsEnabled=row!=null;}
  async Task NodeAction(string action){if(busy||nodeBusy||String.IsNullOrEmpty(NodeHost))return;var row=N<ListView>("NodeList").SelectedItem as NodeRow;string host=NodeHost;nodeBusy=true;busy=true;UpdateNodeControls();UpdateRoutingView();UpdateControls();N<TextBlock>("NodeStatus").Text=action=="refresh"?"正在通过 IPv6 查询 DNS…":"正在应用上游策略…";try{var req=new HttpRequestMessage(HttpMethod.Post,"http://127.0.0.1:17890/api/upstreams/"+action);req.Headers.Add("X-Token",token);req.Content=new StringContent(json.Serialize(new{host=host,ip=row==null?"":row.IP}),Encoding.UTF8,"application/json");using(var response=await http.SendAsync(req)){string body=await response.Content.ReadAsStringAsync();if(!response.IsSuccessStatusCode)throw new Exception(body.Trim());var view=json.Deserialize<NodeView>(body);nodeError=view.error;RenderNodes(view);Log(action=="refresh"?(String.IsNullOrEmpty(view.error)?"已刷新真实 IPv6 候选。":"DNS 检测未通过，未回退 IPv4。"):"上游策略已保存，仅对 "+host+" 生效；受影响的连接将重连。");}}catch(Exception e){ShowMessage("上游操作未完成",e.Message);}finally{nodeBusy=false;busy=false;UpdateNodeControls();UpdateRoutingView();UpdateControls();}await PollUpstreams();}
 }
}
