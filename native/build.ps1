$ErrorActionPreference='Stop'
$root=Split-Path $PSScriptRoot
$args=@('/nologo','/target:winexe','/platform:x64','/optimize+',('/out:'+(Join-Path $root 'output\EpicIPv6Native.exe')),('/win32manifest:'+(Join-Path $PSScriptRoot 'app.manifest')),('/resource:'+(Join-Path $PSScriptRoot 'MainWindow.xaml')+',MainWindow.xaml'),('/resource:'+(Join-Path $root 'output\IPv6Engine-0.8.1.exe')+',engine.exe'),'/r:C:\Windows\Microsoft.NET\Framework64\v4.0.30319\WPF\PresentationFramework.dll','/r:C:\Windows\Microsoft.NET\Framework64\v4.0.30319\WPF\PresentationCore.dll','/r:C:\Windows\Microsoft.NET\Framework64\v4.0.30319\WPF\WindowsBase.dll','/r:System.Xaml.dll','/r:System.Net.Http.dll','/r:System.Web.Extensions.dll',(Join-Path $PSScriptRoot 'App.cs'),(Join-Path $PSScriptRoot 'Research.cs'),(Join-Path $PSScriptRoot 'QuickRouting.cs'),(Join-Path $PSScriptRoot 'Upstreams.cs'),(Join-Path $PSScriptRoot 'AssemblyInfo.cs'))
foreach($icon in Get-ChildItem (Join-Path $PSScriptRoot 'icons') -Filter *.img){$args+=('/resource:'+$icon.FullName+',icons.'+$icon.BaseName)}
& 'C:\Windows\Microsoft.NET\Framework64\v4.0.30319\csc.exe' @args
if($LASTEXITCODE -ne 0){throw 'Native build failed'}


