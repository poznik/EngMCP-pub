[Setup]
AppId=EngMCP
AppName=EngMCP
AppVersion=0.3.0
DefaultDirName={localappdata}\Programs\EngMCP
PrivilegesRequired=lowest
ArchitecturesAllowed=x64compatible
OutputDir=..\dist
OutputBaseFilename=EngMCP-Setup-x64
Compression=lzma2
SolidCompression=yes
UninstallDisplayIcon={app}\engmcp.exe
DisableProgramGroupPage=yes
AllowNoIcons=yes
CloseApplications=no

[Languages]
Name: "russian"; MessagesFile: "compiler:Languages\Russian.isl"

[Tasks]
Name: "autostart"; Description: "Запускать EngMCP при входе в Windows"; Flags: unchecked

[Files]
Source: "..\dist\engmcp.exe"; DestDir: "{app}"; Flags: ignoreversion

Source: "THIRD-PARTY-NOTICES.txt"; DestDir: "{app}"; Flags: ignoreversion

[Icons]
Name: "{userprograms}\Настроить EngMCP"; Filename: "{app}\engmcp.exe"; Parameters: "open"
Name: "{userstartup}\EngMCP"; Filename: "{app}\engmcp.exe"; Parameters: "serve"; Tasks: autostart

[InstallDelete]
Type: files; Name: "{userstartup}\EngMCP.lnk"

[Run]
Filename: "{app}\engmcp.exe"; Parameters: "open"; Description: "Открыть настройки EngMCP"; Flags: postinstall nowait skipifsilent

[UninstallRun]
Filename: "{app}\engmcp.exe"; Parameters: "stop"; Flags: runhidden waituntilterminated

[Code]
function PrepareToInstall(var NeedsRestart: Boolean): String;
var
  AppExe: String;
  Code: Integer;
begin
  AppExe := ExpandConstant('{app}\engmcp.exe');
  if FileExists(AppExe) then
    if not Exec(AppExe, 'stop', '', SW_HIDE, ewWaitUntilTerminated, Code) or (Code <> 0) then
      Result := 'Закрой EngMCP перед обновлением.';
end;
