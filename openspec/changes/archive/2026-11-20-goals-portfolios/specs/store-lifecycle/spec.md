## ADDED Requirements

### Requirement: ゴールとポートフォリオのキャッシュの片付け
ワークスペースが消えたときは、そのゴール・支えているものの一覧・期間・取得時刻の記録を消し、自分のポートフォリオでなくなったものは、自分のポートフォリオに (入れ子でも) 入っていなければ中身ごと消す (MUST)。初期化 (reset) ではすべて消す。ゴールの指標と期間は JSON の列に置き、読むときに termsafe を通す。
根拠: internal/store/goals.go; internal/store/write.go (PruneWorkspaces); internal/store/reset.go; TestGoalsGoWithTheirWorkspaceAndReset, TestPortfoliosPrune, TestGoalsAreTermsafe

#### Scenario: 入れ子のポートフォリオ
- **WHEN** 自分のポートフォリオ A の中のポートフォリオ N の、さらに中のポートフォリオ NN を開いたあと、ポートフォリオの一覧を取り直す
- **THEN** A が自分のものである間は NN も残り、A が一覧から消えると N と NN も消える
