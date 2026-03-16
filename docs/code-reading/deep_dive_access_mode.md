# Gitea 段階的アクセス制御（AccessMode）深掘り調査

**調査日**: 2026-03-14
**対象**: Gitea の権限モデル全体（AccessMode → Unit 権限 → 認可ミドルウェア）
**前提知識**: [reading_guide_auth.md](reading_guide_auth.md) の STEP 1〜9
**親ドキュメント**: [investigate_20260314_gitea_architecture.md](investigate_20260314_gitea_architecture.md) パターン #3

---

## 1. アーキテクチャ全体像: 3 層の権限モデル

Gitea の権限システムは **3 つの層** で構成されている:

```
┌─────────────────────────────────────────────────┐
│ 層 1: AccessMode（リポジトリ全体の権限）           │
│   int 型の 5 段階: None < Read < Write < Admin < Owner │
│   → >= 比較で「この権限以上か？」を一発判定           │
├─────────────────────────────────────────────────┤
│ 層 2: Unit 単位の権限（unitsMode マップ）           │
│   Code, Issues, PRs, Wiki, Releases, Actions 等     │
│   → Unit ごとに異なる AccessMode を設定可能          │
│   → チームに「Code は Write、Issues は Read」が可能    │
├─────────────────────────────────────────────────┤
│ 層 3: 公開アクセス権限（anonymous/everyone）         │
│   → Unit ごとに匿名・ログインユーザーへの最低権限を設定│
│   → ForcePrivate 設定で一括無効化可能                │
└─────────────────────────────────────────────────┘
```

**関連ファイル:**

| 層 | ファイル | 役割 |
|---|---------|------|
| 1 | [`models/perm/access_mode.go`](../../models/perm/access_mode.go) | AccessMode の 5 段階定義 |
| 2 | [`models/perm/access/repo_permission.go`](../../models/perm/access/repo_permission.go) | Permission 構造体（3 層を集約） |
| 2 | [`models/organization/team.go`](../../models/organization/team.go) | チーム × Unit 権限 |
| 2 | [`models/unit/unit.go`](../../models/unit/unit.go) | Unit 型の定義 |
| 3 | [`models/perm/access/repo_permission.go`](../../models/perm/access/repo_permission.go) | anonymous/everyone アクセス |
| - | [`models/perm/access/access.go`](../../models/perm/access/access.go) | 権限キャッシュテーブル |
| - | [`services/context/permission.go`](../../services/context/permission.go) | 認可ミドルウェア |

---

## 2. 層 1: AccessMode — なぜ int 型なのか

### 定義

```go
// models/perm/access_mode.go — ソース: ../../models/perm/access_mode.go
type AccessMode int
const (
    AccessModeNone  AccessMode = iota // 0: アクセス不可
    AccessModeRead                     // 1: 読み取り
    AccessModeWrite                    // 2: 書き込み
    AccessModeAdmin                    // 3: 管理者
    AccessModeOwner                    // 4: オーナー
)
```

> ソース: [`models/perm/access_mode.go`](../../models/perm/access_mode.go)

### int 型の設計意図

**数値比較の威力:**

```go
// ✅ Gitea のアプローチ: int 比較
func (p *Permission) IsAdmin() bool {
    return p.AccessMode >= perm_model.AccessModeAdmin  // Admin(3) も Owner(4) も true
}
func (p *Permission) CanAccess(mode AccessMode, unitType unit.Type) bool {
    return p.UnitAccessMode(unitType) >= mode
}

// ❌ もし文字列 enum だったら
func (p *Permission) CanAccess(required string, unitType unit.Type) bool {
    levels := map[string]int{"none": 0, "read": 1, "write": 2, "admin": 3, "owner": 4}
    return levels[p.mode] >= levels[required]  // 毎回マップ参照が必要
}
```

**3 つのメリット:**

1. **`>=` 一発判定** — Admin チェックで Owner も自動的に通る。条件分岐が不要
2. **拡張に強い** — 新しい段階を追加しても、既存の `>=` 比較コードに変更不要
3. **`max()` で集計** — 複数チーム所属時の権限合算が `max(modeA, modeB)` だけ

### ParseAccessMode: 入力の安全な変換

```go
func ParseAccessMode(permission string, allowed ...AccessMode) AccessMode {
    m := AccessModeNone
    switch permission {
    case "read":  m = AccessModeRead
    case "write": m = AccessModeWrite
    case "admin": m = AccessModeAdmin
    // "owner" は意図的に解析しない（内部判定専用）
    }
    if len(allowed) == 0 { return m }
    return util.Iif(slices.Contains(allowed, m), m, AccessModeNone)
}
```

**設計判断:** `owner` をユーザー入力から受け付けない。Owner はシステムが内部的に判定するもの（リポオーナー、サイト管理者等）であり、外部から指定させない。

---

## 3. 層 2: Permission 構造体と Unit 権限

### Permission 構造体

ソース: [`models/perm/access/repo_permission.go`](../../models/perm/access/repo_permission.go)

```go
type Permission struct {
    AccessMode perm_model.AccessMode                    // リポジトリ全体の権限
    units      []*repo_model.RepoUnit                   // 有効な Unit のリスト
    unitsMode  map[unit.Type]perm_model.AccessMode      // Unit 別の権限（チーム経由）
    everyoneAccessMode  map[unit.Type]perm_model.AccessMode  // ログインユーザーの最低権限
    anonymousAccessMode map[unit.Type]perm_model.AccessMode  // 匿名ユーザーの最低権限
}
```

**重要: `unitsMode` が nil か非 nil かで権限判定の挙動が変わる。**

- `unitsMode == nil`: 全 Unit に `AccessMode` がそのまま適用される（個人リポのコラボレータ等）
- `unitsMode != nil`: Unit ごとに個別の権限を持つ（組織チーム経由のアクセス）

### UnitAccessMode: 権限解決の中核メソッド

```go
func (p *Permission) UnitAccessMode(unitType unit.Type) perm_model.AccessMode {
    // パス 1: unitsMode に明示的な設定がある場合
    if m, ok := p.unitsMode[unitType]; ok {
        return util.Iif(p.AccessMode >= perm_model.AccessModeAdmin, p.AccessMode, m)
    }
    // パス 2: デフォルトの AccessMode + 公開アクセスの最大値
    unitDefaultAccessMode := p.AccessMode
    unitDefaultAccessMode = max(unitDefaultAccessMode, p.anonymousAccessMode[unitType])
    unitDefaultAccessMode = max(unitDefaultAccessMode, p.everyoneAccessMode[unitType])
    hasUnit := slices.ContainsFunc(p.units, func(u *repo_model.RepoUnit) bool {
        return u.Type == unitType
    })
    return util.Iif(hasUnit, unitDefaultAccessMode, perm_model.AccessModeNone)
}
```

**判定フロー:**

```
UnitAccessMode(unitType)
  │
  ├─ unitsMode に当該 Unit がある?
  │   ├─ YES: Admin/Owner ならその権限を優先、それ以外は unitsMode の値
  │   └─ NO:  ↓
  │
  ├─ デフォルト権限 = max(AccessMode, anonymous[unitType], everyone[unitType])
  │
  └─ その Unit がリポジトリに存在する?
      ├─ YES: デフォルト権限を返す
      └─ NO:  None を返す
```

**設計上のポイント: Admin/Owner は Unit 権限をオーバーライドする。**
チームで「Code は Read のみ」と設定されていても、Admin なら全 Unit にフルアクセスできる。

### 便利メソッド群

Permission 構造体は `CanRead`, `CanWrite` 等の便利メソッドを提供。全て `UnitAccessMode` に委譲:

```go
func (p *Permission) CanRead(unitType unit.Type) bool {
    return p.CanAccess(perm_model.AccessModeRead, unitType)
}
func (p *Permission) CanWrite(unitType unit.Type) bool {
    return p.CanAccess(perm_model.AccessModeWrite, unitType)
}
func (p *Permission) CanAccess(mode perm_model.AccessMode, unitType unit.Type) bool {
    return p.UnitAccessMode(unitType) >= mode  // ← ここで >= 比較
}
```

### Unit 型の一覧

ソース: [`models/unit/unit.go`](../../models/unit/unit.go)

```go
type Type int
const (
    TypeInvalid      Type = iota  // 0: 無効
    TypeCode                      // 1: ソースコード
    TypeIssues                    // 2: イシュー
    TypePullRequests              // 3: プルリクエスト
    TypeReleases                  // 4: リリース
    TypeWiki                      // 5: Wiki
    // 6-9: 予約済み
    TypeActions                   // 10: CI/CD Actions
)
```

チーム（`TeamUnit`）はこの Unit ごとに個別の AccessMode を持てる:

```
例: DevOps チーム
  Code         → Write
  Issues       → Read
  PullRequests → Write
  Actions      → Admin
  Wiki         → None（アクセス不可）
```

---

## 4. 権限解決フロー: GetUserRepoPermission

`GetUserRepoPermission()` が Permission 構造体を構築する核心関数。

### ソース位置

[`models/perm/access/repo_permission.go:317`](../../models/perm/access/repo_permission.go) — 【読み順 STEP 7】

### 判定の優先順

```
GetUserRepoPermission(ctx, repo, user)
  │
  ├─ ① 匿名 + プライベートリポ → None（即終了）
  │
  ├─ ② Owner の可視性チェック（プライベート組織の公開リポ等）
  │     → 不可視かつコラボレータでもない → None
  │
  ├─ ③ 匿名 + 公開リポ → Read（即終了）
  │
  ├─ ④ サイト管理者 or リポオーナー → Owner（即終了）
  │
  ├─ ⑤ access テーブル参照（コラボレータ権限のキャッシュ）
  │
  ├─ ⑥ 個人リポの場合 → ⑤の結果で終了
  │
  └─ ⑦ 組織リポの場合:
       ├─ 公開リポなら最低 Read を保証（Restricted ユーザーを除く）
       ├─ コラボレータなら全 Unit に AccessMode を適用
       ├─ Owners チーム所属 → Owner（即終了）
       └─ 各チームの Unit 権限を集計 → Unit ごとに max() で最大値を採用
```

### チーム権限の集計ロジック（⑦の詳細）

```go
// 全チームの Unit 権限を集計
for _, u := range repo.Units {
    for _, team := range teams {
        teamMode, _ := team.UnitAccessModeEx(ctx, u.Type)
        unitAccessMode := max(perm.unitsMode[u.Type], minAccessMode, teamMode)
        perm.unitsMode[u.Type] = unitAccessMode
    }
}
```

**例: ユーザーが 2 チームに所属している場合:**

```
チーム A: Code=Write, Issues=Read
チーム B: Code=Read,  Issues=Write

結果:    Code=Write, Issues=Write  ← 各 Unit で max()
```

### finalProcessRepoUnitPermission: 後処理

権限解決の最後に呼ばれる後処理関数。以下を行う:

1. **anonymous/everyone アクセスの適用**: 各 Unit の公開アクセス設定を反映
2. **権限なし Unit の除去**: `unitsMode` にも `anonymousAccessMode` にも `everyoneAccessMode` にもない Unit を `units` リストから除去

---

## 5. 権限キャッシュ: Access テーブル

### 構造

ソース: [`models/perm/access/access.go`](../../models/perm/access/access.go)

```go
type Access struct {
    ID     int64
    UserID int64        // UNIQUE(UserID, RepoID)
    RepoID int64
    Mode   AccessMode   // コラボ + チーム権限の最大値
}
```

### なぜキャッシュするのか

権限チェックはリクエストごとに発生する。チーム × メンバー × Unit の JOIN を毎回実行するとコストが高い。事前計算した最大値を **1 行の SELECT** で取得できるようにしている。

### キャッシュの再構築

| 関数 | 呼ばれるタイミング | 処理内容 |
|------|-------------------|---------|
| `RecalculateAccesses()` | コラボレータ追加/削除 | 全ユーザー分を再計算 |
| `RecalculateTeamAccesses()` | 組織チームの権限変更 | コラボ + 全チームの権限を再計算 |
| `RecalculateUserAccess()` | 特定ユーザーの権限変更 | 1 ユーザー分のみ更新（軽量版） |

### refreshAccesses の最適化

```go
func refreshAccesses(ctx context.Context, repo *repo_model.Repository, accessMap map[int64]*userAccess) error {
    minMode := perm.AccessModeRead
    // 公開リポ + 個人リポなら Write 未満の権限はキャッシュ不要
    // （Read は暗黙的に許可されるため）
    if !repo.IsPrivate && !repo.Owner.IsOrganization() {
        minMode = perm.AccessModeWrite
    }
    // minMode 未満の権限は保存しない（行数を削減）
    // ...
}
```

**設計判断:** 公開リポの Read 権限はキャッシュしない。全ユーザーが暗黙的に Read を持つため、行数を節約している。

---

## 6. 認可ミドルウェア: 宣言的な権限チェック

### ソース位置

[`services/context/permission.go`](../../services/context/permission.go) — 【読み順 STEP 8】

### ルーティングでの使用例

```go
// リポ管理者のみ
m.Get("/settings", RequireRepoAdmin(), repo.Settings)

// Code Unit の書き込み権限
m.Post("/upload", RequireUnitWriter(unit.TypeCode), repo.Upload)

// Issues Unit の読み取り権限
m.Get("/issues", RequireUnitReader(unit.TypeIssues), repo.Issues)
```

### ミドルウェアの実装

```go
func RequireRepoAdmin() func(ctx *Context) {
    return func(ctx *Context) {
        if !ctx.IsSigned || !ctx.Repo.IsAdmin() {
            ctx.NotFound(nil)
            return
        }
    }
}

func RequireUnitWriter(unitTypes ...unit.Type) func(ctx *Context) {
    return func(ctx *Context) {
        if slices.ContainsFunc(unitTypes, ctx.Repo.CanWrite) {
            return  // 権限あり → 次のハンドラへ
        }
        ctx.NotFound(nil)  // 権限なし → 404
    }
}
```

### 重要な設計判断: 403 ではなく 404

権限不足時に `403 Forbidden` ではなく `404 Not Found` を返す。リポジトリの**存在自体**を権限のないユーザーに漏らさないため。GitHub も同じアプローチを採用している。

---

## 7. 設計パターンの抽出

### パターン一覧

| # | パターン | Gitea での実例 | 汎用的な原則 |
|---|---------|---------------|------------|
| 1 | int 比較 enum | `AccessMode >= AccessModeWrite` | 階層的権限は int で表現し `>=` で判定 |
| 2 | 権限キャッシュ | Access テーブル + RecalculateAccesses | 頻繁な権限チェックは事前計算で高速化 |
| 3 | Unit 分離 | `unitsMode map[unit.Type]AccessMode` | リソース種別ごとに独立した権限を持たせる |
| 4 | max 集計 | チーム権限の最大値採用 | 複数ロール所属時は最も広い権限を採用 |
| 5 | 宣言的ミドルウェア | `RequireUnitWriter(unit.TypeCode)` | 認可ロジックをルート定義に宣言的に記述 |
| 6 | 404 隠蔽 | 権限不足で NotFound | リソースの存在自体を漏らさない |
| 7 | Owner 入力拒否 | ParseAccessMode で owner を除外 | 最高権限はシステム判定のみ、外部入力不可 |
| 8 | Admin オーバーライド | UnitAccessMode で Admin 以上は unitsMode を無視 | 管理者は細粒度設定に縛られない |

### パターン詳細

#### パターン 1: int 比較 enum

**問題:** 複数の権限レベルがあり、「この権限以上か」を頻繁に判定する必要がある
**解法:** 権限を int 型の enum で定義し、`>=` で比較
**利点:** 条件分岐が不要。新レベル追加時も既存コードに影響なし
**適用条件:** 権限が**完全に順序付けられる**場合のみ有効。「Code は書けるが Issues は読めない」のような直交する権限には向かない（→ Unit 分離パターンと組み合わせる）

#### パターン 4: max 集計

**問題:** ユーザーが複数のグループ（チーム）に所属し、それぞれ異なる権限を持つ
**解法:** 全グループの権限の最大値を採用（楽観的アプローチ）
**利点:** シンプルで予測しやすい。「どれか一つでも許可していれば許可」
**注意:** 最小値（悲観的）を採用する設計もありえる。Gitea は「最大値」を選択している。セキュリティ要件次第で使い分ける

#### パターン 6: 404 隠蔽

**問題:** 権限のないリソースに 403 を返すと、リソースの存在を攻撃者に漏らす
**解法:** 403 の代わりに 404 を返す
**適用条件:** リソースの存在自体が機密情報である場合（リポジトリ、プライベート API 等）。公開リソースには不要

---

## 8. TCG Match Manager への適用

**現状:** Admin API / Player API の 2 系統で分離
**課題:** ロール拡張時（ジャッジ、観戦者等）にルーティングの分岐が複雑化する

### 適用案

```go
// 1. 段階的ロール定義（Gitea パターン 1: int 比較 enum）
type Role int
const (
    RoleNone      Role = iota // 未認証
    RoleViewer                // 観戦者（閲覧のみ）
    RolePlayer                // プレイヤー（結果提出可）
    RoleJudge                 // ジャッジ（結果修正可）
    RoleOrganizer             // 主催者（トーナメント管理）
)

// 2. リソース種別（Gitea パターン 3: Unit 分離）
type Resource int
const (
    ResourceTournament Resource = iota
    ResourceMatch
    ResourceResult
    ResourceStanding
)

// 3. 宣言的ミドルウェア（Gitea パターン 5）
api := router.Group("/api/v1")
api.GET("/tournaments/:id", RequireRole(RoleViewer), h.GetTournament)
api.POST("/matches/:id/result", RequireRole(RolePlayer), h.SubmitResult)
api.PUT("/matches/:id/result", RequireRole(RoleJudge), h.OverrideResult)
api.POST("/tournaments", RequireRole(RoleOrganizer), h.CreateTournament)

// 4. ミドルウェア実装（Gitea パターン 6: 404 隠蔽）
func RequireRole(minRole Role) gin.HandlerFunc {
    return func(c *gin.Context) {
        role := c.MustGet("userRole").(Role)
        if role < minRole {
            c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
            c.Abort()
            return
        }
        c.Next()
    }
}
```

### 段階的導入ステップ

1. `Role` の int enum を定義し、既存の Admin/Player を `RolePlayer` / `RoleOrganizer` にマッピング
2. ミドルウェアで `>= RolePlayer` 判定に移行（2 系統のルーティングを統合）
3. 必要に応じて `RoleViewer`, `RoleJudge` を追加（既存コードの変更不要）

### Gitea との対比

| Gitea | TCG Match Manager | 備考 |
|-------|-------------------|------|
| AccessMode (5 段階) | Role (5 段階) | int 比較は同じ |
| Unit (Code, Issues...) | Resource (Tournament, Match...) | 将来の細粒度制御に備える |
| Access テーブル | Firestore ドキュメント | キャッシュ戦略は異なる |
| Team × Unit 権限 | 当面不要 | 組織機能がないため |
| chi ミドルウェア | Gin ミドルウェア | API はほぼ同じ |
