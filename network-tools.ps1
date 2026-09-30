param([ValidateSet('EnableBypass','RestoreBypass','EnableEpicProxy','RestoreEpicProxy','Audit','EnableGuard','RestoreGuard')][string]$Action='Audit',[int]$Seconds=60,[string]$StateDirectory='')
$ErrorActionPreference='Stop'
$base=$PSScriptRoot
$stateDir=if($StateDirectory){$StateDirectory}else{Join-Path $base 'runtime'}
New-Item -ItemType Directory -Path $stateDir -Force | Out-Null
$registryPath='HKCU:\Software\Microsoft\Windows\CurrentVersion\Internet Settings'
$bypassState=Join-Path $stateDir 'proxy-bypass-state.json'
$guardState=Join-Path $stateDir 'ipv4-guard-state.json'
$epicState=Join-Path $stateDir 'epic-install-proxy-state.json'
function Refresh-InternetSettings {
 if(-not ('EpicIPv6WinInet' -as [type])) {Add-Type -TypeDefinition 'using System; using System.Runtime.InteropServices; public static class EpicIPv6WinInet { [DllImport("wininet.dll")] public static extern bool InternetSetOption(IntPtr h, int o, IntPtr b, int l); }'}
 [void][EpicIPv6WinInet]::InternetSetOption([IntPtr]::Zero,39,[IntPtr]::Zero,0)
 [void][EpicIPv6WinInet]::InternetSetOption([IntPtr]::Zero,37,[IntPtr]::Zero,0)
}
function Require-Admin {
 $identity=[Security.Principal.WindowsIdentity]::GetCurrent()
 if(-not ([Security.Principal.WindowsPrincipal]::new($identity)).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)){throw 'Run this action as Administrator.'}
}
switch($Action){
 'EnableEpicProxy' {
  if(Test-Path -LiteralPath $epicState){throw 'Epic installer proxy already managed.'}
  $path=Join-Path $env:LOCALAPPDATA 'Epic Games\Epic Online Services\InstallHelper\CrashLogs\Saved\Config\WindowsClient\Engine.ini'
  $exists=Test-Path -LiteralPath $path
  [byte[]]$original=@(); if($exists){$original=[IO.File]::ReadAllBytes($path)}
  $text=if($exists){[IO.File]::ReadAllText($path)}else{''}
  if($text -match '(?im)^\s*HttpProxyAddress\s*='){throw 'An existing Epic installer proxy is configured. Review before changing.'}
  $addition="`r`n; BEGIN EpicIPv6Helper`r`n[HTTP]`r`nHttpProxyAddress=http://127.0.0.1:17891`r`n; END EpicIPv6Helper`r`n"
  @{path=$path;existed=$exists;original=[Convert]::ToBase64String($original);addition=$addition;time=(Get-Date).ToString('o')} | ConvertTo-Json | Set-Content -LiteralPath $epicState -Encoding utf8
  New-Item -ItemType Directory -Path (Split-Path $path) -Force | Out-Null
  [IO.File]::WriteAllText($path,$text+$addition,[Text.UTF8Encoding]::new($true))
  'Epic installation worker now has its own IPv6 helper proxy. Pause/resume the download to restart the worker.'
 }
 'RestoreEpicProxy' {
  if(-not (Test-Path -LiteralPath $epicState)){'No managed Epic installer proxy.';break}
  $s=Get-Content -LiteralPath $epicState -Raw | ConvertFrom-Json
  $text=[IO.File]::ReadAllText($s.path)
  $originalText=[Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($s.original)).TrimStart([char]0xfeff)
  $remaining=$text.Replace($s.addition,'')
  if($remaining -eq $originalText){if($s.existed){[IO.File]::WriteAllBytes($s.path,[Convert]::FromBase64String($s.original))}else{Remove-Item -LiteralPath $s.path}}else{[IO.File]::WriteAllText($s.path,$remaining,[Text.UTF8Encoding]::new($true))}
  Move-Item -LiteralPath $epicState -Destination (Join-Path $stateDir ('epic-install-proxy-restored-'+(Get-Date -Format 'yyyyMMdd-HHmmss')+'.json'))
  'Managed Epic installer proxy restored.'
 }
 'EnableBypass' {
  if(Test-Path -LiteralPath $bypassState){throw 'Bypass already active. Restore it before enabling again.'}
  $domains=@((Invoke-RestMethod 'http://127.0.0.1:17890/api/state').domains)
  $previous=Get-ItemProperty -LiteralPath $registryPath
  $old=[string]$previous.ProxyOverride
  $parts=@($old -split ';' | Where-Object {$_})
  $added=@($domains | Where-Object {$_ -notin $parts})
  @{original=$old;existed=($null -ne $previous.PSObject.Properties['ProxyOverride']);added=$added;time=(Get-Date).ToString('o')} | ConvertTo-Json | Set-Content -LiteralPath $bypassState -Encoding utf8
  Set-ItemProperty -LiteralPath $registryPath -Name ProxyOverride -Value (($parts+$added)-join ';')
  Refresh-InternetSettings
  'Added only Epic download domains to proxy bypass. Restart Epic to refresh cached settings.'
 }
 'RestoreBypass' {
  if(-not (Test-Path -LiteralPath $bypassState)){'No managed bypass to restore.';break}
  $state=Get-Content -LiteralPath $bypassState -Raw | ConvertFrom-Json
  $current=[string](Get-ItemProperty -LiteralPath $registryPath).ProxyOverride
  $remaining=@($current -split ';' | Where-Object {$_ -and $_ -notin $state.added})
  $originalParts=@($state.original -split ';' | Where-Object {$_})
  if((Compare-Object $remaining $originalParts).Count -eq 0){$value=$state.original}else{$value=$remaining -join ';'}
  if(-not $state.existed -and -not $value){Remove-ItemProperty -LiteralPath $registryPath -Name ProxyOverride -ErrorAction SilentlyContinue}else{Set-ItemProperty -LiteralPath $registryPath -Name ProxyOverride -Value $value}
  Refresh-InternetSettings
  Move-Item -LiteralPath $bypassState -Destination (Join-Path $stateDir ('proxy-bypass-restored-'+(Get-Date -Format 'yyyyMMdd-HHmmss')+'.json'))
  'Managed proxy bypass restored; unrelated entries preserved.'
 }
 'EnableGuard' {
  Require-Admin
  if(Test-Path -LiteralPath $guardState){throw 'Guard state already exists. Restore first.'}
  if(@(Get-NetFirewallProfile | Where-Object {-not $_.Enabled}).Count){throw 'Firewall profile disabled; cannot certify enforcement.'}
  $paths=@(Get-CimInstance Win32_Process | Where-Object {$_.Name -match '^(EpicGamesLauncher|EpicWebHelper|EpicOnlineServicesUserHelper|EpicOnlineServicesInstallHelper)\.exe$'} | Select-Object -ExpandProperty ExecutablePath -Unique | Where-Object {$_})
  $installRoot='C:\Program Files (x86)\Epic Games\Epic Online Services'
  if(Test-Path -LiteralPath $installRoot){$paths+=@(Get-ChildItem -LiteralPath $installRoot -Recurse -Filter 'EpicOnlineServicesInstallHelper.exe' -ErrorAction SilentlyContinue | Select-Object -ExpandProperty FullName)}
  $paths=@($paths | Sort-Object -Unique)
  if($paths.Count -eq 0){throw 'No Epic executables discovered.'}
  $names=@();try {foreach($p in $paths){$name='EpicIPv6Helper-'+[Guid]::NewGuid().ToString('N');New-NetFirewallRule -Name $name -DisplayName $name -Group 'EpicIPv6Helper temporary audit' -Direction Outbound -Action Block -Program $p -RemoteAddress @('0.0.0.0-126.255.255.255','128.0.0.0-255.255.255.255') -Protocol Any -Profile Any | Out-Null;$names+=$name;@{rules=$names;programs=$paths;time=(Get-Date).ToString('o')} | ConvertTo-Json | Set-Content -LiteralPath $guardState -Encoding utf8}}catch{foreach($n in $names){Remove-NetFirewallRule -Name $n -ErrorAction SilentlyContinue};throw}
  'IPv4 egress guard enabled for discovered Epic executables. Loopback remains allowed; existing third-party loopback proxies can still relay traffic. Verify bypass separately.'
 }
 'RestoreGuard' {
  Require-Admin
  if(Test-Path -LiteralPath $guardState){$s=Get-Content -LiteralPath $guardState -Raw | ConvertFrom-Json;foreach($n in $s.rules){if($n -notlike 'EpicIPv6Helper-*'){throw 'Invalid managed rule name'};Remove-NetFirewallRule -Name $n -ErrorAction SilentlyContinue};Move-Item -LiteralPath $guardState -Destination (Join-Path $stateDir ('ipv4-guard-restored-'+(Get-Date -Format 'yyyyMMdd-HHmmss')+'.json'))}
  'Temporary managed firewall rules removed.'
 }
 'Audit' {
  $start=Get-Date;$end=$start.AddSeconds($Seconds);$seen=@{};$samples=0;$errors=@()
  $before=Invoke-RestMethod 'http://127.0.0.1:17890/api/state'
  do {try {$procs=@(Get-Process | Where-Object {$_.ProcessName -match '^(EpicIPv6Helper|EpicGamesLauncher|EpicWebHelper|EpicOnlineServicesUserHelper|EpicOnlineServicesInstallHelper)$'});$ids=@($procs.Id);$map=@{};foreach($p in $procs){$map[[int]$p.Id]=$p.ProcessName};foreach($c in @(Get-NetTCPConnection -State Established -ErrorAction Stop | Where-Object {$_.OwningProcess -in $ids})){$ip=[Net.IPAddress]::Parse($c.RemoteAddress);$kind=if([Net.IPAddress]::IsLoopback($ip)){'loopback'}elseif($ip.AddressFamily -eq [Net.Sockets.AddressFamily]::InterNetworkV6 -and -not $ip.IsIPv4MappedToIPv6){'ipv6'}else{'ipv4'};$k="$($c.OwningProcess)|$($c.LocalPort)|$($c.RemoteAddress)|$($c.RemotePort)";if(-not $seen.ContainsKey($k)){$seen[$k]=[pscustomobject]@{process=$map[[int]$c.OwningProcess];pid=$c.OwningProcess;localPort=$c.LocalPort;remote=$c.RemoteAddress;port=$c.RemotePort;family=$kind;firstSeen=(Get-Date).ToString('o')}}};$samples++}catch{$errors+=$_.Exception.Message};Start-Sleep -Milliseconds 300}while((Get-Date) -lt $end)
  $after=Invoke-RestMethod 'http://127.0.0.1:17890/api/state'
  $rows=@($seen.Values);$result=[ordered]@{started=$start.ToString('o');ended=(Get-Date).ToString('o');samples=$samples;helperReceivedDelta=($after.down-$before.down);externalIPv4Observed=@($rows | Where-Object {$_.family -eq 'ipv4'}).Count;helperExternalIPv4Observed=@($rows | Where-Object {$_.family -eq 'ipv4' -and $_.process -eq 'EpicIPv6Helper'}).Count;otherLoopbackProxyObserved=@($rows | Where-Object {$_.family -eq 'loopback' -and $_.port -eq 7890 -and $_.process -ne 'EpicIPv6Helper'}).Count;connections=$rows;errors=$errors;limitation='TCP snapshots may miss short connections and do not measure UDP or decrypt HTTPS. IPv6-only helper is enforced in code; whole Epic purity requires controlled process/firewall coverage and bypass validation.'}
  $path=Join-Path $stateDir ('audit-'+(Get-Date -Format 'yyyyMMdd-HHmmss')+'.json');$result | ConvertTo-Json -Depth 6 | Set-Content -LiteralPath $path -Encoding utf8
  [pscustomobject]$result | Select-Object started,ended,samples,helperReceivedDelta,externalIPv4Observed,helperExternalIPv4Observed,otherLoopbackProxyObserved | ConvertTo-Json
  "Report: $path"
 }
}
