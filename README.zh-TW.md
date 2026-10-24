# Local-Probe

[English](README.md) · [简体中文](README.zh-CN.md) · [繁體中文](README.zh-TW.md) · [日本語](README.ja.md)

**Windows 桌面管理介面與 MCP 工具，讓 AI 探索、搜尋及讀取明確授權的本機工作區。**

> **R11.3 Desktop Preview。** 自有原始碼採 [MIT](LICENSE)，第三方授權另列。目前快照沒有已發布的 GitHub Release。這是未簽章預覽版，並非正式 supervisor 或安裝程式；`execution_available=false`、`release_ready=false` 維持不變。

## 目前能力

- 列出目錄、尋找檔案、搜尋 literal 文字及分頁目錄樹。
- 有界 bytes/lines/tail 與批次讀取、受限工作區快照和環境資訊。
- 管理授權根目錄，預覽連線/資料夾的 deny/ignore 規則，提供 revision 檢查與離線設定儲存。
- 檢視連線健康、活動篩選、脫敏診斷及原生 `.lpbackup` 備份/還原。
- WebView2 介面、系統匣、DPI 縮放、主題、語言與字級。
- 完成外部設定後啟動/停止所選 Cloudflare 或 OpenAI Tunnel 連線。

GUI 只管理**一個選定連線**；儲存歷史連線不等於多連線同時執行。開發者頁僅管理設定與命令範本，沒有開放命令、腳本、建置或檔案寫入。

## 建置及開啟桌面 GUI

需要 Windows x64、WebView2 Runtime、PowerShell 7；從原始碼建置另需 Go 1.25+ 與 Git。首次解析模組需要網路或快取。依所選通道自行準備官方 `cloudflared` 或 `tunnel-client`，兩者均不隨包提供。

```powershell
git clone --branch main https://github.com/LE-saber/Local-Probe.git
Set-Location .\Local-Probe
pwsh -NoLogo -NoProfile -File .\scripts\Build-LocalProbeDesktop.ps1
pwsh -NoLogo -NoProfile -File .\scripts\Start-LocalProbeDesktop.ps1
```

缺少設定仍可開啟 GUI，不會因此授權磁碟或產生憑證。初次設定見[桌面指南](docs/DESKTOP_PREVIEW.zh-CN.md)與[部署指南](docs/DEPLOYMENT_PREVIEW.zh-CN.md)。切換版本前須從系統匣結束舊實例，僅關閉視窗不夠。

## 選擇連線

| 通道 | 本機 origin | 身分驗證 | GUI 管理 |
| --- | --- | --- | --- |
| Cloudflare Preview | `127.0.0.1:8788` | Cloudflare Access JWT | 設定後支援 |
| OpenAI Secure MCP Tunnel | `127.0.0.1:8787` | 本機 hop token + Platform Tunnel | 先完成獨立憑證初始化 |

兩條路徑互相獨立，勿讓不同啟動方式同時管理相同 origin/連接埠。本機 ready 不代表 ChatGPT Workspace 關聯或真實遠端呼叫成功。

### 初始化授權資料夾

以打算授權的實際資料夾取代範例路徑：

```powershell
$authorizedRoot = (Resolve-Path 'C:\replace-with-folder-you-authorize').Path
pwsh -NoLogo -NoProfile -ExecutionPolicy Bypass `
  -File .\scripts\Setup-LocalProbePreview.ps1 `
  -RepoRoot (Get-Location).Path `
  -AuthorizedRoot $authorizedRoot
```

初始化使用 Git 忽略的 `.runtime/` 與 `.tools\cloudflared.exe`。Cloudflare token 存於**儲存庫外、上層目錄**的 `.secrets\cloudflared-tunnel-token.txt`。勿提交 token、key、JWT、Cookie 或執行設定。

Cloudflare 公開 hostname 應轉送至 `http://127.0.0.1:8788`；在 `.runtime\cloudflare-access.json` 填寫 issuer、JWKS、audience、JWT subject 對應及相同的 `public_hosts`。連線就緒後使用 `https://<public-host>/mcp`，先驗證 `initialize`、`tools/list`、`ping` 再讀取檔案。GUI 只管理自行啟動的子程序。

舊版原生 Preview 仍可透過 [Build-LocalProbePreview.ps1](scripts/Build-LocalProbePreview.ps1) 與 [Start-LocalProbePreview.ps1](scripts/Start-LocalProbePreview.ps1) 使用；新桌面使用者應採上方 Desktop 步驟。

## 存取與發布邊界

可用檔案及工具由 profile roots、tools、deny/ignore 與唯讀政策決定。Preview 不開放任意 shell、任意命令、寫入、憑證展示或由模型編輯設定。後端模組和設定意圖不等於執行已開放。

正式 supervisor/service、真實 WFP/broker enforcement、簽章、安裝程式、自動更新、Linux/macOS GUI、預設大規模索引、百萬檔案端到端證據及獨立安全審查均未宣稱交付。本機檢查與歷史驗證不能建立這些保證。[R11.3 發布說明](docs/RELEASE_NOTES_R11.3.zh-CN.md) 是草稿，並非已發布版本。

## 文件與證據

詳細指南大多保留簡體中文原文：

- [部署](docs/DEPLOYMENT_PREVIEW.zh-CN.md)、[Desktop GUI](docs/DESKTOP_PREVIEW.zh-CN.md)、[原生 Preview](docs/PREVIEW.zh-CN.md)。
- [Cloudflare 設定](docs/CLOUDFLARE_TUNNEL_SETUP.zh-CN.md)、[OpenAI Tunnel 設定](docs/TUNNEL_SETUP.zh-CN.md)。
- [開發](docs/DEVELOPMENT.zh-CN.md)、[架構](docs/ARCHITECTURE.zh-CN.md)、[工具契約](docs/TOOL_CONTRACTS.md)、[威脅模型](docs/THREAT_MODEL.md)。
- [實施狀態](docs/IMPLEMENTATION_STATUS.md)、[R11 後端進度](docs/R11_BACKEND_PROGRESS.zh-CN.md)、[發布清單](docs/R9_RELEASE_CHECKLIST.zh-CN.md)。
- [總體計畫](docs/MASTER_PLAN.zh-CN.md)、[程式碼地圖](.planning/codebase/STRUCTURE.md)。

## 開發與打包

於合適且已設定的環境執行現有檢查：

```powershell
go test ./...
go test -race ./...
go vet ./...
go build ./...
git diff --check
```

環境檢查見 [Test-LocalProbeEnvironment.ps1](scripts/Test-LocalProbeEnvironment.ps1)。舊版原生 Preview ZIP 命令如下：

```powershell
pwsh -NoLogo -NoProfile -ExecutionPolicy Bypass `
  -File .\scripts\Package-LocalProbePreview.ps1 `
  -RepoRoot (Get-Location).Path -Version R9-preview -Build
```

這會準備未簽章的人工測試包，不是穩定發行。打包、非 Quick 驗收、乾淨目錄重建及真實 Windows/Tunnel/ChatGPT 檢查是獨立關卡；本次文件更新不宣稱重新執行過。保留第三方授權通知，分發前另行審查。
