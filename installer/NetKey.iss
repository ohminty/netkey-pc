; NetKey for Windows: the app, the service and the NetKey network adapter.
; Build: ISCC.exe /DStage=<staging folder> NetKey.iss
;   <Stage>\app\        the published WinUI app (dotnet publish)
;   <Stage>\netkeyd.exe the service
;   <Stage>\driver\     TAP-Windows6 (OpenVPN, signed by Microsoft): OemVista.inf, tap0901.sys, tap0901.cat, devcon.exe
;   <Stage>\licenses\   Microsoft runtime license terms and notices; LICENSE, NOTICE, THIRD-PARTY.txt
; build-windows.ps1 prepares all of it.

#ifndef Stage
  #define Stage "..\stage"
#endif
#define AppVersion "0.1.0"

[Setup]
AppId={{6B7E2E0C-4F4B-4E8A-9C1D-0E5B2A7C9F31}
AppName=NetKey
AppVersion={#AppVersion}
AppVerName=NetKey
UninstallDisplayName=NetKey
VersionInfoVersion={#AppVersion}
AppPublisher=NetKey
AppPublisherURL=https://netkey.no
DefaultDirName={autopf}\NetKey
DefaultGroupName=NetKey
DisableProgramGroupPage=yes
PrivilegesRequired=admin
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
MinVersion=10.0.17763
OutputDir={#Stage}\out
OutputBaseFilename=NetKey-{#AppVersion}-setup
SetupIconFile={#Stage}\app\Assets\NetKey.ico
UninstallDisplayIcon={app}\Assets\NetKey.ico
Compression=lzma2/max
SolidCompression=yes
WizardStyle=modern
; NetKey artwork instead of Inno Setup's default pictures (100 %, 150 %, 200 % display scaling)
WizardImageFile=art\wiz-large-100.bmp,art\wiz-large-150.bmp,art\wiz-large-200.bmp
WizardSmallImageFile=art\wiz-small-100.bmp,art\wiz-small-150.bmp,art\wiz-small-200.bmp
CloseApplications=yes
RestartApplications=no

[Files]
Source: "{#Stage}\app\*"; DestDir: "{app}"; Flags: recursesubdirs ignoreversion
Source: "{#Stage}\netkeyd.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#Stage}\driver\*"; DestDir: "{app}\driver"; Flags: ignoreversion
Source: "{#Stage}\LICENSE"; DestDir: "{app}"; DestName: "LICENSE.txt"; Flags: ignoreversion
Source: "{#Stage}\NOTICE"; DestDir: "{app}"; DestName: "NOTICE.txt"; Flags: ignoreversion
Source: "{#Stage}\THIRD-PARTY.txt"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#Stage}\licenses\*"; DestDir: "{app}\licenses"; Flags: ignoreversion

[Icons]
Name: "{group}\NetKey"; Filename: "{app}\NetKey.exe"; IconFilename: "{app}\Assets\NetKey.ico"
Name: "{autodesktop}\NetKey"; Filename: "{app}\NetKey.exe"; IconFilename: "{app}\Assets\NetKey.ico"; Tasks: desktopicon

[Tasks]
Name: "desktopicon"; Description: "Create a desktop shortcut"
Name: "autostart"; Description: "Start NetKey in the notification area when I sign in"

[Registry]
Root: HKCU; Subkey: "Software\Microsoft\Windows\CurrentVersion\Run"; ValueType: string; ValueName: "NetKey"; ValueData: """{app}\NetKey.exe"" --tray"; Flags: uninsdeletevalue; Tasks: autostart

[Run]
Filename: "{app}\NetKey.exe"; Description: "Open NetKey"; Flags: nowait postinstall skipifsilent

[UninstallRun]
Filename: "{app}\netkeyd.exe"; Parameters: "uninstall ""{app}\driver"""; Flags: runhidden waituntilterminated; RunOnceId: "NetKeyUninstall"

[UninstallDelete]
; identity.json stays: it's this PC's permanent ID at the hub (nickname, bans), like the Pi's box identity
Type: files; Name: "{commonappdata}\NetKey\party.json"
Type: files; Name: "{commonappdata}\NetKey\hub.json"
Type: files; Name: "{commonappdata}\NetKey\settings.json"
Type: files; Name: "{commonappdata}\NetKey\netkeyd.log"
Type: files; Name: "{commonappdata}\NetKey\*.tmp"

[Code]
// Stop the running service before its files are replaced (upgrades).
function PrepareToInstall(var NeedsRestart: Boolean): String;
var
  Code: Integer;
begin
  Exec(ExpandConstant('{sys}\sc.exe'), 'stop NetKey', '', SW_HIDE, ewWaitUntilTerminated, Code);
  Sleep(1500);
  Result := '';
end;

// Create the NetKey adapter and the service; show the log if that fails.
procedure CurStepChanged(CurStep: TSetupStep);
var
  Code: Integer;
  Log: String;
begin
  if CurStep = ssPostInstall then
  begin
    WizardForm.StatusLabel.Caption := 'Setting up the NetKey network adapter and service...';
    Log := ExpandConstant('{app}\install.log');
    if not Exec(ExpandConstant('{cmd}'), '/c ""' + ExpandConstant('{app}\netkeyd.exe') + '" install "' +
        ExpandConstant('{app}\driver') + '" > "' + Log + '" 2>&1"', '', SW_HIDE, ewWaitUntilTerminated, Code)
        or (Code <> 0) then
      MsgBox('Setting up the NetKey network adapter or service failed (code ' + IntToStr(Code) + ').' + #13#10 +
        'Details are in ' + Log, mbError, MB_OK);
  end;
end;
