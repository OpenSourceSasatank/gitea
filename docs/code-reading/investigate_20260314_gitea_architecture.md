# Gitea から学ぶアーキテクチャパターン調査

**調査日**: 2026-03-14
**対象**: [Gitea](https://github.com/go-gitea/gitea)（Go + Vue 3 + HTMX、Git ホスティングプラットフォーム）
**目的**: TCG Match Manager（Go + Flutter + Firestore）に応用可能なアーキテクチャパターンの抽出

---

## 背景

Gitea は Go 製の成熟した大規模 Web アプリケーションであり、Gin と同じ Go エコシステムで REST API ・認証・ CI/CD ・テスト基盤を実践した事例として参考になる。本プロジェクトと言語が共通であるため、設計パターンを直接適用しやすい。

---

## 応用可能なパターン一覧

| # | パターン | 優先度 | 主な適用箇所 |
|---|---------|--------|------------|
| 1 | Swagger/OpenAPI 自動ドキュメント生成 | ⭐️⭐️⭐️ 高 | `routers/api/`、API 仕様管理 |
| 2 | Webhook イベント通知 | ⭐️⭐️⭐️ 高 | トーナメント状態変更通知 |
| 3 | 段階的アクセス制御（AccessMode） | ⭐️⭐️ 中 | Admin/Player 権限拡張 |
| 4 | CI 条件実行（変更ファイルベース） | ⭐️⭐️ 中 | Cloud Build パイプライン最適化 |
| 5 | Graceful Shutdown | ⭐️⭐️ 中 | Cloud Run 上の安全な終了処理 |
| 6 | E2E テスト基盤 | ⭐️ 低 | Flutter 統合テストの改善 |
| 7 | ミドルウェアチェーン構成 | ⭐️ 低 | Gin ルーティング設計の改善 |

---

## パターン詳細

### 1. Swagger/OpenAPI 自動ドキュメント生成

**Gitea の実装:**
Gitea は `routers/api/v1/` に Swagger 定義を統合し、コードのアノテーションから API ドキュメントを自動生成している。エンドポイントの追加・変更時にドキュメントが自動的に更新されるため、仕様と実装の乖離が発生しない。

**参考パス:** [`routers/api/v1/`](../../routers/api/v1/)

**TCG Match Manager への適用:**
現在、API 仕様は `specs/` 配下で手動管理している。Go の Gin フレームワーク向けには `swaggo/gin-swagger` が利用可能で、ハンドラ関数にコメントアノテーションを付与するだけで Swagger UI を自動生成できる。

```go
// @Summary トーナメント作成
// @Description 新しいトーナメントを作成する
// @Tags tournament
// @Accept json
// @Produce json
// @Param body body CreateTournamentRequest true "トーナメント作成リクエスト"
// @Success 201 {object} TournamentResponse
// @Router /api/v1/tournaments [post]
func (h *TournamentHandler) CreateTournament(c *gin.Context) { ... }
```

**導入ステップ:**
1. `go get github.com/swaggo/gin-swagger` で依存追加
2. 既存ハンドラにアノテーションコメントを追加
3. `swag init` でドキュメント生成
4. `/swagger/` エンドポイントで Swagger UI を提供

---

### 2. Webhook イベント通知

**Gitea の実装:**
`services/webhook/` でイベント駆動の Webhook 配信を実装。リポジトリへの push、Issue 作成、PR マージなどのイベントが発生すると、登録済み URL に HTTP POST で通知を送信する。

**参考パス:** [`services/webhook/`](../../services/webhook/)

**TCG Match Manager への適用:**
トーナメント運営において以下のようなイベント通知が有用：

| イベント | 通知先 | 用途 |
|---------|--------|------|
| ラウンド開始 | プレイヤー | 対戦相手の確認促し |
| 結果提出 | 対戦相手 | 承認リクエスト |
| 結果確定 | 管理者 | 次ラウンド開始可能の通知 |
| 最終順位確定 | 全プレイヤー | 結果閲覧 |

**実装方針:**
- Firestore のリアルタイムリスナーと組み合わせてフロントに即時反映
- 将来的には Cloud Functions トリガーで外部通知（LINE、Discord 等）に拡張可能

---

### 3. 段階的アクセス制御（AccessMode）

**Gitea の実装:**
`models/perm/` で `AccessMode` を 5 段階の int enum として定義。`>=` 比較で「この権限以上か」を一発判定できる。3 層構造（AccessMode → Unit 権限 → 公開アクセス）で細粒度の認可を実現。

**参考パス:** [`models/perm/`](../../models/perm/), [`models/perm/access/`](../../models/perm/access/)

**深掘り調査:** [deep_dive_access_mode.md](deep_dive_access_mode.md) — 3 層モデル、権限解決フロー、キャッシュ戦略、8 つの設計パターン抽出

**詳細な読み順ガイド:** [reading_guide_auth.md](reading_guide_auth.md)

**TCG Match Manager への適用:**
現在の Admin/Player 2 系統を `Role` int enum + 宣言的ミドルウェアに統合し、Viewer/Judge 等のロール拡張に備える。詳細は [deep_dive_access_mode.md](deep_dive_access_mode.md) セクション 8 を参照。

---

### 4. CI 条件実行（変更ファイルベース）

**Gitea の実装:**
`.github/workflows/files-changed.yml` で変更ファイルのパスパターンを判定し、該当するテストのみを実行する。バックエンド変更時にフロントエンドテストをスキップするなど、CI 時間を大幅に短縮している。

**参考パス:** [`.github/workflows/files-changed.yml`](../../.github/workflows/files-changed.yml)

**TCG Match Manager への適用:**
Cloud Build で以下のような条件分岐が可能：

| 変更パス | 実行するステップ |
|---------|----------------|
| `backend/**` | Go テスト + ビルド + デプロイ |
| `admin/**`, `player/**` | フロントビルド + デプロイ |
| `docs/**` | ドキュメント検証のみ |
| `firestore.rules` | ルールテスト + デプロイ |

```yaml
# cloudbuild.yaml での条件分岐例
steps:
  - id: 'check-changes'
    name: 'gcr.io/cloud-builders/git'
    entrypoint: 'bash'
    args:
      - '-c'
      - |
        CHANGED=$(git diff --name-only HEAD~1)
        echo "$CHANGED" > /workspace/changed_files.txt
```

---

### 5. Graceful Shutdown

**Gitea の実装:**
`modules/graceful/` でコンテキストベースの 4 段階状態マシン（Init→Running→ShuttingDown→Terminate）によるグレースフル・シャットダウンを実装。シグナル受信時に：
1. ShutdownContext キャンセル → 新規リクエストの受付を停止
2. HammerContext キャンセル → 猶予時間後に残存コネクションを強制切断
3. TerminateContext キャンセル → 終了コールバック実行（DB 切断等）
4. ManagerContext キャンセル → プロセス終了

さらに SIGHUP による FD 継承を使ったゼロダウンタイムリスタートも実装。

**参考パス:** [`modules/graceful/`](../../modules/graceful/)

**深掘り調査:** [reading_guide_graceful_shutdown.md](reading_guide_graceful_shutdown.md) — 11 ファイルに `【読み順 STEP 1-11】` コメント付与、4 つの設計パターン抽出

**TCG Match Manager への適用:**
Cloud Run はインスタンスのスケールダウン時に SIGTERM を送信する。現在の Gin サーバーに以下を追加することで、処理中のリクエスト（特にトランザクション中の結果登録）が途中で切断されることを防げる。

```go
srv := &http.Server{Addr: ":8080", Handler: router}
go srv.ListenAndServe()

quit := make(chan os.Signal, 1)
signal.Notify(quit, syscall.SIGTERM, syscall.SIGINT)
<-quit

ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
defer cancel()
srv.Shutdown(ctx)
```

---

### 6. E2E テスト基盤

**Gitea の実装:**
`tests/e2e/` で Playwright を使用したブラウザベースの E2E テストを実装。GitHub Actions で自動実行され、UI のリグレッションを検知する。テストデータは `tests/testdata/` にフィクスチャとして管理。

**参考パス:** [`tests/e2e/`](../../tests/e2e/), [`.github/workflows/pull-e2e-tests.yml`](../../.github/workflows/pull-e2e-tests.yml)

**TCG Match Manager への適用:**
Flutter フロントエンドの `integration_test/` に相当する。Gitea から参考にできるのは：
- **テストフィクスチャの管理方法**: テスト用データを専用ディレクトリで一元管理
- **CI でのテスト実行**: Firestore Emulator + Flutter Driver の自動実行パイプライン
- **スクリーンショット比較**: Visual Regression Testing の導入

---

### 7. ミドルウェアチェーン構成

**Gitea の実装:**
`modules/web/` で chi/v5 をラップし、BeforeRouting（パス変更可能）と AfterRouting（ルート情報取得可能）の 2 段階ミドルウェアを構成。認証・ CSRF ・ロケール・フラッシュメッセージなどを柔軟に組み合わせている。

**参考パス:** [`modules/web/`](../../modules/web/), [`modules/web/middleware/`](../../modules/web/middleware/)

**TCG Match Manager への適用:**
Gin のミドルウェアグループ機能を活用し、ルートグループごとに適切なミドルウェアを宣言的に構成する。

```go
// 現在の構成をより宣言的に整理
api := router.Group("/api/v1")
{
    // 公開エンドポイント
    public := api.Group("/")
    public.Use(middleware.RateLimit())

    // プレイヤー認証が必要
    player := api.Group("/")
    player.Use(middleware.Auth(), middleware.RequireRole(RolePlayer))

    // 管理者認証が必要
    admin := api.Group("/admin")
    admin.Use(middleware.Auth(), middleware.RequireRole(RoleOrganizer))
}
```

---

## Gitea プロジェクト概要（参考情報）

| 項目 | 内容 |
|------|------|
| 言語 | Go 1.26 |
| フロントエンド | TypeScript, Vue 3, HTMX, Tailwind CSS |
| ルーティング | chi/v5（カスタムラッパー） |
| ORM | Xorm（PostgreSQL, MySQL, SQLite, MSSQL 対応） |
| 認証 | Database, LDAP, OAuth2, SAML, WebAuthn, Reverse Proxy |
| テスト | ユニット + インテグレーション（複数 DB）+ E2E（Playwright） |
| CI/CD | GitHub Actions（条件実行、マルチ DB テスト） |
| デプロイ | Docker（標準 + rootless） |
| ローカルリポジトリ | `/Users/sss/StudioProjects/opensource/gitea` |

## 関連ドキュメント

- [認証・認可 コードリーディングガイド](reading_guide_auth.md) — コード内の `【読み順 STEP N】` コメントに対応する詳細ガイド
- [AccessMode 深掘り調査](deep_dive_access_mode.md) — 3 層権限モデル、権限解決フロー、設計パターン抽出
- [Graceful Shutdown コードリーディングガイド](reading_guide_graceful_shutdown.md) — 4 段階状態マシン、ゼロダウンタイムリスタート、4 つの設計パターン抽出

---

## まとめ

Gitea は Go 製の大規模プロジェクトとして、特に **API ドキュメント自動化**と**イベント通知の仕組み**が TCG Match Manager に直接応用可能である。言語が共通（Go）であるため、コードレベルでの参考がしやすい。段階的アクセス制御や CI 最適化は、プロジェクトの成長に合わせて段階的に導入していくのが望ましい。
