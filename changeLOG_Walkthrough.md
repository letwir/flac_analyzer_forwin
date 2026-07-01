# Walkthrough: アーキテクチャ拡張の圏論的検証

本ドキュメントは、新たな特徴量（Phase, Coherence 等）の導入およびワーカー分割の妥当性検証の履歴を記録しますの。

## 変更の概要 (Changes Made)

1. **アーキテクチャの圏論的検証**:
   - `librosa_worker.py` と `scipy_worker.py` の分割について、これを「計算ライブラリの境界」ではなく、「射 (Morphism) の集合」として定義づけることで圏論的純粋性が向上することを確認しました。
   - `precache.py` は、生波形を周波数領域に引き上げる関手 (Functor) として位置づけ、共有メモリを介した不変 (Immutable) なコンテキストとして提供する設計を採用しました。

2. **新規メトリクスの評価**:
   - **Phase spectrum / PSD / Band-limited envelope**: 単一の Stem に対する射 ($X \to F$) であり、既存アーキテクチャに副作用なく組み込めることを確認。
   - **Coherence (ステム間相関)**: $\text{Stem} \times \text{Stem} \to \text{Feature}$ という「直積 (Categorical Product)」の導入が必要になるため、DBの JSONB 構造に `cross_stems` のような新しいカテゴリ対象を追加することを決定しました。

## テスト内容 (What was tested)

- 本フェーズは設計検証（Planning）です。実装段階へ移行するにあたり、旦那様からのフィードバック（JSONBの構造案、優先する特徴量の選定、PyWavelets導入の可否）を待機しております。

## 検証結果 (Validation results)

- ワーカー分割と新規特徴量の導入は、明示的に「直積対象」を定義する限りにおいて、**圏論的破綻を引き起こさない（むしろ純粋性を高める）** と結論付けました。