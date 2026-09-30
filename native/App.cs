using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.IO;
using System.Linq;
using System.Net.Http;
using System.Reflection;
using System.Runtime.InteropServices;
using System.Security.Cryptography;
using System.Text;
using System.Text.RegularExpressions;
using System.Threading;
using System.Threading.Tasks;
using System.Web.Script.Serialization;
using System.Windows;
using System.Windows.Controls;
using System.Windows.Input;
using System.Windows.Markup;
using System.Windows.Media;
using System.Windows.Threading;

namespace LocalIPv6 {
 public class Entry {public string host,remote,error,kind,time;public long down;public bool active;}
 public class State {public string application,version,startupError;public int pid;public long down,ipv6Connections,blocked;public bool clashActive,hostsActive;public List<Entry> entries;}
 public class Check {public string name,detail;public bool passed;}
 public class Verification {public bool passed;public string report,scope;public List<Check> checks;}
 public class Probe {public string url,error,sha256;public long bytes;public double seconds;public int status;public bool passed;}
 public class Row {public string Status{get;set;}public string Host{get;set;}public string Address{get;set;}public string Received{get;set;}}
 public class Platform {public string Name,Mark,Description,Color;public bool Working;public Platform(string n,string m,string d,string c,bool w=false){Name=n;Mark=m;Description=d;Color=c;Working=w;}}
 public partial class DesktopApp {
  const string Title="IPv6 下载助手 · 科研与游戏";
  Window window; HttpClient http; JavaScriptSerializer json=new JavaScriptSerializer(); State state=new State(); DispatcherTimer timer;
  string token="",selectedName="",enginePath="";Platform selected;bool busy,polling,ready,exiting,dark,expanded=true;long previousBytes;DateTime previousTime=DateTime.UtcNow;
  Dictionary<string,Button> cards=new Dictionary<string,Button>(); TextBlock runtimeDown,runtimeRate,runtimeCount,runtimeFamily; ListBox runtimeLog;
  readonly List<string> history=new List<string>();
  static Mutex instance;
  [DllImport("user32.dll",CharSet=CharSet.Unicode)] static extern IntPtr FindWindow(string cls,string title);
  [DllImport("user32.dll")] static extern bool ShowWindow(IntPtr h,int n);
  [DllImport("user32.dll")] static extern bool SetForegroundWindow(IntPtr h);
  [STAThread] public static void Main(){
   bool first;instance=new Mutex(true,"Local\\EpicIPv6Helper-NativeUI",out first);
   if(!first){IntPtr h=FindWindow(null,Title);if(h!=IntPtr.Zero){ShowWindow(h,9);SetForegroundWindow(h);}return;}
   var app=new Application();app.ShutdownMode=ShutdownMode.OnMainWindowClose;
   app.DispatcherUnhandledException+=(s,e)=>{MessageBox.Show("操作未完成："+e.Exception.Message,Title,MessageBoxButton.OK,MessageBoxImage.Warning);e.Handled=true;};
   try{var desktop=new DesktopApp();desktop.Build();app.Run(desktop.window);}catch(Exception e){MessageBox.Show(e.ToString(),Title,MessageBoxButton.OK,MessageBoxImage.Error);}finally{instance.ReleaseMutex();instance.Dispose();}
  }
  T N<T>(string n)where T:class{return window.FindName(n) as T;}
  Brush Brush(string key){return (Brush)window.Resources[key];}
  static Brush Hex(string color){return new SolidColorBrush((Color)ColorConverter.ConvertFromString(color));}
  static string Bytes(long n){double v=n;string[] u={"B","KiB","MiB","GiB"};int i=0;while(v>=1024&&i<3){v/=1024;i++;}return v.ToString(i==0?"0":"0.00")+" "+u[i];}
  TextBlock Text(string text,double size=13,bool bold=false){var block=new TextBlock{Text=text,FontSize=size,FontWeight=bold?FontWeights.SemiBold:FontWeights.Normal,TextWrapping=TextWrapping.Wrap,Margin=new Thickness(0,0,0,8),LineHeight=size*1.65};block.SetResourceReference(TextBlock.ForegroundProperty,"Ink");return block;}
  void Build(){
   using(var stream=Assembly.GetExecutingAssembly().GetManifestResourceStream("MainWindow.xaml"))using(var reader=new StreamReader(stream)){window=(Window)XamlReader.Parse(reader.ReadToEnd());}
   window.Title=Title;
   http=new HttpClient(new HttpClientHandler{UseProxy=false});http.Timeout=TimeSpan.FromSeconds(115);
   json.MaxJsonLength=4*1024*1024;
   N<Border>("TitleBar").MouseLeftButtonDown+=(s,e)=>{if(e.ClickCount==2){window.WindowState=window.WindowState==WindowState.Maximized?WindowState.Normal:WindowState.Maximized;}else if(e.OriginalSource is TextBlock||e.OriginalSource is Border){try{window.DragMove();}catch{}}};
   N<Button>("MinButton").Click+=(s,e)=>window.WindowState=WindowState.Minimized;
   N<Button>("CloseButton").Click+=(s,e)=>window.Close();
   window.Closing+=async(s,e)=>{if(exiting)return;e.Cancel=true;if(busy){Log("请等待当前检测完成后再退出。");return;}busy=true;N<TextBlock>("FooterStatus").Text="正在恢复网络配置并退出…";try{if(ready)await Post("shutdown");exiting=true;timer.Stop();window.Close();}catch(Exception ex){busy=false;ShowMessage("恢复未完成",ex.Message+"\n已保留窗口和备份，避免退出后下载规则指向停止的服务。");}};
   N<Button>("ThemeButton").Click+=(s,e)=>SetTheme();
   N<Button>("StatusButton").Click+=(s,e)=>ShowRuntime();
   N<Button>("RestoreButton").Click+=async(s,e)=>await Restore();
   N<Button>("PlayButton").Click+=async(s,e)=>await ToggleService();
   N<Button>("ExpandPlatforms").Click+=(s,e)=>Expand(!expanded);
   N<Button>("DismissDialog").Click+=(s,e)=>{if(busy){Log("检测在后台进行，可关闭此面板。");}N<Grid>("Overlay").Visibility=Visibility.Collapsed;runtimeLog=null;};
   N<Button>("VerifyButton").Click+=async(s,e)=>await Verify();
N<Button>("RefreshButton").Click+=async(s,e)=>await Poll();
   N<Button>("OptimizeButton").Click+=(s,e)=>ShowOptimizer();
   N<Button>("AutoNode").Click+=(s,e)=>Log("采用平台自动节点；助手只建立公网 IPv6 连接。");
   N<Button>("AkamaiNode").Click+=(s,e)=>ShowMessage("Akamai 下载节点","已接入 Epic 官方 Akamai IPv6 路径。腾讯云 HTTP 下载会映射到此节点；保留原始文件路径。当前由平台请求决定 CDN，不强制改写 HTTPS 域名。可在 CDN 优选中测试。");
   N<Button>("CloudfrontNode").Click+=(s,e)=>ShowMessage("CloudFront 下载节点","已接入 Epic CloudFront HTTPS 隧道，保留 SNI 与原始加密数据。当前由平台请求决定 CDN，可在 CDN 优选中测试连接。");
   N<Button>("IPv6Option").Click+=(s,e)=>Log("当前网络策略：仅 IPv6，禁止 IPv4 回退。");
   var platforms=new[]{new Platform("Epic Games","EPIC","UE 引擎 / Epic 游戏下载","#202124",true)};
   foreach(var p in platforms)AddPlatform(p,"PlatformGrid");
   timer=new DispatcherTimer{Interval=TimeSpan.FromSeconds(1)};timer.Tick+=async(s,e)=>await Poll();
   Select(platforms.First(p=>p.Working));BuildResearch();window.Loaded+=async(s,e)=>{await StartEngine();timer.Start();};
  }
  void AddPlatform(Platform p,string gridName){
   var content=new Grid{HorizontalAlignment=HorizontalAlignment.Stretch};content.ColumnDefinitions.Add(new ColumnDefinition{Width=new GridLength(37)});content.ColumnDefinitions.Add(new ColumnDefinition());
   var badge=new Border{Width=32,Height=32,CornerRadius=new CornerRadius(11),Background=Hex(p.Color),HorizontalAlignment=HorizontalAlignment.Left,VerticalAlignment=VerticalAlignment.Center};badge.Child=new TextBlock{Text=p.Mark,FontSize=p.Mark.Length>2?9:15,FontWeight=FontWeights.Bold,Foreground=System.Windows.Media.Brushes.White,VerticalAlignment=VerticalAlignment.Center,HorizontalAlignment=HorizontalAlignment.Center};badge.Child=PlatformIcon("epic",p.Mark,26);content.Children.Add(badge);
   var labels=new StackPanel{VerticalAlignment=VerticalAlignment.Center,Margin=new Thickness(9,0,0,0)};var name=Text(p.Name,12,true);name.Margin=new Thickness(0,0,0,4);name.TextWrapping=TextWrapping.NoWrap;name.TextTrimming=TextTrimming.CharacterEllipsis;labels.Children.Add(name);var description=Text("IPv6 已接入",10);description.SetResourceReference(TextBlock.ForegroundProperty,"Muted");description.Margin=new Thickness(0);labels.Children.Add(description);Grid.SetColumn(labels,1);content.Children.Add(labels);
   var button=new Button{Content=content,Margin=new Thickness(1,2,4,4),Padding=new Thickness(10,9,8,9),Height=65,HorizontalContentAlignment=HorizontalAlignment.Stretch,ToolTip="已实现并实测：Epic IPv6 下载"};button.Click+=(s,e)=>Select(p);cards[p.Name]=button;N<System.Windows.Controls.Primitives.UniformGrid>(gridName).Children.Add(button);
  }
  void Select(Platform p){ShowPage("epic");selected=p;selectedName=p.Name;N<TextBlock>("HeroTitle").Text=p.Name;N<TextBlock>("HeroIcon").Text=p.Mark;N<TextBlock>("HeroIcon").FontSize=p.Mark.Length>2?17:26;N<TextBlock>("HeroSub").Text="UE 引擎 / Epic 游戏下载";N<TextBlock>("SelectionSummary").Text=p.Name+"  ·  已实现";N<TextBlock>("Capability").Text="Epic IPv6 · 已接入后端";N<StackPanel>("ConfigurationPanel").Visibility=Visibility.Visible;foreach(var kv in cards){kv.Value.BorderBrush=kv.Key==p.Name?Hex("#8BA0E6"):Brush("Line");kv.Value.Background=kv.Key==p.Name?Brush("Soft"):Brush("Panel");}Expand(false);Log("已选择 Epic Games。可启动服务、查看连接或运行功能验收。");UpdateControls();}
  void Expand(bool show){expanded=show;N<ScrollViewer>("PlatformsScroll").Visibility=Visibility.Visible;N<Button>("ExpandPlatforms").Content=show?"收起 ⌃":"更换平台 ⌄";}
  async Task StartEngine(){try{
   // The only executable resource is our own compiled backend; no browser host is used.
   string dir=Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData),"EpicIPv6Helper","native-0.7.0");Directory.CreateDirectory(dir);enginePath=Path.Combine(dir,"EpicIPv6Engine.exe");
   byte[] embedded;using(var stream=Assembly.GetExecutingAssembly().GetManifestResourceStream("engine.exe"))using(var ms=new MemoryStream()){stream.CopyTo(ms);embedded=ms.ToArray();}
   bool same=false;if(File.Exists(enginePath)){using(var sha=SHA256.Create()){same=sha.ComputeHash(File.ReadAllBytes(enginePath)).SequenceEqual(sha.ComputeHash(embedded));}}
   if(!same){File.WriteAllBytes(enginePath,embedded);}
   bool existing=false;try{var s=json.Deserialize<State>(await http.GetStringAsync("http://127.0.0.1:17890/api/state"));existing=s.application=="EpicIPv6Helper";if(existing&&s.version!="0.7.0")throw new InvalidOperationException("旧版后端正在运行，请先恢复并退出旧版助手。");}catch(InvalidOperationException){throw;}catch{}
   if(!existing){Process.Start(new ProcessStartInfo(enginePath,"--no-open --manual"){UseShellExecute=false,CreateNoWindow=true,WindowStyle=ProcessWindowStyle.Hidden});}
   for(int i=0;i<45;i++){try{var html=await http.GetStringAsync("http://127.0.0.1:17890/");token=Regex.Match(html,"const token='([a-f0-9]+)'").Groups[1].Value;if(token.Length>0){ready=true;break;}}catch{}await Task.Delay(200);}
   if(!ready)throw new Exception("本地服务未就绪，请检查 17890、17891 和本地 80/443 端口。");Log("本地服务就绪。请选择平台；尚未自动修改网络配置。");await Poll();
  }catch(Exception e){Log("启动失败："+e.Message);ShowMessage("启动未完成",e.Message);}}
  async Task<string> Post(string action){var request=new HttpRequestMessage(HttpMethod.Post,"http://127.0.0.1:17890/api/"+action);request.Headers.Add("X-Token",token);var response=await http.SendAsync(request);var text=await response.Content.ReadAsStringAsync();if(!response.IsSuccessStatusCode)throw new Exception(text.Trim());return text;}
  async Task Poll(){if(!ready||polling||exiting)return;polling=true;try{state=json.Deserialize<State>(await http.GetStringAsync("http://127.0.0.1:17890/api/state"));double elapsed=(DateTime.UtcNow-previousTime).TotalSeconds;double rate=elapsed>0?Math.Max(0,(state.down-previousBytes)/elapsed):0;previousBytes=state.down;previousTime=DateTime.UtcNow;
   N<TextBlock>("IPv6Badge").Text=state.ipv6Connections>0?"IPv6  ·  已连接":"IPv6  ·  待检测";bool active=state.clashActive||state.hostsActive;N<TextBlock>("FlowState").Text=active?(rate>0?Bytes((long)rate)+"/s":"IPv6 转发就绪"):"等待连接";N<Button>("StatusButton").Content=active&&rate>0?"⌁  "+Bytes((long)rate)+"/s":"⌁  运行状态";
   N<TextBlock>("ConnectionNote").Text=(active?"正在转发":"尚未启动")+"  ·  累计 "+Bytes(state.down)+"  ·  "+state.ipv6Connections+" 条 IPv6 上游  ·  "+state.blocked+" 次失败或阻止";
   N<ListView>("Connections").ItemsSource=(state.entries??new List<Entry>()).AsEnumerable().Reverse().Take(80).Select(x=>new Row{Status=!String.IsNullOrEmpty(x.error)?(x.error.Contains("blocked")?"已阻止":"失败"):x.active?"连接中":"已结束",Host=x.host,Address=String.IsNullOrEmpty(x.remote)?"—":x.remote,Received=Bytes(x.down)}).ToList();
   if(runtimeDown!=null){runtimeDown.Text=Bytes(state.down);runtimeRate.Text=Bytes((long)rate)+"/s";runtimeCount.Text=state.ipv6Connections.ToString();runtimeFamily.Text=active?"IPv6 ONLY":"待机";}
   await PollResearch();await PollRouting();UpdateControls();
  }catch(Exception e){Log("本地服务连接中断："+e.Message);}finally{polling=false;}}
  void UpdateControls(){N<Button>("AkamaiNode").IsEnabled=selected!=null&&selected.Working;N<Button>("CloudfrontNode").IsEnabled=selected!=null&&selected.Working;bool active=(state.clashActive&&(routeState==null||routeState.routing.sourceID=="epic"))||state.hostsActive;N<Button>("PlayButton").Content=busy?"…":active?"□":"▷";N<Button>("PlayButton").IsEnabled=ready&&!busy&&(active||(selected!=null&&selected.Working));N<Button>("VerifyButton").IsEnabled=ready&&!busy&&selected!=null&&selected.Working;N<Button>("OptimizeButton").IsEnabled=ready&&!busy&&selected!=null&&selected.Working;N<Button>("RestoreButton").IsEnabled=ready&&!busy;}
  async Task ToggleService(){if(busy)return;if((state.clashActive&&(routeState==null||routeState.routing.sourceID=="epic"))||state.hostsActive){await Restore();return;}if(selected==null||!selected.Working){ShowMessage("选择平台","请先选择已支持的下载来源。");return;}busy=true;UpdateControls();Log("正在启用 Epic IPv6 下载转发…");try{await ApplyRoute("epic","");Log("已启用：Epic → 本机 Clash → 助手 → 官方 CDN IPv6。");}catch(Exception e){ShowMessage("未能接入 Clash",e.Message+"\n当前版本支持本机 Clash for Windows 的规则模式。未切换为 IPv4。");Log("启动未完成。");}finally{busy=false;UpdateControls();}await Poll();}
  async Task Restore(){if(busy)return;busy=true;UpdateControls();try{await Post("restore-clash");await Post("stop");Log("已恢复本程序管理的网络配置，下载转发已停止。");}catch(Exception e){ShowMessage("恢复未完成",e.Message);Log("恢复未完成；已保留备份。");}finally{busy=false;UpdateControls();}await Poll();}
  void Log(string text){N<TextBlock>("FooterStatus").Text=text;history.Insert(0,DateTime.Now.ToString("HH:mm:ss")+"  "+text);if(history.Count>100)history.RemoveAt(history.Count-1);if(runtimeLog!=null)runtimeLog.ItemsSource=history.ToArray();}
  StackPanel Dialog(string heading,string note=null){var root=N<StackPanel>("DialogContent");root.Children.Clear();root.Children.Add(Text(heading,22,true));if(note!=null){var n=Text(note,12);n.SetResourceReference(TextBlock.ForegroundProperty,"Muted");root.Children.Add(n);}N<Grid>("Overlay").Visibility=Visibility.Visible;runtimeLog=null;return root;}
  void ShowMessage(string heading,string text){Dialog(heading).Children.Add(Text(text));}
  void ShowRuntime(){var root=Dialog("实时速度与最近动态","本地服务与桌面界面在本机运行，不打开浏览器。所有数字来自真实连接。");var grid=new System.Windows.Controls.Primitives.UniformGrid{Columns=2};runtimeRate=Stat(grid,"当前下载速度","—");runtimeDown=Stat(grid,"累计下载数据",Bytes(state.down));runtimeCount=Stat(grid,"IPv6 上游连接",state.ipv6Connections.ToString());runtimeFamily=Stat(grid,"网络策略",state.clashActive?"IPv6 ONLY":"待机");root.Children.Add(grid);var buttons=new StackPanel{Orientation=Orientation.Horizontal,Margin=new Thickness(0,8,0,18)};var verify=new Button{Content="运行功能验收",Margin=new Thickness(0,0,8,0)};verify.Click+=async(s,e)=>await Verify();buttons.Children.Add(verify);var restore=new Button{Content="恢复配置"};restore.Click+=async(s,e)=>await Restore();buttons.Children.Add(restore);root.Children.Add(buttons);root.Children.Add(Text("最近活动",14,true));runtimeLog=new ListBox{ItemsSource=history.ToArray(),MaxHeight=240,BorderThickness=new Thickness(0),Background=Brush("Soft"),Foreground=Brush("Ink"),Padding=new Thickness(10)};root.Children.Add(runtimeLog);}
  TextBlock Stat(System.Windows.Controls.Primitives.UniformGrid grid,string label,string value){var p=new StackPanel();var title=Text(label,11);title.SetResourceReference(TextBlock.ForegroundProperty,"Muted");p.Children.Add(title);var v=Text(value,24,true);p.Children.Add(v);grid.Children.Add(new Border{CornerRadius=new CornerRadius(14),Background=Brush("Soft"),Padding=new Thickness(18),Margin=new Thickness(0,4,12,8),Child=p});return v;}
  async Task Verify(){if(busy||!ready)return;busy=true;UpdateControls();var root=Dialog("功能验收","将读取实时规则、下载真实 UE 数据块并检查内容哈希，同时采样外网 TCP 地址。通常需要 15–60 秒。不会重新下载整套引擎。");var progress=Text("正在检测…",16,true);root.Children.Add(progress);Log("正在进行真实 UE 数据块与 IPv6 出站验收。");try{var report=json.Deserialize<Verification>(await Post("verify"));root.Children.Clear();root.Children.Add(Text(report.passed?"✓  本次功能验收全部通过":"存在未通过项",22,true));foreach(var check in report.checks){var p=new StackPanel();var title=Text((check.passed?"✓  ":"✕  ")+check.name,14,true);title.Foreground=Hex(check.passed?"#269A78":"#C75F6C");p.Children.Add(title);var detail=Text(check.detail,12);detail.SetResourceReference(TextBlock.ForegroundProperty,"Muted");p.Children.Add(detail);root.Children.Add(new Border{CornerRadius=new CornerRadius(12),Background=Brush("Soft"),Padding=new Thickness(14),Margin=new Thickness(0,0,0,9),Child=p});}root.Children.Add(Text("报告："+report.report,11));root.Children.Add(Text(report.scope,11));Log(report.passed?"功能验收全部通过，报告已保存。":"验收存在未通过项，详情已保留。");}catch(Exception e){progress.Text="检测未完成："+e.Message;Log("功能验收未完成。");}finally{busy=false;UpdateControls();}await Poll();}
  void ShowOptimizer(){var root=Dialog("CDN 检测 · 连通性与样本测速","实测两个 Epic 官方 CDN 的 1 MiB 文件片段；展示真实耗时和传输速率，暂不强制覆盖平台的 CDN/IP。");var grid=new System.Windows.Controls.Primitives.UniformGrid{Columns=3};Stat(grid,"候选节点","2");Stat(grid,"测试内容","1 MiB");Stat(grid,"网络策略","IPv6");root.Children.Add(grid);var test=new Button{Content="开始检测",HorizontalAlignment=HorizontalAlignment.Left,Margin=new Thickness(0,14,0,14)};root.Children.Add(test);var results=new StackPanel();root.Children.Add(results);test.Click+=async(s,e)=>{if(busy)return;busy=true;test.IsEnabled=false;UpdateControls();results.Children.Clear();results.Children.Add(Text("正在下载测试片段…"));try{var rows=json.Deserialize<List<Probe>>(await Post("probe"));results.Children.Clear();foreach(var row in rows){string name=row.url.Contains("akamaized")?"Akamai":"CloudFront";var p=new StackPanel();p.Children.Add(Text((row.passed?"✓  ":"✕  ")+name,16,true));p.Children.Add(Text(row.passed?"HTTP "+row.status+" · "+Bytes(row.bytes)+" · "+row.seconds.ToString("0.00")+" 秒 · "+Bytes((long)(row.bytes/Math.Max(row.seconds,0.01)))+"/s":"失败："+row.error,12));results.Children.Add(new Border{Background=Brush("Soft"),CornerRadius=new CornerRadius(12),Padding=new Thickness(16),Margin=new Thickness(0,0,0,10),Child=p});}results.Children.Add(Text("速率包含连接与 TLS 开销，不等于持续下载带宽。节点仍由平台自动选择。",11));Log("CDN 节点检测完成。");}catch(Exception ex){results.Children.Clear();results.Children.Add(Text("检测未完成："+ex.Message));}finally{busy=false;test.IsEnabled=true;UpdateControls();}await Poll();};}
  void SetTheme(){dark=!dark;var colors=dark?new[]{"#151D2A","#1F2939","#E8EDF6","#9AA9BE","#344158","#283448","#99AEFF"}:new[]{"#F1F5FA","#FDFEFF","#202938","#758195","#DFE5EE","#F2F6FC","#536FDB"};var keys=new[]{"Bg","Panel","Ink","Muted","Line","Soft","Accent"};for(int i=0;i<keys.Length;i++)window.Resources[keys[i]]=Hex(colors[i]);foreach(var kv in cards){kv.Value.Background=kv.Key==selectedName?Brush("Soft"):Brush("Panel");kv.Value.BorderBrush=kv.Key==selectedName?Hex("#8BA0E6"):Brush("Line");}ShowPage(currentPage);Log(dark?"已切换深色外观。":"已切换浅色外观。");}
 }
}



