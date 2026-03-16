# オープンソースコードの読み方 - Gitea「Issue作成」を題材に

調査日: 2026-03-16

## はじめに: なぜ「Issue作成」か

OSSのコードを読むとき、**具体的な1つの機能のリクエスト→レスポンスの流れを端から端まで追う**のが最も効果的な方法である。「Issue作成」は以下の理由で最適：

- 全レイヤー（ルーティング→ハンドラ→サービス→モデル→DB）を横断する
- 副作用（通知、Webhook、インデックス更新）がある
- 誰でも知っている機能で、動作のイメージがつきやすい
- 複雑すぎず、簡単すぎない

---

## Step 0: 読み方の大原則

OSSを読むときに守るべき3つの原則：

### 原則1: エントリーポイントから逆引きせよ

コードを「上から順に」読まない。**ユーザーの操作**から逆引きする。

```
ユーザーの操作 = 「Issue作成ボタンを押す」
→ HTTPリクエスト = POST /{owner}/{repo}/issues/new
→ ルーティング定義を探す
→ ハンドラ関数を見つける
→ そこから呼ばれる関数を芋づる式に追う
```

### 原則2: 全部読まない。必要な関数だけ深掘りせよ

1つのファイルが500行あっても、今追いたい関数だけ読む。周辺のヘルパー関数は**名前だけ見て推測**し、必要になったら読む。

### 原則3: レイヤーを意識せよ

大規模OSSには必ずレイヤー構造がある。今どの層にいるか常に意識する。

```
routers/ (HTTPの入口)  →  services/ (ビジネスロジック)  →  models/ (DB操作)
```

---

## Step 1: ルーティングを探す（入口を見つける）

### 方法: URLパスでgrepする

ユーザーが Issue を作成するとき、フォームは `POST /{owner}/{repo}/issues/new` に送信される。
このURLパスをルーティング定義から探す。

**探し方:**
```bash
grep -rn "issues/new" routers/web/web.go
```

**発見:**
```
routers/web/web.go:1288-1290
```

```go
m.Group("/issues", func() {
    m.Group("/new", func() {
        m.Combo("").Get(repo.NewIssue).
            Post(web.Bind(forms.CreateIssueForm{}), repo.NewIssuePost)
    })
})
```

### ここから読み取れること

| 要素 | 意味 |
|------|------|
| `m.Group("/issues", ...)` | `/issues` 以下のルートグループ |
| `m.Combo("").Get(...).Post(...)` | 同一パスに GET と POST を定義 |
| `web.Bind(forms.CreateIssueForm{})` | **リクエストボディをフォーム構造体にバインド** するミドルウェア |
| `repo.NewIssue` | GET ハンドラ = フォーム表示 |
| `repo.NewIssuePost` | POST ハンドラ = Issue作成処理 |

### 読み方のコツ: ルーティングファイルは「地図」

`routers/web/web.go` はアプリケーション全体の URL マップ。**最初に読むべきファイル**。
ここを読めば、どんな機能がどこにあるかが分かる。

---

## Step 2: フォーム構造体を確認する（入力を理解する）

ハンドラに渡されるデータの形を知る。

**ファイル:** `services/forms/repo_form.go:410-421`

```go
type CreateIssueForm struct {
    Title               string `binding:"Required;MaxSize(255)"`
    LabelIDs            string `form:"label_ids"`
    AssigneeIDs         string `form:"assignee_ids"`
    ReviewerIDs         string `form:"reviewer_ids"`
    Ref                 string `form:"ref"`
    MilestoneID         int64
    ProjectID           int64
    Content             string
    Files               []string
    AllowMaintainerEdit bool
}
```

### 読み方のコツ: 構造体タグが仕様書

- `binding:"Required;MaxSize(255)"` → タイトルは必須、最大255文字
- `form:"label_ids"` → HTMLフォームの `name="label_ids"` と対応
- `int64` → IDは数値

**「入力が何か」を最初に理解すると、後続のコードが格段に読みやすくなる。**

---

## Step 3: HTTPハンドラを読む（処理の全体像を掴む）

**ファイル:** `routers/web/repo/issue_new.go:327-412`

```go
func NewIssuePost(ctx *context.Context) {
    // 1. フォームデータ取得
    form := web.GetForm(ctx).(*forms.CreateIssueForm)

    // 2. バリデーション（ラベル、マイルストーン、アサイニー、プロジェクトの存在確認）
    validateRet := ValidateRepoMetasForNewIssue(ctx, *form, false)

    // 3. 空タイトルチェック
    if util.IsEmptyString(form.Title) {
        ctx.JSONError(ctx.Tr("repo.issues.new.title_empty"))
        return
    }

    // 4. テンプレートからコンテンツ生成（テンプレート利用時）
    content := form.Content
    if filename := ctx.Req.Form.Get("template-file"); filename != "" {
        if template, err := issue_template.UnmarshalFromRepo(...); err == nil {
            content = issue_template.RenderToMarkdown(template, ctx.Req.Form)
        }
    }

    // 5. Issue構造体を組み立て
    issue := &issues_model.Issue{
        RepoID:      repo.ID,
        Repo:        repo,
        Title:       form.Title,
        PosterID:    ctx.Doer.ID,
        Poster:      ctx.Doer,
        MilestoneID: milestoneID,
        Content:     content,
        Ref:         form.Ref,
    }

    // 6. ★ サービス層に委譲（ここが核心）
    if err := issue_service.NewIssue(ctx, repo, issue, labelIDs, attachments, assigneeIDs, projectID); err != nil {
        // エラーハンドリング...
        return
    }

    // 7. レスポンス（リダイレクト）
    ctx.JSONRedirect(issue.Link())
}
```

### 読み方のコツ: ハンドラの役割を理解する

ハンドラは「**交通整理役**」であり、自分ではビジネスロジックを持たない：

1. 入力の取得・バリデーション
2. サービス層への委譲
3. 結果に応じたレスポンス

**サービス呼び出しの1行** (`issue_service.NewIssue(...)`) が全ての核心。
この1行を見つけたら、次のステップでその中身を読む。

---

## Step 4: サービス層を読む（ビジネスロジックの核心）

**ファイル:** `services/issue/issue.go:27-69`

```go
func NewIssue(ctx context.Context, repo *repo_model.Repository, issue *issues_model.Issue,
    labelIDs []int64, uuids []string, assigneeIDs []int64, projectID int64) error {

    // 1. ブロックユーザーチェック
    if user_model.IsUserBlockedBy(ctx, issue.Poster, repo.OwnerID) ||
       user_model.IsUserBlockedBy(ctx, issue.Poster, assigneeIDs...) {
        return user_model.ErrBlockedUser
    }

    // 2. ★ トランザクション内で一括実行
    if err := db.WithTx(ctx, func(ctx context.Context) error {
        // 2a. Issue本体をDBに保存
        if err := issues_model.NewIssue(ctx, repo, issue, labelIDs, uuids); err != nil {
            return err
        }
        // 2b. アサイニーを追加
        for _, assigneeID := range assigneeIDs {
            if _, err := AddAssigneeIfNotAssigned(ctx, issue, issue.Poster, assigneeID, true); err != nil {
                return err
            }
        }
        // 2c. プロジェクトに関連付け
        if projectID > 0 {
            if err := issues_model.IssueAssignOrRemoveProject(ctx, issue, issue.Poster, projectID, 0); err != nil {
                return err
            }
        }
        return nil
    }); err != nil {
        return err
    }

    // 3. メンション処理（トランザクション外）
    mentions, err := issues_model.FindAndUpdateIssueMentions(ctx, issue, issue.Poster, issue.Content)

    // 4. ★★ 通知を発火（副作用のトリガー）
    notify_service.NewIssue(ctx, issue, mentions)
    if len(issue.Labels) > 0 {
        notify_service.IssueChangeLabels(ctx, issue.Poster, issue, issue.Labels, nil)
    }
    if issue.Milestone != nil {
        notify_service.IssueChangeMilestone(ctx, issue.Poster, issue, 0)
    }

    return nil
}
```

### 読み方のコツ: トランザクション境界に注目する

`db.WithTx()` の中と外を区別して読む：
- **中**: DB操作（失敗したら全てロールバック）
- **外**: 副作用（通知、Webhook。DBコミット成功後に実行）

この区分は「なぜこの順序なのか？」を理解するカギになる。

---

## Step 5: モデル層を読む（DBに何が書き込まれるか）

**ファイル:** `models/issues/issue_update.go:428-451`

```go
func NewIssue(ctx context.Context, repo *repo_model.Repository, issue *Issue,
    labelIDs []int64, uuids []string) (err error) {
    return db.WithTx(ctx, func(ctx context.Context) error {
        // 1. リポジトリ内の連番インデックスを採番
        idx, err := db.GetNextResourceIndex(ctx, "issue_index", repo.ID)
        issue.Index = idx

        // 2. タイトルを255文字に切り詰め
        issue.Title = util.EllipsisDisplayString(issue.Title, 255)

        // 3. ★ 実際のDB INSERT
        return NewIssueWithIndex(ctx, issue.Poster, NewIssueOptions{
            Repo:        repo,
            Issue:       issue,
            LabelIDs:    labelIDs,
            Attachments: uuids,
        })
    })
}
```

**`NewIssueWithIndex`** (`models/issues/issue_update.go:335-424`) でさらに詳細な処理：

```go
func NewIssueWithIndex(ctx context.Context, doer *user_model.User, opts NewIssueOptions) (err error) {
    e := db.GetEngine(ctx)

    // 1. マイルストーンの存在確認
    // 2. ★ Issue レコードを INSERT
    if _, err := e.Insert(opts.Issue); err != nil { return err }

    // 3. マイルストーンカウンター更新 + コメント作成
    // 4. リポジトリの Issue 数カウンター更新
    if err := IncrRepoIssueNumbers(ctx, ...); err != nil { return err }

    // 5. ラベルの紐付け
    for _, label := range labels {
        if err = newIssueLabel(ctx, opts.Issue, label, ...); err != nil { return err }
    }

    // 6. Issue 購読ユーザーの登録
    if err = NewIssueUsers(ctx, opts.Repo, opts.Issue); err != nil { return err }

    // 7. 添付ファイルの紐付け
    if err := UpdateIssueAttachments(ctx, opts.Issue.ID, opts.Attachments); err != nil { return err }

    // 8. クロスリファレンス解析（他のIssueへの参照）
    return opts.Issue.AddCrossReferences(ctx, doer, false)
}
```

### 読み方のコツ: モデル層は「何がDBに入るか」だけに集中する

モデル層の関数は長くなりがちだが、やっていることは：
1. **INSERT** (メインレコード)
2. **関連テーブルの更新** (ラベル、マイルストーン、ユーザー)
3. **カウンターの更新** (リポジトリの統計)

これらの操作を分類しながら読むと見通しが良くなる。

### Issue 構造体（テーブル定義）

**ファイル:** `models/issues/issue.go:69-99`

```go
type Issue struct {
    ID                int64                  `xorm:"pk autoincr"`
    RepoID            int64                  `xorm:"INDEX UNIQUE(repo_index)"`
    Index             int64                  `xorm:"UNIQUE(repo_index)"`  // リポジトリ内連番 (#1, #2, ...)
    PosterID          int64                  `xorm:"INDEX"`
    Title             string                 `xorm:"name"`
    Content           string                 `xorm:"LONGTEXT"`
    MilestoneID       int64                  `xorm:"INDEX"`
    IsClosed          bool                   `xorm:"INDEX"`
    IsPull            bool                   `xorm:"INDEX"`
    NumComments       int
    Ref               string
    // ... (xorm:"-" のフィールドはDBに保存されないメモリ上の関連データ)
}
```

### 読み方のコツ: xorm タグが DB スキーマ

- `xorm:"pk autoincr"` → PRIMARY KEY AUTO INCREMENT
- `xorm:"INDEX"` → インデックスあり
- `xorm:"UNIQUE(repo_index)"` → 複合ユニーク制約
- `xorm:"-"` → **DBには保存されない**（コード内でのみ使う関連オブジェクト）

`xorm:"-"` のフィールドは Lazy Load パターン。必要になったら `LoadRepo()`, `LoadPoster()` 等で読み込む。

---

## Step 6: 副作用を追う（通知・Webhook・インデックス）

サービス層の最後で `notify_service.NewIssue()` が呼ばれる。

**ファイル:** `services/notify/notify.go:74-78`

```go
func NewIssue(ctx context.Context, issue *issues_model.Issue, mentions []*user_model.User) {
    for _, notifier := range notifiers {
        notifier.NewIssue(ctx, issue, mentions)
    }
}
```

### Notifier パターン（Observer パターン）

`notifiers` はアプリ起動時に登録された複数の Notifier 実装のスライス。

**登録されている Notifier 一覧:**

| Notifier | ファイル | 役割 |
|----------|----------|------|
| `feed.Notifier` | `services/feed/notifier.go` | アクティビティフィードに記録 |
| `mailer.Notifier` | `services/mailer/mailer.go` | メール通知を送信 |
| `uinotification.Notifier` | `services/uinotification/notify.go` | Web UI の通知ベルに表示 |
| `indexer.Notifier` | `services/indexer/indexer.go` | 検索インデックスを更新 |
| `webhook.Notifier` | `services/webhook/notifier.go` | 外部 Webhook を発火 |
| `actions.Notifier` | `services/actions/init.go` | GitHub Actions 互換のワークフロー起動 |
| `mirror.Notifier` | `services/mirror/notifier.go` | ミラーリポジトリの同期 |
| `automerge.Notifier` | `services/automerge/automerge.go` | 自動マージチェック |

### Webhook Notifier の例

**ファイル:** `services/webhook/notifier.go:275-290`

```go
func (m *webhookNotifier) NewIssue(ctx context.Context, issue *issues_model.Issue, mentions []*user_model.User) {
    issue.LoadRepo(ctx)
    issue.LoadPoster(ctx)

    permission, _ := access_model.GetUserRepoPermission(ctx, issue.Repo, issue.Poster)
    PrepareWebhooks(ctx, EventSource{Repository: issue.Repo}, webhook_module.HookEventIssues, &api.IssuePayload{
        Action:     api.HookIssueOpened,
        Index:      issue.Index,
        Issue:      convert.ToAPIIssue(ctx, issue.Poster, issue),
        Repository: convert.ToRepo(ctx, issue.Repo, permission),
        Sender:     convert.ToUser(ctx, issue.Poster, nil),
    })
}
```

### 読み方のコツ: 副作用は「名前から推測→必要なら深掘り」

8つの Notifier 全部を最初から読む必要はない。
名前から役割を推測し、興味のあるもの（例: Webhook）だけ深掘りする。

---

## Step 7: フロントエンドとの接続を理解する

### テンプレート

**ファイル:** `templates/repo/issue/new.tmpl`

```html
{{template "base/head" .}}
<div class="page-content repository new issue">
    {{template "repo/header" .}}
    <div class="ui container">
        {{template "repo/issue/new_form" .}}
    </div>
</div>
{{template "base/footer" .}}
```

**ファイル:** `templates/repo/issue/new_form.tmpl`

```html
<form class="issue-content ui comment form form-fetch-action"
      id="new-issue" action="{{.Link}}" method="post">
    <!-- タイトル入力 -->
    <input name="title" required maxlength="255" ...>

    <!-- コンテンツ入力（Markdownエディタ） -->
    {{template "repo/issue/comment_tab" .}}

    <!-- 送信ボタン -->
    <button class="ui primary button">
        {{ctx.Locale.Tr "repo.issues.create"}}
    </button>

    <!-- サイドバー（ラベル、マイルストーン、プロジェクト、アサイニー） -->
    {{template "repo/issue/sidebar/label_list" ...}}
    {{template "repo/issue/sidebar/milestone_list" ...}}
    {{template "repo/issue/sidebar/project_list" ...}}
    {{template "repo/issue/sidebar/assignee_list" ...}}
</form>
```

### 重要: `form-fetch-action` クラス

このフォームには `class="form-fetch-action"` がついている。
これは Gitea の JS が fetch API でフォームを送信する仕組み。
通常のフォーム送信ではなく、**非同期POST → JSONレスポンス → リダイレクト** となる。

ハンドラ側の `ctx.JSONRedirect(issue.Link())` と対応する。

### データの流れ

```
[ブラウザ]
    ↓ form-fetch-action が fetch() で POST
[Go: ミドルウェア]
    ↓ web.Bind() がフォームデータ → CreateIssueForm にマッピング
[Go: NewIssuePost ハンドラ]
    ↓ バリデーション → issue_service.NewIssue() 呼び出し
[Go: Service層]
    ↓ トランザクション内でDB操作 → 通知発火
[Go: Model層]
    ↓ xorm で INSERT
[DB]
    ↓ 保存完了
[Go: ハンドラ]
    ↓ ctx.JSONRedirect(issue.Link())
[ブラウザ]
    → /owner/repo/issues/123 にリダイレクト
```

---

## まとめ: OSS を読むための実践的手順

### 1. 地図を手に入れる

- `routers/web/web.go` (URLルーティング) を最初にざっと眺める
- ディレクトリ構造から層を把握する (`routers/`, `services/`, `models/`)

### 2. 1つの機能を端から端まで追う

```
URL → ルーティング → ハンドラ → サービス → モデル → DB
                                    ↓
                              副作用 (通知, Webhook)
```

### 3. 各ステップで「探し方」を覚える

| やりたいこと | 探し方 |
|------------|--------|
| URLからハンドラを見つける | `grep "URLパス" routers/web/web.go` |
| ハンドラからサービスを見つける | ハンドラ内の `xxx_service.関数名()` を追う |
| サービスからモデルを見つける | サービス内の `xxx_model.関数名()` を追う |
| 副作用を見つける | `notify_service.XXX()` を追う |
| DBスキーマを知る | モデル構造体の `xorm` タグを読む |
| フォームの入力を知る | `forms.XXXForm` 構造体を見る |
| テンプレートを見つける | ハンドラの `ctx.HTML(status, "テンプレート名")` |

### 4. 深さを調整する

- **浅く広く**: ルーティングファイル全体を眺めて機能一覧を把握
- **狭く深く**: 1つの関数を呼び出しチェーンの最後まで追う
- **必要に応じて**: 副作用やエッジケースは最初は飛ばす

### 5. パターンを見抜く

Gitea で繰り返し現れるパターン：

| パターン | 例 |
|---------|---|
| ハンドラ = 薄い。サービスに委譲 | `NewIssuePost` → `issue_service.NewIssue` |
| トランザクション = `db.WithTx()` | サービス層でトランザクション管理 |
| 遅延ロード = `xorm:"-"` + `LoadXXX()` | `issue.LoadRepo()`, `issue.LoadPoster()` |
| Observer = Notifier インターフェース | `notify_service.NewIssue()` → 8つの実装に通知 |
| フォームバインド = ミドルウェア | `web.Bind(forms.CreateIssueForm{})` |

**これらのパターンを1つの機能で理解すれば、他の全ての機能（PR作成、コメント追加、リリース作成...）にも同じ読み方が適用できる。**

---

## 読んだファイル一覧（読む順序）

| 順序 | ファイル | 行 | 目的 |
|------|----------|-----|------|
| 1 | `routers/web/web.go` | 1288-1290 | ルーティング定義 |
| 2 | `services/forms/repo_form.go` | 410-421 | フォーム構造体 |
| 3 | `routers/web/repo/issue_new.go` | 327-412 | HTTPハンドラ |
| 4 | `services/issue/issue.go` | 27-69 | サービス層 |
| 5 | `models/issues/issue_update.go` | 428-451 | モデル層（NewIssue） |
| 6 | `models/issues/issue_update.go` | 335-424 | モデル層（NewIssueWithIndex） |
| 7 | `models/issues/issue.go` | 69-99 | Issue構造体定義 |
| 8 | `services/notify/notify.go` | 74-78 | 通知ディスパッチ |
| 9 | `services/webhook/notifier.go` | 275-290 | Webhook通知の実装例 |
| 10 | `templates/repo/issue/new.tmpl` | 全体 | テンプレート |
| 11 | `templates/repo/issue/new_form.tmpl` | 1-60 | フォームテンプレート |

**この11ファイルだけで、Issue作成の全フローが理解できる。**
