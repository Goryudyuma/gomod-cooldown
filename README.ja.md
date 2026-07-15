# gomod-cooldown

[English](README.md)

`gomod-cooldown` は、新しく利用可能になったGo module versionを、設定したcooldown期間が
過ぎるまで依存関係更新の候補から外すCLIです。一時的にloopback限定のGOPROXYを起動し、
version discoveryだけをフィルタしてからコマンドを実行し、終了時にproxyを停止します。

> このツールは依存関係更新中のversion discoveryをフィルタします。明示指定した
> versionや、すでにpinされているversionのダウンロードを禁止するものではありません。

これは運用を補助するツールであり、セキュリティ境界やダウンロード遮断proxyでは
ありません。
Go projectまたはGoogleと提携・承認・スポンサー関係はありません。

## インストール

`gomod-cooldown` のbuildとinstallにはGo 1.25以降が必要です。

v1.0.0の公開後は、再現可能な環境にするため、そのstable releaseをexact指定して
installします。

```sh
go install github.com/Goryudyuma/gomod-cooldown/cmd/gomod-cooldown@v1.0.0
```

release candidateの評価期間中は、`v1.0.0-rc.1` の公開後にexactなcandidate tagを
使います。未公開の `@v1.0.0` はまだ利用できません。

```sh
go install github.com/Goryudyuma/gomod-cooldown/cmd/gomod-cooldown@v1.0.0-rc.1
```

常に公開済みの最新版を意図的に追う場合だけ、代わりに `@latest` を使います。tag付きの
stable releaseが1つもない場合だけ、`@latest` はpre-releaseを候補にします。exact versionを
使うと、version discoveryのタイミングにも左右されずにinstallできます。

チェックアウトしたソースからは、`go build ./cmd/gomod-cooldown` でbuildできます。

## 使い方

```sh
cd /path/to/your/module
gomod-cooldown --cooldown=14d -- go get -u -t ./...
go mod tidy
```

更新対象moduleのrootで実行してください。`-t` はtestが使う依存関係も更新します。testの
依存関係を更新しない場合は、`-t` を外して `go get -u ./...` を使います。更新後は
`go.mod` と `go.sum` の差分を確認し、そのmoduleのtestを実行してください。

このworkflowでは `go get -u all` を避けてください。`all` は、更新前のpackage graphに
ある依存module内のinternal packageも、提供moduleを更新しながら対象として保持することが
あります。新しいmoduleからそのinternal packageが削除されていると、main moduleが直接
importしていなくても `does not contain package` で失敗することがあります。`./...` に
限定すると、main module内のpackageを起点に更新できます。

構文は `gomod-cooldown [flags] -- command [args...]` です。コマンドはshellを経由せず、
元のargvのまま実行されます。子プロセスの `GOPROXY` には一時ローカルURLだけが設定
され、呼び出し元の環境変数や `go env` 設定は変更されません。

フラグ:

- `--cooldown=14d`: Goのduration形式に加えて、`d` を厳密に24時間として受け付けます。
  例: `168h`、`1.5d`、`7d`、`14d12h`。`1.5d` は36時間です。正の値が必要です。
- `--upstream=https://proxy.golang.org`: 単一の固定upstream GOPROXYを指定します。
- `--time-source=commit`: 既定値です。`.info.Time` だけを使い、通常のGo commandと
  同じくmodule単位のdiscovery requestだけで完了します。`combined` はindex timestampも
  使う、高コストの明示opt-inモードです。
- `--upstream-timeout=30s`: upstream HTTP requestのタイムアウトです。
- `--verbose`: upstream requestと判定の詳細を出力します。

`--help` と `-h` は `--` なしでcommand helpをstdoutへ表示し、正常終了します。
`--version` はstdoutへ `gomod-cooldown <version>` を表示します。module version metadataが
埋め込まれていないbuildではversionは `devel` です。

次のexit statusはv1 CLI contractの一部です。

- `0`: 子commandが成功した場合、またはhelp/versionを要求した場合。
- `1`: wrapperのsetupまたは内部処理に失敗した場合。
- `2`: CLIの使い方が不正な場合。
- `126`: 子commandは見つかったが起動できなかった場合。
- `127`: 子commandが見つからなかった場合。
- それ以外では、子commandが通常終了したstatusをそのまま返します。LinuxとmacOSでは
  子commandとその子孫を専用process groupで実行し、wrapperに届いたSIGINTとSIGTERMを
  そのgroupへ1回だけforwardします。対話的なcontrolling terminalでは、実行中だけ
  子process groupをforegroundにするため、terminal inputとterminal由来のSIGINT/SIGTERMは
  通常どおり動作します。signal終了は `128 + signal`（SIGINTは`130`、SIGTERMは`143`）として
  返します。

helpとversionの出力先はstdoutです。wrapperの診断はstderrへ出力し、子processは呼び出し元の
stdin、stdout、stderrを引き継ぎます。

## フィルタ対象

フィルタするのは、次のversion discovery endpointだけです。

```
/<module>/@v/list
/<module>/@latest
```

`.info`、`.mod`、`.zip`を含むversion-specific endpointと、その他のGOPROXY endpointは
すべて指定upstreamへ透過します。そのため、`go get example.com/mod@v1.2.3` のような
明示指定や、`go.mod` にすでに記録されたversionはcooldown中でもダウンロードできます。

upstreamの `@v/list` に含まれるversionの `.info` endpointが404または410を返す場合、
その取得不能なversionだけを使用不能としてdiscoveryから除外します。このnegative resultは
同じCLI実行が終わるまでcacheします。403、429、5xx、通信エラー、不正または整合しない
metadataなど、その他の `.info` 失敗は引き続きdiscovery全体を502でfail closedにします。

また、cooldownによってraw listにある取得可能な最高compatible versionが除外された
ことだけを理由に、それより高い `+incompatible` versionが暗黙のdiscoveryで選ばれない
ようにします。除外された
compatible versionの `.mod` が実際のmodule-awareなファイルなら、それより高い
`+incompatible` 候補も除外します。`module <path>` だけで構成されたsyntheticなlegacy
`.mod` はmodule-awareの根拠とみなさないため、その場合は候補を残します。exact指定または
pin済みの `+incompatible` versionは、version-specific endpointの透過によって引き続き
ダウンロードできます。

GOPROXYのdiscovery requestからは、元のversion queryや現在選択中のversionを判別できません。
そのため、この保護によって `@v2` のようなversion-prefix queryも暗黙のdiscoveryから
見えなくなる場合があります。この区別が必要な場合はexact versionを指定してください。

子プロセスの `GOPROXY` に `https://proxy.golang.org,direct` などのfallbackは追加しません。
discovery endpointが返す404が後段proxyで迂回されないようにするためです。

version判定用に検証済みの `.info` metadataも、1回のCLI実行中だけmemoryにcacheして
再利用します。すべてのcacheは終了時に破棄され、次回起動には引き継ぎません。変化し得る
`@v/list` と `@latest` 自体はcacheせず、requestごとにupstreamから取得します。

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

- LinuxまたはmacOSでwrapperが子processへterminalのforegroundを渡している間は、`Ctrl-Z`と
  `fg`を使う従来の対話的なshell job controlには未対応です。wrapper内のcommandをsuspendせず、
  対応済みのSIGINT/SIGTERMを使ってください。その他のUnix targetはbest effortです。非terminal
  の子processでは専用process groupを使いますが、character deviceのstdinでは対話入力を保つため
  wrapperと子processが同じgroupを使います。上記のsignal 1回配送の保証はLinux/macOSに限ります。
- cooldown判定はmodule pathごとに独立しています。同時にreleaseされたversionを組み合わせる
  必要がある関連module群について、全体の互換性を解決するものではありません。連携した更新が
  必要なecosystemでは、そのupgrade guideに従うか、互換性が分かっているexact version群を
  指定してください。
- 親moduleと新しく分割されたnested moduleの両方に同じpackageが含まれるために起きる
  ambiguous importは、このツールでは解決できません。古いmodule requirementを削除するか、
  意図したmodule versionを選んでから再実行してください。
- `go get example.com/mod@v1.2.3` のようなexact requestと、`go.mod` ですでにpinされた
  versionはversion-specific proxy endpointを使うため、cooldownでは保留されません。これは
  明示的なescape hatchとして意図した挙動です。
- `GOPRIVATE` や `GONOPROXY` によってGo commandがこのproxyを迂回することがあります。
  これはGo標準の挙動であり、このツールはprivate moduleの取得を制御しません。
- 対応するupstream GOPROXYは1つだけです。子プロセスにはローカルproxy URLだけを渡し、
  `,direct` や別proxyへのfallbackは追加しません。
- module cacheに既存データがあれば、Go commandはnetwork requestを行わない場合が
  あります。proxyの挙動を確認する際はfresh cacheを使ってください。
- 502はdiscovery metadataまたは完全なindex snapshotを検証できなかったことを表します。
  空の候補一覧として扱わず、原因をstderrへ出力します。
- 除外したversionについては、module、version、commit time、取得できたfirst-cached
  time、effective availability time、cutoffをログに記録します。
- Dependabot security updatesの代替ではありません。併用することを想定しています。

## 開発

最低対応toolchainはGo 1.25です。CIではGo 1.25.xと現在のstable Go releaseでcore testを
実行し、Linux、macOS、WindowsでCLIのtest、build、smoke testを行います。ローカルでの
全確認手順とcontribution processは [CONTRIBUTING.md](CONTRIBUTING.md) を参照してください。

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
依存しません。E2Eでは本物のGo commandをローカルfake GOPROXYに対して実行します。
また、Prometheus、Helm、Caddyの固定commitからbyte-for-byteで取得した `go.mod` も
検証します。fixtureの出典は
[`internal/cli/testdata/large-modules`](internal/cli/testdata/large-modules) に記録しています。
GitHub Actionsはtest、race検出、vet、`govulncheck`、version固定した`golangci-lint`、
cross-platform build smoke testを実行します。リポジトリ内のPRでは別workflowが
`gofmt`/`goimports`を実行し、安全な差分があれば整形用PRを作成・更新します。

## 互換性

v1の互換性contractには、flag名と意味、既定値、stdout/stderrの挙動、文書化したexit code、
上記のplatform別signal handling、`github.com/Goryudyuma/gomod-cooldown` のinstall/module pathが
含まれます。filteringの挙動を文書化済みpolicyに戻す修正patchによって、個別versionの判定結果が
変わることはあります。その変更は [CHANGELOG.md](CHANGELOG.md) に記録します。

## ライセンスと第三者通知

このプロジェクトは [MIT License](LICENSE) で提供されます。同梱する依存関係とtest
fixtureにはそれぞれのライセンスが引き続き適用されます。詳細は
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md) を参照してください。

## Inspiration

このプロジェクトは [imjasonh/go-cooldown](https://github.com/imjasonh/go-cooldown) に
着想を得ています。明示指定・pin済みversionをダウンロード可能に保ちながら、
version discovery endpointだけをフィルタする点が異なります。
