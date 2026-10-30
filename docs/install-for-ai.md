# Installing Lovkilde: instructions for an AI assistant

You are installing Lovkilde for a user: a local server with Danish legislation from
retsinformation.dk, which you then use through MCP. Follow these steps in order. Ask the user
before anything that needs their permission (installing software, starting it at login), and tell
them what you did when you are done. Talk to the user in their language; Lovkilde's own pages and
answers are in Danish.

Lovkilde is an unofficial copy. It is not legal advice, and its texts can contain errors. Say so
to the user, and never accept the terms of use on their behalf (step 5).

## What it needs

- Linux, macOS or Windows, on amd64 or arm64.
- About 5 GB of free disk: the law texts (1.3 GB), the search index (0.9 GB), and room for the
  next edition while it replaces the current one.
- About 2 GB of RAM while running, and 4 GB for a minute or two on first start, while it builds
  its search index.
- Internet access to github.com and huggingface.co.

## 1. Download the program

Pick the archive for the platform from the latest release:

| Platform | File |
|---|---|
| Linux, x86-64 | `lovkilde-linux-amd64.tar.gz` |
| Linux, ARM | `lovkilde-linux-arm64.tar.gz` |
| macOS, Apple silicon | `lovkilde-darwin-arm64.tar.gz` |
| macOS, Intel | `lovkilde-darwin-amd64.tar.gz` |
| Windows, x86-64 | `lovkilde-windows-amd64.zip` |
| Windows, ARM | `lovkilde-windows-arm64.zip` |

The URL is `https://github.com/lkarlslund/lovkilde/releases/latest/download/<file>`. Check
the file against `SHA256SUMS` from the same place. On Linux:

```bash
mkdir -p ~/.local/bin && cd "$(mktemp -d)"
curl -fsSLO https://github.com/lkarlslund/lovkilde/releases/latest/download/lovkilde-linux-amd64.tar.gz
curl -fsSLO https://github.com/lkarlslund/lovkilde/releases/latest/download/SHA256SUMS
grep ' lovkilde-linux-amd64.tar.gz$' SHA256SUMS | sha256sum -c
tar -xzf lovkilde-linux-amd64.tar.gz
install lovkilde-linux-amd64/lovkilde ~/.local/bin/lovkilde
```

On macOS, check with `grep ' <file>$' SHA256SUMS | shasum -a 256 -c`. Download with curl, as above,
rather than a browser: macOS then does not quarantine the program. On Windows, compare
`(Get-FileHash <file>).Hash` with the file's line in `SHA256SUMS`, unpack the zip with
`Expand-Archive` and put `lovkilde.exe` in a folder of the user's choice, for example
`%LOCALAPPDATA%\Programs\lovkilde`.

Check that it runs: `lovkilde --version` prints `lovkilde version rNNNNN`.

## 2. Start it once

```bash
lovkilde serve --no-browser
```

The first start downloads the law texts (1.3 GB) into `~/.lovkilde`, then serves at
`http://127.0.0.1:8787` and builds its search index in the background. Lookups work at once;
search works after a minute or two. The log says `search index ready` when it does.

It listens on this machine only. Do not make it reachable from the network
(`--addr 0.0.0.0:8787`) unless the user asks for that; if they do, they must set an API key on
`http://127.0.0.1:8787/settings` first.

## 3. Keep it running

Ask the user whether Lovkilde should start at login. If yes:

**Linux (systemd user service):** write `~/.config/systemd/user/lovkilde.service`:

```ini
[Unit]
Description=Lovkilde
After=network-online.target

[Service]
ExecStart=%h/.local/bin/lovkilde serve --no-browser
Restart=on-failure
RestartSec=30

[Install]
WantedBy=default.target
```

Then `systemctl --user daemon-reload && systemctl --user enable --now lovkilde`.

**macOS (launchd):** write `~/Library/LaunchAgents/dk.lovkilde.plist` with `ProgramArguments`
set to the program's path, `serve`, `--no-browser`, and `RunAtLoad` and `KeepAlive` set to true.
Then `launchctl load ~/Library/LaunchAgents/dk.lovkilde.plist`.

**Windows:** by default, put a shortcut in the user's Startup folder. Lovkilde then starts at
logon and sits in the notification area, with its icon: a click opens the website, and its menu
has "Om Lovkilde" and "Afslut".

```powershell
$exe = "$env:LOCALAPPDATA\Programs\lovkilde\lovkilde.exe"
$lnk = (New-Object -ComObject WScript.Shell).CreateShortcut("$([Environment]::GetFolderPath('Startup'))\Lovkilde.lnk")
$lnk.TargetPath = $exe
$lnk.Arguments = "serve --no-browser"
$lnk.WindowStyle = 7  # minimised: the window hides once the icon is up
$lnk.Save()
Start-Process $exe -ArgumentList "serve","--no-browser"
```

Offer the other two ways when they fit better:

- **A scheduled task at logon**, if the user prefers Task Scheduler:

  ```powershell
  Register-ScheduledTask -TaskName Lovkilde -Trigger (New-ScheduledTaskTrigger -AtLogOn) `
    -Action (New-ScheduledTaskAction -Execute $exe -Argument "serve --no-browser") `
    -Settings (New-ScheduledTaskSettingsSet -ExecutionTimeLimit (New-TimeSpan))
  ```

- **A Windows service**, to run from boot without anyone logged in, for example on a shared
  machine. It needs a prompt running as administrator, and has no icon:

  ```powershell
  & $exe service install
  ```

  It copies itself into `%USERPROFILE%\.lovkilde\program`, starts again if it fails, and logs to
  `%USERPROFILE%\.lovkilde\lovkilde.log`; `lovkilde service uninstall` removes it. Use this
  rather than a service wrapper such as NSSM: when the user updates the program, the service
  switches to the new build through the service manager, which a wrapper does not follow.

Without any of these, the user starts Lovkilde themselves; on Windows by double-clicking it.

## 4. Connect your MCP client

The server's MCP endpoint is `http://127.0.0.1:8787/mcp` (streamable HTTP).

**Claude Code:**

```bash
claude mcp add --scope user --transport http lovkilde http://127.0.0.1:8787/mcp
```

**A client that starts MCP servers itself** (for example Claude Desktop) runs Lovkilde over
stdio instead, and needs no running server:

```json
{
  "mcpServers": {
    "lovkilde": {
      "command": "/full/path/to/lovkilde",
      "args": ["mcp"]
    }
  }
}
```

**Other clients:** use the HTTP endpoint if they support it, otherwise `lovkilde mcp` over stdio.

The client may need a restart to see the new server. The tools are `eula`, `lookup`, `search` and
`update`.

## 5. Accept the terms with the user

No tool answers before the terms of use are accepted. Call `eula` without arguments, show the
user the terms in full, and ask whether they accept them. Only when the user says yes, call
`eula` with `accept: true`. Acceptance holds for this server, its website included, and across
restarts. The user can also accept on `http://127.0.0.1:8787/eula`.

## 6. Check that it works

Ask something with a known answer, for example `lookup` with `document: "eli/lta/2019/1399"` and
`address: "§ 1"`. The answer names the source of its text and the date of the data.

## Later: updates

Lovkilde checks daily for newer law texts and a newer version of itself, and downloads nothing on
its own. When something is available, its answers end with a note saying so. Tell the user. Run
the tool `update` with `action: "dataset"` or `action: "app"` only when the user asks for it.
`action: "check"` asks right away.

## If something fails

- **The port is taken:** another Lovkilde is probably running already; `curl -s -o /dev/null -w
  '%{http_code}' http://127.0.0.1:8787/status` answers 200, or 403 before the terms are accepted.
  Start a second one elsewhere with `--addr 127.0.0.1:8788`.
- **The first download fails:** check the network and start it again.
- **Anything else:** run `lovkilde serve --no-browser` in a terminal and read its log, or show
  the user `lovkilde --help`.
