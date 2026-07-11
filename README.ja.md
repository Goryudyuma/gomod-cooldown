# gomod-cooldown

[English](README.md)

`gomod-cooldown` は、依存関係の更新時に使うローカルCLIラッパーです。一時的に
loopback限定のGOPROXYを起動し、version discoveryだけをフィルタしてからコマンドを
実行し、終了時にproxyを停止します。

> このツールは依存関係更新中のversion discoveryをフィルタします。明示指定した
> versionや、すでにpinされているversionのダウンロードを禁止するものではありません。

これは運用を補助するツールであり、セキュリティ境界やダウンロード遮断proxyでは
ありません。
Go projectまたはGoogleと提携・承認・スポンサー関係はありません。

## インストール

```sh
go install github.com/Goryudyuma/gomod-cooldown/cmd/gomod-cooldown@latest
```

チェックアウトしたソースからは、`go build ./cmd/gomod-cooldown` でbuildできます。

## 使い方

```sh
gomod-cooldown --cooldown=14d -- go get -u all
```

構文は `gomod-cooldown [flags] -- command [args...]` です。コマンドはshellを経由せず、
元のargvのまま実行されます。子プロセスの `GOPROXY` には一時ローカルURLだけが設定
され、呼び出し元の環境変数や `go env` 設定は変更されません。

フラグ:

- `--cooldown=14d`: Goのduration形式に加えて、`d` を厳密に24時間として受け付けます。
  例: `168h`、`7d`、`14d12h`。正の値が必要です。
- `--upstream=https://proxy.golang.org`: 単一の固定upstream GOPROXYを指定します。
- `--time-source=commit`: 既定値です。`.info.Time` だけを使い、通常のGo commandと
  同じくmodule単位のdiscovery requestだけで完了します。`combined` はindex timestampも
  使う、高コストの明示opt-inモードです。
- `--upstream-timeout=30s`: upstream HTTP requestのタイムアウトです。
- `--verbose`: upstream requestと判定の詳細を出力します。

## フィルタ対象

フィルタするのは、次のversion discovery endpointだけです。

```
/<module>/@v/list
/<module>/@latest
```

`.info`、`.mod`、`.zip`を含むversion-specific endpointと、その他のGOPROXY endpointは
すべて指定upstreamへ透過します。そのため、`go get example.com/mod@v1.2.3` のような
明示指定や、`go.mod` にすでに記録されたversionはcooldown中でもダウンロードできます。

子プロセスの `GOPROXY` に `https://proxy.golang.org,direct` などのfallbackは追加しません。
discovery endpointが返す404が後段proxyで迂回されないようにするためです。

## availability time

`.info.Time` はcommit timeであり、公開日時ではありません。新しいtagが古いcommitを
指すことがあるためです。既定値がcommit modeなのは、module単位のproxy requestだけで
判定でき、対話的なCLI利用でも実用的だからです。

公式の `index.golang.org` feedには、module versionが `proxy.golang.org` に最初にcache
された時刻が含まれます。このfirst-cached timeはavailability timeとして扱いますが、
厳密なtag公開日時ではありません。

`--time-source=combined` は明示opt-inです。indexにはmodule単位のlookup APIがないため、
起動時にcutoff直前から現在までの時系列global feedを `since` と `limit=2000` で最終short
pageまで読み切ります。cooldown期間が長いと時間がかかることがあります。不正record、
HTTP失敗、timeout、cursorの進行停止はfail closedで扱い、commit timeだけへ黙ってfallback
しません。このモードでは正確に `https://proxy.golang.org` をupstreamとして指定する必要が
あります。

候補versionごとの判定は次のとおりです。

```
availableAt = max(commitTime, firstCachedTime)
cutoff = now - cooldown
availableAt <= cutoff なら許可
```

完全に取得したrecent snapshotに存在しないversionは、first-cached timeがcutoff以前で
あったものとして扱います。indexとproxyの反映タイミングにはraceがあり得るため、
これは厳密なセキュリティ境界ではありません。

upstreamの `@latest` が新しすぎるときは、`@v/list` をフィルタし、許可されたtagged
releaseのうちsemantic versionが最も高いものを返します。releaseがなければ最も高い
pre-releaseを返します。pseudo-versionは新たに作りません。そのため、古い候補が
pseudo-versionだけのmoduleでは `@latest` が404になることがあります。一方で、pin済み
pseudo-versionのversion-specific endpointは引き続きダウンロードできます。

## 注意点とトラブルシューティング

- `GOPRIVATE` や `GONOPROXY` によってGo commandがこのproxyを迂回することがあります。
  これはGo標準の挙動であり、このツールはprivate moduleの取得を制御しません。
- module cacheに既存データがあれば、Go commandはnetwork requestを行わない場合が
  あります。proxyの挙動を確認する際はfresh cacheを使ってください。
- 502はdiscovery metadataまたは完全なindex snapshotを検証できなかったことを表します。
  空の候補一覧として扱わず、原因をstderrへ出力します。
- 除外したversionについては、module、version、commit time、取得できたfirst-cached
  time、effective availability time、cutoffをログに記録します。
- Dependabot security updatesの代替ではありません。併用することを想定しています。

## 開発

```sh
gofmt -w cmd internal
go mod tidy
go test ./...
go vet ./...
go test -race ./...
golangci-lint run
golangci-lint fmt
```

テストは `httptest.Server`、inject可能なHTTP clientとclockを使うため、外部networkに
依存しません。
GitHub Actionsはテスト、race検出、vet、`golangci-lint`を実行します。安全な
`gofmt`/`goimports`の差分があれば、週次または手動実行のワークフローが整形用PRを作成します。

## ライセンスと第三者通知

このプロジェクトは [Apache License 2.0](LICENSE) で提供されます。静的リンクする
依存関係の通知は [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md) を参照してください。

## Inspiration

このプロジェクトは [imjasonh/go-cooldown](https://github.com/imjasonh/go-cooldown) に
着想を得ています。明示指定・pin済みversionをダウンロード可能に保ちながら、
version discovery endpointだけをフィルタする点が異なります。
