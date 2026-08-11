; Inno Setup script for TR Driver Sync
#define MyAppName "TR Driver Sync"
#define MyAppVersion "0.5.1"
#define MyAppPublisher "TR Driver"
#define MyAppExeName "necipdrive-sync.exe"

[Setup]
AppId={{A7C2E9B1-4D55-4F2A-9C11-8B3E6F0A1D22}
AppName={#MyAppName}
AppVersion={#MyAppVersion}
AppPublisher={#MyAppPublisher}
DefaultDirName={localappdata}\Programs\TRDriver
DefaultGroupName=TR Driver
DisableProgramGroupPage=yes
PrivilegesRequired=lowest
OutputDir=..\..\dist\windows
OutputBaseFilename=TRDriverSyncSetup
Compression=lzma
SolidCompression=yes
WizardStyle=modern
; SetupIconFile is optional — use exe icon if ico missing
UninstallDisplayIcon={app}\{#MyAppExeName}
CloseApplications=force
RestartApplications=no

[Languages]
Name: "turkish"; MessagesFile: "compiler:Languages\Turkish.isl"
Name: "english"; MessagesFile: "compiler:Default.isl"

[Tasks]
Name: "desktopicon"; Description: "Masaüstü simgesi oluştur"; GroupDescription: "Ek simgeler:"; Flags: unchecked
Name: "autostart"; Description: "Windows açılışında tray'de başlat"; GroupDescription: "Başlangıç:"; Flags: checkedonce

[Files]
Source: "..\..\dist\windows\necipdrive-sync.exe"; DestDir: "{app}"; Flags: ignoreversion

[Icons]
Name: "{group}\{#MyAppName}"; Filename: "{app}\{#MyAppExeName}"
Name: "{autodesktop}\{#MyAppName}"; Filename: "{app}\{#MyAppExeName}"; Tasks: desktopicon

[Registry]
Root: HKCU; Subkey: "Software\Microsoft\Windows\CurrentVersion\Run"; ValueType: string; ValueName: "TRDriverSync"; ValueData: """{app}\{#MyAppExeName}"""; Flags: uninsdeletevalue; Tasks: autostart

[Code]
function InitializeSetup(): Boolean;
var
  Version: String;
  ResultCode: Integer;
begin
  Result := True;
  if not RegQueryStringValue(HKLM, 'SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}', 'pv', Version)
     and not RegQueryStringValue(HKLM, 'SOFTWARE\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}', 'pv', Version) then
  begin
    if MsgBox('TR Driver Sync için Microsoft Edge WebView2 Runtime gerekli.'#13#10'İndirme sayfası açılsın mı?', mbConfirmation, MB_YESNO) = IDYES then
      ShellExec('open', 'https://developer.microsoft.com/microsoft-edge/webview2/', '', '', SW_SHOWNORMAL, ewNoWait, ResultCode);
  end;
end;

[Run]
Filename: "{app}\{#MyAppExeName}"; Description: "TR Driver Sync'i başlat"; Flags: nowait postinstall skipifsilent

[UninstallDelete]
Type: filesandordirs; Name: "{localappdata}\TRDriver\cache"
Type: filesandordirs; Name: "{localappdata}\TRDriver\webview"
