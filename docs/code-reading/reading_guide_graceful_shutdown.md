# Graceful Shutdown コードリーディングガイド

コード内の `【読み順 STEP N】` コメントに対応するガイド。

**調査日**: 2026-03-17
**対象パッケージ**: `modules/graceful/`、`cmd/web.go`

---

## 概要

Gitea の Graceful Shutdown は **コンテキストベースの 4 段階状態マシン** で実装されている。
シグナル受信からプロセス終了まで、以下の順序で進行する：

```
SIGTERM/SIGINT 受信
    ↓
① ShutdownContext キャンセル（新規接続拒否、リスナー Close）
    ↓  ← GracefulHammerTime 待機
② HammerContext キャンセル（残存コネクション強制切断）
    ↓  ← 全サーバー停止待ち + 1 秒
③ TerminateContext キャンセル（終了コールバック実行）
    ↓  ← terminateWaitGroup 完了待ち
④ ManagerContext キャンセル（プロセス終了）
```

---

## 読む順番

| STEP | ファイル | 何がわかるか |
|------|---------|-------------|
| 1 | [`modules/graceful/manager.go`](../../modules/graceful/manager.go) | 状態マシンの 4 状態定義（Init/Running/ShuttingDown/Terminate） |
| 1+ | [`modules/graceful/manager.go`](../../modules/graceful/manager.go) | `doShutdown()` — 停止シーケンスの核心ロジック |
| 2 | [`modules/graceful/context.go`](../../modules/graceful/context.go) | コンテキスト階層（Shutdown→Hammer→Terminate→Manager） |
| 3 | [`modules/graceful/manager_common.go`](../../modules/graceful/manager_common.go) | Manager 構造体の全フィールドとコールバック登録 API |
| 4 | [`modules/graceful/manager_unix.go`](../../modules/graceful/manager_unix.go) | `start()` — Unix での起動処理と systemd 連携 |
| 5 | [`modules/graceful/manager_unix.go`](../../modules/graceful/manager_unix.go) | `handleSignals()` — シグナル→アクション対応表 |
| 6 | [`modules/graceful/server.go`](../../modules/graceful/server.go) | Graceful Server — コネクション追跡、wrappedListener/wrappedConn |
| 7 | [`modules/graceful/server_hooks.go`](../../modules/graceful/server_hooks.go) | サーバーの停止フック — awaitShutdown/doShutdown/doHammer |
| 8 | [`modules/graceful/server_http.go`](../../modules/graceful/server_http.go) | HTTP サーバーファクトリ — BaseContext と KeepAlive 制御 |
| 9 | [`modules/graceful/restart_unix.go`](../../modules/graceful/restart_unix.go) | ゼロダウンタイムリスタート — FD 継承による fork+exec |
| 9+ | [`modules/graceful/net_unix.go`](../../modules/graceful/net_unix.go) | FD 継承の受信側 — 親プロセスのリスナーを復元 |
| 10 | [`modules/graceful/releasereopen/releasereopen.go`](../../modules/graceful/releasereopen/releasereopen.go) | リソース解放/再オープン — SIGUSR1 による logrotate 対応 |
| 11 | [`cmd/web.go`](../../cmd/web.go) | エントリポイント — InitManager() と Done() 待機 |

---

## 処理フロー

### 1. 通常のシャットダウン（SIGTERM/SIGINT）

```
┌──────────────────────────────────────────────────────────┐
│ OS Signal: SIGTERM or SIGINT                              │
└──────────────────┬───────────────────────────────────────┘
                   ▼
┌──────────────────────────────────────────────────────────┐
│ handleSignals() [manager_unix.go:STEP 5]                  │
│   → DoGracefulShutdown()                                  │
└──────────────────┬───────────────────────────────────────┘
                   ▼
┌──────────────────────────────────────────────────────────┐
│ doShutdown() [manager.go:STEP 1+]                         │
│   1. state: Running → ShuttingDown                        │
│   2. shutdownCtxCancel() → 全 ShutdownContext 利用者に通知  │
│   3. toRunAtShutdown コールバックを並行実行                   │
│   4. GracefulHammerTime 後に doHammerTime() をスケジュール    │
└──────────────────┬───────────────────────────────────────┘
                   ▼
┌──────────────────────────────────────────────────────────┐
│ Server 側の反応 [server_hooks.go:STEP 7]                   │
│   awaitShutdown() が IsShutdown() を検知                    │
│   → srv.doShutdown(): リスナーを Close（新規接続拒否）        │
│   → Serve() 内で waitForActiveConnections()（完了待ち）       │
└──────────────────┬───────────────────────────────────────┘
                   ▼
┌──────────────────────────────────────────────────────────┐
│ Hammer Time [manager.go:158-169]                          │
│   GracefulHammerTime 経過後:                                │
│   → hammerCtxCancel() → BaseContext が Done に               │
│   → srv.doHammer(): closeAllConnections() で強制切断         │
└──────────────────┬───────────────────────────────────────┘
                   ▼
┌──────────────────────────────────────────────────────────┐
│ Terminate [manager.go:172-190]                            │
│   runningServerWaitGroup.Wait() 完了後:                     │
│   → doTerminate(): toRunAtTerminate コールバック実行          │
│   → terminateWaitGroup.Wait()                             │
│   → managerCtxCancel() → Done() チャネルが Close             │
└──────────────────┬───────────────────────────────────────┘
                   ▼
┌──────────────────────────────────────────────────────────┐
│ cmd/web.go [STEP 11]                                      │
│   <-graceful.GetManager().Done() が返る → プロセス終了        │
└──────────────────────────────────────────────────────────┘
```

### 2. ゼロダウンタイムリスタート（SIGHUP）

```
┌───────────────────────────────────────────────────────────┐
│ OS Signal: SIGHUP                                          │
└──────────────────┬────────────────────────────────────────┘
                   ▼
┌───────────────────────────────────────────────────────────┐
│ DoGracefulRestart() [manager_unix.go:181-201]              │
│   → doFork() → RestartProcess() [restart_unix.go:STEP 9]  │
└──────────────────┬────────────────────────────────────────┘
                   ▼
┌───────────────────────────────────────────────────────────┐
│ RestartProcess()                                           │
│   1. アクティブリスナーから FD を抽出                           │
│   2. 環境変数 LISTEN_FDS=N をセット                           │
│   3. os.StartProcess() で新プロセスを起動                      │
│      (stdin, stdout, stderr + リスナー FD を継承)             │
└──────────────────┬────────────────────────────────────────┘
                   ▼
┌───────────────────────────────────────────────────────────┐
│ 新プロセス起動                                               │
│   getProvidedFDs() [net_unix.go:STEP 9+]                   │
│   → LISTEN_FDS から FD を復元 → リスナーとして使用              │
│   → isChild = true                                         │
│   → RegisterServer() 時に KillParent() で旧プロセスに SIGTERM │
└──────────────────┬────────────────────────────────────────┘
                   ▼
┌───────────────────────────────────────────────────────────┐
│ 旧プロセス: 通常のシャットダウンシーケンスを実行                   │
│ 新プロセス: 同じポートで即座にリクエスト受付開始                    │
│ → ダウンタイムなし                                            │
└───────────────────────────────────────────────────────────┘
```

---

## 設計パターン

### パターン 1: Context 階層による段階的停止

**問題:** 複数のコンポーネントを安全な順序で停止したい

**解法:** 4 つの入れ子コンテキストを順にキャンセルすることで、「通知 → 猶予 → 強制 → 完了」の段階を実現。
各コンポーネントは自分に適したコンテキストを `select` で監視するだけで、停止シーケンスに自動的に参加できる。

```go
// 利用側はコンテキストを監視するだけ
func (s *MyService) Run() {
    ctx := graceful.GetManager().ShutdownContext()
    for {
        select {
        case <-ctx.Done():
            // クリーンアップ処理
            return
        case task := <-s.taskCh:
            s.process(task)
        }
    }
}
```

**利点:**
- 各コンポーネントが停止ロジックを個別に持つ必要がない
- `context.Context` の標準パターンなので Go 開発者に馴染みやすい
- タイムアウト（HammerTime）による暴走防止が組み込み

**OSS 実例:** [`modules/graceful/context.go`](../../modules/graceful/context.go), [`modules/graceful/manager.go`](../../modules/graceful/manager.go)

---

### パターン 2: FD 継承によるゼロダウンタイムリスタート

**問題:** バイナリ更新時にリクエストを落としたくない

**解法:** リスニングソケットのファイルディスクリプタ（FD）を子プロセスに継承させる。
旧プロセスは新規接続の受付を停止し、既存接続の処理完了後に終了する。

```
旧プロセス                    新プロセス
───────────                  ──────────
リスナー FD を抽出
  ↓
LISTEN_FDS=N で子プロセス起動 → FD からリスナーを復元
  ↓                            ↓
新規接続を拒否                 新規接続を受付開始
  ↓                            ↓
既存接続の完了待ち              KillParent() → 旧プロセスに SIGTERM
  ↓
プロセス終了
```

**利点:**
- クライアントから見てダウンタイムがゼロ
- ロードバランサーなしで実現可能
- systemd のソケットアクティベーションとも互換

**OSS 実例:** [`modules/graceful/restart_unix.go`](../../modules/graceful/restart_unix.go), [`modules/graceful/net_unix.go`](../../modules/graceful/net_unix.go)

---

### パターン 3: コネクション追跡付きリスナーラッパー

**問題:** 「全リクエスト完了まで待つ」を実現したい

**解法:** `net.Listener` と `net.Conn` をラップし、Accept 時にカウンタ増加、Close 時に減少。
カウンタがゼロになるまで `sync.Cond` で待機する。

```go
// Accept → カウンタ++
func (wl *wrappedListener) Accept() (net.Conn, error) {
    c, err := wl.Listener.Accept()
    return wl.server.wrapConnection(c)  // connCounter++
}

// Close → カウンタ--
func (w *wrappedConn) Close() error {
    w.server.removeConnection(w)  // connCounter--
    return w.Conn.Close()
}

// 全コネクション完了待ち
func (srv *Server) waitForActiveConnections() {
    srv.lock.Lock()
    for srv.connCounter > 0 {
        srv.connEmptyCond.Wait()
    }
    srv.lock.Unlock()
}
```

**利点:**
- HTTP に限らず任意のネットワークプロトコルに適用可能（SSH 等）
- `sync.Cond` によるイベント駆動で CPU を浪費しない
- Hammer 時は `closeAllConnections()` でカウンタをリセットして即座に通過

**OSS 実例:** [`modules/graceful/server.go`](../../modules/graceful/server.go)

---

### パターン 4: 事前定義サーバー数による起動同期

**問題:** 全サーバーの起動完了を確認してから systemd に READY を通知したい

**解法:** `numberOfServersToCreate` 定数で起動予定サーバー数を宣言。
各サーバーの起動（または不使用の通知）時に `InformCleanup()` でカウントアップ。
全数到達で `READY=1` を送信する。
使用しないサーバーは `NoHTTPRedirector()` / `NoInstallListener()` で明示的に通知する。

```go
const numberOfServersToCreate = 4  // HTTP, HTTPS/Install, Redirector, SSH

// サーバー起動時
func DefaultGetListener(...) {
    defer GetManager().InformCleanup()  // カウンタ++
    ...
}

// サーバー不使用時
func NoHTTPRedirector() {
    graceful.GetManager().InformCleanup()  // カウンタ++
}
```

**利点:**
- systemd の Type=notify と連携し、起動完了を正確に報告
- 未使用リスナーのリーク防止

**OSS 実例:** [`modules/graceful/manager.go:41`](../../modules/graceful/manager.go), [`cmd/web_graceful.go`](../../cmd/web_graceful.go)

---

## シグナル一覧

| シグナル | アクション | 関数 |
|---------|----------|------|
| `SIGHUP` | Graceful Restart（fork+exec） | `DoGracefulRestart()` |
| `SIGINT` | Graceful Shutdown | `DoGracefulShutdown()` |
| `SIGTERM` | Graceful Shutdown | `DoGracefulShutdown()` |
| `SIGUSR1` | ログファイル再オープン | `releasereopen.GetManager().ReleaseReopen()` |
| `SIGUSR2` | Immediate Hammer（強制停止） | `DoImmediateHammer()` |
| `SIGTSTP` | ログ記録のみ | — |

---

## コンテキスト使い分けガイド

| コンテキスト | 用途 | 取得方法 |
|------------|------|---------|
| `ShutdownContext` | バックグラウンドタスク、キューワーカー | `GetManager().ShutdownContext()` |
| `HammerContext` | HTTP リクエストの BaseContext、process.DefaultContext | `GetManager().HammerContext()` |
| `TerminateContext` | 最終クリーンアップ（DB 切断等） | `GetManager().TerminateContext()` |
| `Manager（Done）` | プロセス終了の待機 | `<-GetManager().Done()` |

---

## 利用者側の API

| API | 用途 | 例 |
|-----|------|-----|
| `RunWithShutdownContext(func(ctx))` | ShutdownContext で動くゴルーチン | Cron、EventSource |
| `RunWithCancel(RunCanceler)` | Cancel() で停止できるサービス | Webhook 配信キュー |
| `RunAtShutdown(ctx, func())` | シャットダウン時に実行するコールバック | スケジューラ停止 |
| `RunAtTerminate(func())` | 終了時に実行するコールバック | リソース解放 |
| `RegisterServer()` / `ServerDone()` | サーバーの生存登録 | HTTP/SSH サーバー |
| `InformCleanup()` | サーバー起動/不使用の通知 | 起動同期 |

---

## 関連設定

| 設定キー | デフォルト | 説明 |
|---------|----------|------|
| `GracefulRestartable` | `true`（Linux） | SIGHUP での Graceful Restart を有効にする |
| `GracefulHammerTime` | `60s` | Shutdown → Hammer までの猶予時間 |
| `StartupTimeout` | `0`（無制限） | 起動タイムアウト |
| `PerWriteTimeout` | — | 書き込み操作ごとのタイムアウト |
| `PerWritePerKbTimeout` | — | KB あたりの追加タイムアウト |

---

## Windows との差異

| 機能 | Unix | Windows |
|------|------|---------|
| シグナルハンドリング | POSIX シグナル | Windows Service API |
| Graceful Restart | fork+exec + FD 継承 | 非対応 |
| systemd 連携 | NOTIFY_SOCKET, WATCHDOG | 非対応 |
| 実装ファイル | `manager_unix.go`, `net_unix.go`, `restart_unix.go` | `manager_windows.go`, `net_windows.go` |

---

## ファイル一覧

| ファイル | 行数 | 役割 |
|---------|------|------|
| [`modules/graceful/manager.go`](../../modules/graceful/manager.go) | 257 | 状態マシン、doShutdown/doHammerTime/doTerminate |
| [`modules/graceful/manager_common.go`](../../modules/graceful/manager_common.go) | 111 | Manager 構造体定義、公開 API |
| [`modules/graceful/manager_unix.go`](../../modules/graceful/manager_unix.go) | 202 | Unix シグナルハンドリング、systemd 通知 |
| [`modules/graceful/manager_windows.go`](../../modules/graceful/manager_windows.go) | 190 | Windows Service 統合 |
| [`modules/graceful/context.go`](../../modules/graceful/context.go) | 37 | コンテキストアクセサ |
| [`modules/graceful/server.go`](../../modules/graceful/server.go) | 294 | コネクション追跡付きサーバー |
| [`modules/graceful/server_hooks.go`](../../modules/graceful/server_hooks.go) | 55 | サーバー停止フック |
| [`modules/graceful/server_http.go`](../../modules/graceful/server_http.go) | 38 | HTTP サーバーファクトリ |
| [`modules/graceful/net_unix.go`](../../modules/graceful/net_unix.go) | 322 | FD 継承、リスナー生成 |
| [`modules/graceful/net_windows.go`](../../modules/graceful/net_windows.go) | 20 | Windows リスナー |
| [`modules/graceful/restart_unix.go`](../../modules/graceful/restart_unix.go) | 116 | ゼロダウンタイムリスタート |
| [`modules/graceful/releasereopen/releasereopen.go`](../../modules/graceful/releasereopen/releasereopen.go) | 62 | ログ再オープン管理 |
| [`cmd/web.go`](../../cmd/web.go) | 382 | エントリポイント |
| [`cmd/web_graceful.go`](../../cmd/web_graceful.go) | 50 | HTTP/FCGI サーバーラッパー |
