"""
analyzer/registry_plugins.py
============================
プラグインの自己登録と「あるものを回す」動的巡回ディスパッチャですわ！
圏論における Reader Applicative を用いて、有効化されたプラグインのみを安全に合成・実行いたしますの。
"""

import functools
import importlib
import logging
from abc import ABC, abstractmethod
from dataclasses import dataclass, field
from typing import Any, Callable, Dict, List, Optional, Type

from .core import AudioContext, FeatureExtractor, product_all
from .types_features import RawFeatures

logger = logging.getLogger("analyzer.registry_plugins")


@dataclass
class PluginMetadata:
    """プラグインのメタデータですわ。"""

    name: str
    description: str
    enabled_by_default: bool = True
    priority: int = 100  # 小さいほど先に実行
    options: dict[str, Any] = field(default_factory=dict)


class BasePlugin(ABC):
    """すべての音響解析プラグインの抽象基底クラスですわ。"""

    metadata: PluginMetadata

    @abstractmethod
    def extract(self, ctx: AudioContext) -> dict[str, Any]:
        """純粋な特徴量抽出を実行し、結果の辞書またはデータクラスを返却しますわ。

        Mor: AudioContext -> dict[str, Any]
        """
        pass

    def as_feature_extractor(self) -> FeatureExtractor[dict[str, Any]]:
        """Reader Applicative の FeatureExtractor に持ち上げますの。"""
        return FeatureExtractor(
            lambda ctx: self.extract(ctx),
            name=f"Plugin[{self.metadata.name}]",
        )


class PluginRegistry:
    """プラグインの自己登録・管理・ディスパッチを行うシングルトンレジストリですわ！"""

    _plugins: dict[str, BasePlugin] = {}
    _discovered: bool = False

    @classmethod
    def register(cls, plugin_instance: BasePlugin) -> BasePlugin:
        """プラグインインスタンスを登録しますわ。"""
        name = plugin_instance.metadata.name
        cls._plugins[name] = plugin_instance
        logger.debug(f"[Registry] プラグイン登録: {name} (priority={plugin_instance.metadata.priority})")
        return plugin_instance

    @classmethod
    def get_plugin(cls, name: str) -> Optional[BasePlugin]:
        return cls._plugins.get(name)

    @classmethod
    def get_all_plugins(cls) -> dict[str, BasePlugin]:
        return dict(cls._plugins)

    @classmethod
    def get_sorted_plugins(cls) -> list[BasePlugin]:
        """priority 順（昇順）にソートされたプラグイン一覧を返却しますわ。"""
        return sorted(cls._plugins.values(), key=lambda p: p.metadata.priority)

    @classmethod
    def auto_discover(cls):
        """標準プラグイン群を自動インポートして登録を完了させますわ！"""
        if cls._discovered:
            return
        cls._discovered = True

        plugin_modules = [
            "analyzer.librosa_dynamics",
            "analyzer.librosa_spectral",
            "analyzer.librosa_tonal",
            "analyzer.librosa_rhythm",
            "analyzer.librosa_timbre",
            "analyzer.librosa_vocalpitch",
            "analyzer.scipy_stats",
            "analyzer.psychoacoustics_din45692",
            "analyzer.structure_ssm",
            "analyzer.voice_cpp",
            "analyzer.audio_cutoff_lufs",
        ]

        for mod_name in plugin_modules:
            try:
                importlib.import_module(mod_name)
                logger.debug(f"[Registry] モジュールロード成功: {mod_name}")
            except ImportError as e:
                logger.warning(f"[Registry] モジュール {mod_name} のロードをスキップいたしました: {e}")


def register_plugin(
    name: str,
    description: str = "",
    enabled_by_default: bool = True,
    priority: int = 100,
    options: dict[str, Any] | None = None,
):
    """クラスに付与してプラグインを自動登録するデコレータですわ！"""

    def decorator(cls: Type[BasePlugin]):
        meta = PluginMetadata(
            name=name,
            description=description,
            enabled_by_default=enabled_by_default,
            priority=priority,
            options=options or {},
        )
        instance = cls()
        instance.metadata = meta
        PluginRegistry.register(instance)
        return cls

    return decorator
