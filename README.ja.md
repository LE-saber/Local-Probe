# Local-Probe

[English](README.md) · [简体中文](README.zh-CN.md) · [繁體中文](README.zh-TW.md) · [日本語](README.ja.md)

> **R12 開発更新:** `feat/r12-dual-mode` にはペアリング済み Agent、暗号化 Relay、操作ごとのローカル承認を必要とする Windows バックエンドが含まれます。既存 GUI/Web の高度な操作画面への接続は未完了です。既定の文書は英語です。[英語 README](README.md)、[開発ガイド](docs/R12_DEVELOPMENT.md)、[GUI 引き継ぎ](docs/GUI_HANDOFF.md)をご覧ください。以下は R11.3 デスクトップの説明です。

**明示的に許可したローカルワークスペースを探索・検索・読取するための、Windows デスクトップ管理 UI と MCP ツール。**

> **R11.3 Desktop Preview。** 独自コードは [MIT](LICENSE)、第三者コードは各自のライセンスです。この時点で公開済み GitHub Release はありません。署名なしのプレビューであり、本番 supervisor やインストーラーではありません。`execution_available=false`、`release_ready=false` は維持されています。

## 現在できること

- ディレクトリ一覧、ファイル探索、リテラル文字検索、ディレクトリツリーのページング。
- バイト/行範囲、末尾、一括読取、範囲を限定したワークスペーススナップショットと環境情報。
- 許可したルートの管理、接続/フォルダー別 deny/ignore ルールのプレビュー、revision 確認、オフライン設定保存。
- 接続状態、フィルター付き活動履歴、機密情報を除いた診断、`.lpbackup` バックアップ/復元。
- WebView2 UI、トレイ、DPI、テーマ、言語、文字サイズ。
- 外部設定完了後の、選択した Cloudflare または OpenAI Tunnel 接続の起動/停止。

GUI が管理するのは**選択した接続一つ**です。履歴を保存できても複数の同時接続ではありません。Developer ページは設定とコマンドテンプレートを扱い、コマンド、スクリプト、ビルド、ファイル書込を有効化しません。

## デスクトップ GUI のビルドと起動

Windows x64、WebView2 Runtime、PowerShell 7 が必要です。ソースビルドには Go 1.25 以上と Git も必要で、初回のモジュール取得にはネットワークかキャッシュが必要です。接続方式に応じて公式 `cloudflared` / `tunnel-client` を別途用意してください。同梱されません。

```powershell
git clone --branch main https://github.com/LE-saber/Local-Probe.git
Set-Location .\Local-Probe
pwsh -NoLogo -NoProfile -File .\scripts\Build-LocalProbeDesktop.ps1
pwsh -NoLogo -NoProfile -File .\scripts\Start-LocalProbeDesktop.ps1
```

設定がなくても GUI は開けます。起動だけでディスクへの許可や資格情報の作成は行いません。初回設定は[デスクトップガイド](docs/DESKTOP_PREVIEW.zh-CN.md)と[配置ガイド](docs/DEPLOYMENT_PREVIEW.zh-CN.md)を参照してください。バージョン切替時はトレイから旧インスタンスを終了し、ウィンドウを閉じるだけにしないでください。

## 接続方式

| 方式 | ローカル origin | 認証 | GUI 管理 |
| --- | --- | --- | --- |
| Cloudflare Preview | `127.0.0.1:8788` | Cloudflare Access JWT | 設定後に対応 |
| OpenAI Secure MCP Tunnel | `127.0.0.1:8787` | ローカル hop token + Platform Tunnel | 資格情報の個別初期化後に対応 |

二つは独立した経路です。同じ origin やポートを別々の起動方法で同時に管理しないでください。ローカルの ready は ChatGPT Workspace 登録や実際の遠隔呼出の成功を証明しません。

### 許可するフォルダーを初期化

例のパスを、許可する実際のフォルダーに置き換えます。

```powershell
$authorizedRoot = (Resolve-Path 'C:\replace-with-folder-you-authorize').Path
pwsh -NoLogo -NoProfile -ExecutionPolicy Bypass `
  -File .\scripts\Setup-LocalProbePreview.ps1 `
  -RepoRoot (Get-Location).Path `
  -AuthorizedRoot $authorizedRoot
```

初期化には Git 対象外の `.runtime/` 設定と `.tools\cloudflared.exe` を使います。Cloudflare token は**リポジトリ外の親ディレクトリ**にある `.secrets\cloudflared-tunnel-token.txt` に置きます。token、key、JWT、Cookie、実行設定をコミットしないでください。

Cloudflare の公開 hostname は `http://127.0.0.1:8788` に転送し、`.runtime\cloudflare-access.json` へ issuer、JWKS、audience、JWT subject の対応、同じ `public_hosts` を設定します。接続後のクライアント URL は `https://<public-host>/mcp` です。ファイルアクセス前に `initialize`、`tools/list`、`ping` を確認します。GUI は自身が起動した子プロセスだけを管理します。

旧ネイティブ Preview は [Build-LocalProbePreview.ps1](scripts/Build-LocalProbePreview.ps1) と [Start-LocalProbePreview.ps1](scripts/Start-LocalProbePreview.ps1) に残しています。新規利用では上の Desktop 手順を使ってください。

## アクセスとリリースの境界

利用できるファイル/ツールは profile の roots、tools、deny/ignore、読取専用ポリシーで決まります。任意 shell、任意コマンド、書込、資格情報の表示、モデルによる設定編集は公開していません。バックエンドの存在や設定上の意図は、実行が有効という意味ではありません。

本番 supervisor/service、実 WFP/broker 制御、署名、インストーラー、自動更新、Linux/macOS GUI、標準の大規模索引、百万ファイルでの総合検証、独立した安全審査は提供済みとしていません。ローカル確認や過去の記録でこれらを保証できません。[R11.3 リリースノート](docs/RELEASE_NOTES_R11.3.zh-CN.md) は草案です。

## ガイドと記録

詳細資料の多くは従来の簡体字中国語です。

- [配置](docs/DEPLOYMENT_PREVIEW.zh-CN.md)、[Desktop GUI](docs/DESKTOP_PREVIEW.zh-CN.md)、[ネイティブ Preview](docs/PREVIEW.zh-CN.md)。
- [Cloudflare 設定](docs/CLOUDFLARE_TUNNEL_SETUP.zh-CN.md)、[OpenAI Tunnel 設定](docs/TUNNEL_SETUP.zh-CN.md)。
- [開発](docs/DEVELOPMENT.zh-CN.md)、[構成](docs/ARCHITECTURE.zh-CN.md)、[ツール契約](docs/TOOL_CONTRACTS.md)、[脅威モデル](docs/THREAT_MODEL.md)。
- [実装状況](docs/IMPLEMENTATION_STATUS.md)、[R11 バックエンド進捗](docs/R11_BACKEND_PROGRESS.zh-CN.md)、[リリース確認表](docs/R9_RELEASE_CHECKLIST.zh-CN.md)。
- [全体計画](docs/MASTER_PLAN.zh-CN.md)、[コード構成](.planning/codebase/STRUCTURE.md)。

## 開発とパッケージ

適切に設定した環境で実行する既存の確認コマンドです。

```powershell
go test ./...
go test -race ./...
go vet ./...
go build ./...
git diff --check
```

環境確認は [Test-LocalProbeEnvironment.ps1](scripts/Test-LocalProbeEnvironment.ps1) を使用します。旧ネイティブ Preview の ZIP は次で準備できます。

```powershell
pwsh -NoLogo -NoProfile -ExecutionPolicy Bypass `
  -File .\scripts\Package-LocalProbePreview.ps1 `
  -RepoRoot (Get-Location).Path -Version R9-preview -Build
```

これは手動試験用の署名なしパッケージで、安定版リリースではありません。パッケージ、非 Quick 受け入れ確認、クリーン環境での再ビルド、実際の Windows/Tunnel/ChatGPT 確認は別の条件です。この文書更新で再実行したとは主張しません。配布前に第三者ライセンスと通知を保持・確認してください。
